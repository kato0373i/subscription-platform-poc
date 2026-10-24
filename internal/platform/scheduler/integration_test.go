//go:build integration

package scheduler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/observability"
	"subscription-platform/internal/platform/scheduler"
	"subscription-platform/internal/platform/testsupport/awstest"
	"subscription-platform/internal/platform/testsupport/pgtest"
	sk "subscription-platform/internal/sharedkernel"
)

func TestMain(m *testing.M) {
	pgtest.AtExit(awstest.Terminate)
	pgtest.Main(m)
}

var n atomic.Int64

type funcJob struct {
	name string
	fn   func(ctx context.Context, run scheduler.Run) (scheduler.Stats, error)
}

func (f funcJob) Name() string { return f.name }
func (f funcJob) Run(ctx context.Context, r scheduler.Run) (scheduler.Stats, error) {
	return f.fn(ctx, r)
}

type harness struct {
	d       *pgtest.DB
	svc     *scheduler.Service
	url     string
	clock   *sk.ManualClock
	stop    func()
	job     string
	process string
}

func newHarness(t *testing.T, jobName string, fn func(ctx context.Context, run scheduler.Run) (scheduler.Stats, error)) *harness {
	t.Helper()
	d := pgtest.New(t)
	s, err := scheduler.ParseSchedules([]byte(fmt.Sprintf("jobs:\n  - {name: %s, process: worker-billing, schedule: \"cron(0 3 * * ? *)\"}\n", jobName)))
	if err != nil {
		t.Fatal(err)
	}
	url, dlq := awstest.CreateQueue(t, fmt.Sprintf("scheduler-billing-%d", n.Add(1)), awstest.QueueOptions{MaxReceive: 3})
	clock := sk.NewManualClock(time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC))
	q := messaging.QueueSpec{Name: "scheduler-billing", Process: "worker-billing", Kind: messaging.KindScheduler, Concurrency: 1, VisibilityTimeoutS: 30, MaxReceive: 3}
	runner, err := scheduler.NewRunner(q, d.Pool, s, clock, []scheduler.Job{funcJob{jobName, fn}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = messaging.RunConsumer(ctx, runner, messaging.ConsumerConfig{Client: awstest.SQS(t), QueueURL: url, DLQURL: dlq, WaitTime: time.Second, RetryBase: 200 * time.Millisecond, RetryMax: time.Second})
		close(done)
	}()
	h := &harness{d: d, url: url, clock: clock, job: jobName}
	h.stop = func() { cancel(); <-done }
	t.Cleanup(h.stop)
	h.svc = scheduler.NewService(d.Pool, s, clock, func(ctx context.Context, _ string, body string) error {
		return messaging.SendRaw(ctx, awstest.SQS(t), url, body)
	})
	return h
}

func (h *harness) sendSchedule(t *testing.T) {
	t.Helper()
	body, _ := json.Marshal(scheduler.JobMessage{Job: h.job, Trigger: scheduler.TriggerSchedule})
	if err := messaging.SendRaw(context.Background(), awstest.SQS(t), h.url, string(body)); err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("時間内に成り立たなかった: %s", msg)
}

// AC-PLT-037
func TestScheduleMessagesAreDeduplicated(t *testing.T) {
	var calls atomic.Int64
	var corr atomic.Value
	h := newHarness(t, "billing-test-daily", func(ctx context.Context, run scheduler.Run) (scheduler.Stats, error) {
		calls.Add(1)
		c, _ := observability.CorrelationFrom(ctx)
		corr.Store(c == "job:billing-test-daily:"+run.ID.String()) // AC-PLT-082: スケジュール起動の相関 ID
		return scheduler.Stats{"done": 1}, nil
	})
	h.sendSchedule(t)
	h.sendSchedule(t)
	eventually(t, 20*time.Second, func() bool {
		v, f := awstest.ApproxCount(t, h.url)
		return v == 0 && f == 0 && h.d.QueryInt(t, `SELECT count(*) FROM platform.job_runs WHERE status = 'succeeded'`) == 1
	}, "2 通とも処理される")
	if c := calls.Load(); c != 1 || h.d.QueryInt(t, `SELECT count(*) FROM platform.job_runs`) != 1 {
		t.Fatalf("実行 %d 回、job_runs %d 件", c, h.d.QueryInt(t, `SELECT count(*) FROM platform.job_runs`))
	}
	if ok, _ := corr.Load().(bool); !ok {
		t.Fatal("スケジュール起動の相関 ID が job:<job>:<run_id> でない")
	}
	if h.d.QueryInt(t, `SELECT count(*) FROM platform.job_runs WHERE dedupe_key = 'billing-test-daily:2026-10-12' AND as_of = '2026-10-12'`) != 1 {
		t.Fatal("dedupe_key・as_of が業務時刻の当日でない")
	}
}

// AC-PLT-038
func TestFailedRunIsRetriedAsSameRun(t *testing.T) {
	var calls atomic.Int64
	h := newHarness(t, "billing-test-flaky", func(context.Context, scheduler.Run) (scheduler.Stats, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("first attempt fails")
		}
		return scheduler.Stats{"ok": 1}, nil
	})
	res, err := h.svc.Enqueue(context.Background(), h.job, scheduler.EnqueueOptions{Trigger: scheduler.TriggerDev})
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.svc.Wait(context.Background(), res.RunID, 20*time.Second)
	if err != nil || v.Status != "succeeded" || v.Attempts != 2 {
		t.Fatalf("run = %+v err = %v", v, err)
	}
	// 同じ日の 2 回目の dev 実行は既存の run を返す
	again, _ := h.svc.Enqueue(context.Background(), h.job, scheduler.EnqueueOptions{Trigger: scheduler.TriggerDev})
	if !again.Deduplicated || again.RunID != res.RunID {
		t.Fatalf("dedupe = %+v", again)
	}
	forced, _ := h.svc.Enqueue(context.Background(), h.job, scheduler.EnqueueOptions{Trigger: scheduler.TriggerDev, Force: true})
	if forced.Deduplicated || forced.RunID == res.RunID {
		t.Fatalf("force = %+v", forced)
	}
	if _, err := h.svc.Enqueue(context.Background(), "billing-unknown", scheduler.EnqueueOptions{}); !errors.Is(err, scheduler.ErrUnknownJob) {
		t.Fatalf("unknown job err = %v", err)
	}
}

// AC-PLT-062
func TestJobResumesFromCursor(t *testing.T) {
	var processed sync.Map
	var total, attempts atomic.Int64
	const scope = "2026-10-12"
	h := newHarness(t, "billing-test-bulk", nil)
	_ = h
	job := func(ctx context.Context, run scheduler.Run) (scheduler.Stats, error) {
		attempts.Add(1)
		st, err := run.Cursors.Load(ctx, scope)
		if err != nil {
			return nil, err
		}
		if st.Completed {
			return scheduler.Stats{"skipped": 1}, nil
		}
		start := 0
		if st.Cursor != nil {
			_ = json.Unmarshal(st.Cursor, &start)
		}
		saves := 0
		for i := start + 1; i <= 1200; i++ {
			if _, dup := processed.LoadOrStore(i, true); dup {
				return nil, fmt.Errorf("duplicate item %d", i)
			}
			total.Add(1)
			if i%500 == 0 {
				if err := run.Cursors.Save(ctx, nil, scope, json.RawMessage(strconv.Itoa(i))); err != nil {
					return nil, err
				}
				saves++
				if run.Attempt == 1 && saves == 2 {
					panic("simulated crash after second save")
				}
			}
		}
		return scheduler.Stats{"processed": total.Load()}, run.Cursors.Complete(ctx, nil, scope)
	}
	h2 := newHarness(t, "billing-test-bulk2", job)
	res, err := h2.svc.Enqueue(context.Background(), h2.job, scheduler.EnqueueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	v, _ := h2.svc.Wait(context.Background(), res.RunID, 30*time.Second)
	if v.Status != "succeeded" || v.Attempts != 2 || total.Load() != 1200 {
		t.Fatalf("run = %+v total = %d", v, total.Load())
	}
	if _, ok := processed.Load(1001); !ok {
		t.Fatal("1,001 件目を処理していない")
	}
	again, _ := h2.svc.Enqueue(context.Background(), h2.job, scheduler.EnqueueOptions{Force: true})
	v2, _ := h2.svc.Wait(context.Background(), again.RunID, 20*time.Second)
	if v2.Status != "succeeded" || total.Load() != 1200 || string(v2.Stats) != `{"skipped": 1}` {
		t.Fatalf("完了後の再実行 = %+v（stats %s）total = %d", v2, v2.Stats, total.Load())
	}
}

// AC-PLT-065
func TestCursorRollsBackWithBusinessWrite(t *testing.T) {
	d := pgtest.New(t)
	d.AsOwner(t, `CREATE TABLE billing.work (id int PRIMARY KEY, tenant_id uuid NOT NULL)`, `SELECT platform.enable_tenant_rls('billing.work')`)
	store := scheduler.NewCursorStore(d.Pool, "billing-test-tx", [16]byte{1})
	ctx := context.Background()
	tenant := sk.NewID[sk.TenantTag]()
	err := d.Pool.Module("billing").InTx(db.WithTenantScope(ctx, tenant), func(ctx context.Context, tx db.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO billing.work VALUES (1, $1)`, tenant); err != nil {
			return err
		}
		if err := store.Save(ctx, tx, "k", json.RawMessage(`1`)); err != nil {
			return err
		}
		return errors.New("rollback")
	})
	if err == nil || err.Error() != "rollback" {
		t.Fatalf("err = %v", err)
	}
	st, err := store.Load(ctx, "k")
	if err != nil || st.Cursor != nil || d.QueryInt(t, `SELECT count(*) FROM billing.work`) != 0 {
		t.Fatalf("カーソルが進んだ: %+v %v", st, err)
	}
	// コミットすれば両方残る
	_ = d.Pool.Module("billing").InTx(db.WithTenantScope(ctx, tenant), func(ctx context.Context, tx db.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO billing.work VALUES (1, $1)`, tenant); err != nil {
			return err
		}
		return store.Save(ctx, tx, "k", json.RawMessage(`1`))
	})
	if st, _ := store.Load(ctx, "k"); string(st.Cursor) != "1" {
		t.Fatalf("cursor = %s", st.Cursor)
	}
}

func TestPlatformGCJobs(t *testing.T) {
	d := pgtest.New(t)
	d.AsOwner(t, `SELECT platform.create_messaging_tables('billing')`)
	d.AsSuper(t,
		`INSERT INTO platform.idempotency_keys (scope, key, request_hash, status, expires_at) VALUES ('s', 'old', '\x00', 'completed', now() - interval '1 hour'), ('s', 'new', '\x00', 'completed', now() + interval '1 hour')`,
		`INSERT INTO billing.processed_messages (consumer, message_id, processed_at) VALUES ('c', gen_random_uuid(), now() - interval '31 days'), ('c', gen_random_uuid(), now())`,
		`INSERT INTO billing.outbox_events (id, event_type, aggregate_type, aggregate_id, aggregate_version, correlation_id, payload, occurred_at, published_at) VALUES
		 (gen_random_uuid(), 'billing.x', 'a', gen_random_uuid(), 1, 'c', '{}', now(), now() - interval '8 days'),
		 (gen_random_uuid(), 'billing.x', 'a', gen_random_uuid(), 1, 'c', '{}', now(), NULL)`,
	)
	for _, j := range scheduler.PlatformJobs(d.Pool, []string{"billing"}) {
		st, err := j.Run(context.Background(), scheduler.Run{})
		if err != nil || st["deleted"] > 1 {
			t.Fatalf("%s: %v %v", j.Name(), st, err)
		}
	}
	if d.QueryInt(t, `SELECT count(*) FROM platform.idempotency_keys`)+d.QueryInt(t, `SELECT count(*) FROM billing.processed_messages`)+d.QueryInt(t, `SELECT count(*) FROM billing.outbox_events`) != 3 {
		t.Fatal("消してはいけない行を消したか、消すべき行が残った")
	}
}
