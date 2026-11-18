//go:build integration

package messaging_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"

	"subscription-platform/internal/platform/awsx"
	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/inbox"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/outbox"
	"subscription-platform/internal/platform/testsupport/awstest"
	"subscription-platform/internal/platform/testsupport/pgtest"
	sk "subscription-platform/internal/sharedkernel"
)

func TestMain(m *testing.M) {
	pgtest.AtExit(awstest.Terminate)
	pgtest.Main(m)
}

var seq atomic.Int64

func uniq(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano()%1e6, seq.Add(1))
}

func newEnv(t *testing.T, eventType string, tenant *sk.TenantID) sk.EventEnvelope {
	t.Helper()
	env, err := sk.NewEnvelope(eventType, "item", uuid.New(), 1, tenant, time.Now(), map[string]string{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	env.CorrelationID = "it-" + uuid.NewString()[:8]
	return env
}

func topologyFor(q messaging.QueueSpec) messaging.Topology {
	return messaging.Topology{Bus: "b", Queues: []messaging.QueueSpec{q}}
}

func consumerCfg(t *testing.T, url, dlq string) messaging.ConsumerConfig {
	return messaging.ConsumerConfig{Client: awstest.SQS(t), QueueURL: url, DLQURL: dlq, WaitTime: time.Second, RetryBase: 200 * time.Millisecond, RetryMax: time.Second, ShutdownGrace: 30 * time.Second}
}

func runConsumer(t *testing.T, p messaging.Processor, cfg messaging.ConsumerConfig) (stop func() error) {
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- messaging.RunConsumer(ctx, p, cfg) }()
	var once sync.Once
	var err error
	stop = func() error {
		once.Do(func() { cancel(); err = <-errc })
		return err
	}
	t.Cleanup(func() { _ = stop() })
	return stop
}

// heavyTimeout は大量のメッセージを通すテストの待ち時間の上限（CI の実行環境で MiniStack が遅くなることがあるため長めにする）。
const heavyTimeout = 180 * time.Second

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

func setupSchemas(t *testing.T, d *pgtest.DB, schemas ...string) {
	for _, s := range schemas {
		d.AsOwner(t, fmt.Sprintf("SELECT platform.create_messaging_tables('%s')", s))
	}
}

func appendEvents(t *testing.T, d *pgtest.DB, schema string, n int) {
	t.Helper()
	w := outbox.NewWriter(schema, sk.SystemClock{})
	tenant := sk.NewID[sk.TenantTag]()
	ctx := db.WithTenantScope(context.Background(), tenant)
	for start := 0; start < n; start += 200 {
		err := d.Pool.Module(schema).InTx(ctx, func(ctx context.Context, tx db.Tx) error {
			var evs []sk.EventEnvelope
			for i := start; i < min(start+200, n); i++ {
				evs = append(evs, newEnv(t, schema+".test_happened", &tenant))
			}
			return w.Append(ctx, tx, evs...)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// AC-PLT-019
func TestRelaySkipLockedDeliversAllOnce(t *testing.T) {
	d := pgtest.New(t)
	schemas := []string{"billing", "payments", "subscription"}
	setupSchemas(t, d, schemas...)
	for _, s := range schemas {
		appendEvents(t, d, s, 334)
	}
	bus := uniq("bus")
	url, _ := awstest.CreateQueue(t, uniq("relay"), awstest.QueueOptions{})
	awstest.CreateBusWithRule(t, bus, "all", `{"source":[{"prefix":"sp."}]}`, url)
	pub := &recordingPublisher{inner: messaging.NewEventBridgePublisher(awstest.EventBridge(t), bus), ok: map[string]int{}}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for range 2 {
		r := outbox.NewRelay(d.Pool, pub, schemas, outbox.RelayConfig{Idle: 50 * time.Millisecond})
		wg.Add(1)
		go func() { defer wg.Done(); _ = r.Run(ctx) }()
	}
	// 大量のメッセージを MiniStack に通すので、CI の混んだ実行環境では遅くなることがある。条件がそろえば待たずに進む
	eventually(t, heavyTimeout, func() bool {
		return d.QueryInt(t, `SELECT (SELECT count(*) FROM billing.outbox_events WHERE published_at IS NULL) + (SELECT count(*) FROM payments.outbox_events WHERE published_at IS NULL) + (SELECT count(*) FROM subscription.outbox_events WHERE published_at IS NULL)`) == 0
	}, "全件送信済み")
	cancel()
	wg.Wait()

	// ① SKIP LOCKED の本体：2 つの Relay で、EventBridge が受け付けた（PutEvents の entry が成功した）event がちょうど 1 回ずつ
	published, pubDups := pub.snapshot()
	if len(published) != 1002 || pubDups != 0 {
		t.Fatalf("EventBridge が受け付けた件数 %d、重複 %d（2 つの Relay で同じ行を送った）", len(published), pubDups)
	}

	// ② SQS 側：受け付けたもの以外は届かない。
	// MiniStack 1.5.32 は負荷の高い CI で EventBridge → SQS の配送を取りこぼしたり、二重に届けたりする（PutEvents は成功、P-IMP-26）。
	// 本番でも EventBridge → 標準キューは at-least-once で、重複は inbox が吸収する（AC-PLT-021）。欠けと重複は記録だけにする
	seen := map[string]int{}
	for _, b := range awstest.DrainAtLeast(t, url, 1002, 60*time.Second) {
		env, err := messaging.DecodeBody(b)
		if err != nil {
			t.Fatal(err)
		}
		id := env.EventID.String()
		if published[id] == 0 {
			t.Fatalf("受け付けていない event が届いた: %s", id)
		}
		seen[id]++
	}
	dups := 0
	for _, n := range seen {
		if n > 1 {
			dups++
		}
	}
	if missing := len(published) - len(seen); missing > 0 || dups > 0 {
		t.Logf("MiniStack の EventBridge → SQS で %d 件届かず、%d 件が重複した（PutEvents は成功。P-IMP-26）", missing, dups)
	}
}

// recordingPublisher は本物の Publisher を包み、成功した event_id ごとの送信回数を数える（複数の Relay から同時に呼ばれる）。
type recordingPublisher struct {
	inner messaging.Publisher
	mu    sync.Mutex
	ok    map[string]int
}

func (p *recordingPublisher) Publish(ctx context.Context, evs []sk.EventEnvelope) ([]messaging.PublishResult, error) {
	res, err := p.inner.Publish(ctx, evs)
	if err != nil {
		return res, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range res {
		if r.Err == nil {
			p.ok[r.EventID.String()]++
		}
	}
	return res, nil
}

func (p *recordingPublisher) snapshot() (map[string]int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make(map[string]int, len(p.ok))
	dups := 0
	for id, n := range p.ok {
		cp[id] = n
		if n > 1 {
			dups++
		}
	}
	return cp, dups
}

// AC-PLT-020
// MiniStack は存在しないバスへの PutEvents も受け付けるため、バスの削除では障害を再現できない。
// 到達できないエンドポイントに向けた本物の EventBridge クライアントで「EventBridge が止まっている」（E5）を再現する。
func TestRelayKeepsEventsWhileBusIsDown(t *testing.T) {
	d := pgtest.New(t)
	setupSchemas(t, d, "billing")
	appendEvents(t, d, "billing", 10)
	bus := uniq("bus")
	url, _ := awstest.CreateQueue(t, uniq("down"), awstest.QueueOptions{})
	dead := eventbridge.NewFromConfig(awsx.Static(awstest.Region, "http://127.0.0.1:1"), func(o *eventbridge.Options) { o.RetryMaxAttempts = 1 })
	down := outbox.NewRelay(d.Pool, messaging.NewEventBridgePublisher(dead, bus), []string{"billing"}, outbox.RelayConfig{})
	if _, err := down.FlushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := d.QueryInt(t, `SELECT count(*) FROM billing.outbox_events WHERE attempts = 1 AND published_at IS NULL AND next_attempt_at > now() AND last_error IS NOT NULL`); n != 10 {
		t.Fatalf("1 周目の後の失敗の記録 = %d", n)
	}
	awstest.CreateBusWithRule(t, bus, "all", `{"source":[{"prefix":"sp."}]}`, url)
	up := outbox.NewRelay(d.Pool, messaging.NewEventBridgePublisher(awstest.EventBridge(t), bus), []string{"billing"}, outbox.RelayConfig{})
	eventually(t, 15*time.Second, func() bool {
		_, _ = up.FlushOnce(context.Background())
		return d.QueryInt(t, `SELECT count(*) FROM billing.outbox_events WHERE published_at IS NOT NULL`) == 10
	}, "復旧後の周回で全件送信")
	if got := len(awstest.Drain(t, url)); got != 10 {
		t.Fatalf("届いた件数 = %d", got)
	}
}

func itemsTable(t *testing.T, d *pgtest.DB) {
	d.AsOwner(t, `CREATE TABLE billing.received (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, event_id uuid NOT NULL)`,
		`SELECT platform.enable_tenant_rls('billing.received')`)
}

// AC-PLT-021
func TestInboxDeduplicates(t *testing.T) {
	d := pgtest.New(t)
	setupSchemas(t, d, "billing")
	itemsTable(t, d)
	url, dlq := awstest.CreateQueue(t, uniq("inbox"), awstest.QueueOptions{MaxReceive: 5})
	q := messaging.QueueSpec{Name: "billing-test", Process: "worker-billing", Consumer: "billing.test", Kind: messaging.KindEvents, Concurrency: 2, VisibilityTimeoutS: 30, MaxReceive: 5}
	h := inbox.Wrap(d.Pool.Module("billing"), "billing.test", func(ctx context.Context, tx db.Tx, env sk.EventEnvelope) error {
		_, err := tx.Exec(ctx, `INSERT INTO billing.received (tenant_id, event_id) VALUES ($1, $2)`, env.TenantID, env.EventID.UUID())
		return err
	})
	disp, err := messaging.NewDispatcher(topologyFor(q), q.Name, []messaging.ConsumerSpec{{Name: "billing.test", Handler: h}})
	if err != nil {
		t.Fatal(err)
	}
	tenant := sk.NewID[sk.TenantTag]()
	body, _ := messaging.EncodeEventBridgeBody(newEnv(t, "payments.payment_succeeded", &tenant))
	for range 2 {
		if err := messaging.SendRaw(context.Background(), awstest.SQS(t), url, body); err != nil {
			t.Fatal(err)
		}
	}
	runConsumer(t, disp, consumerCfg(t, url, dlq))
	eventually(t, 20*time.Second, func() bool {
		v, f := awstest.ApproxCount(t, url)
		return v == 0 && f == 0 && d.QueryInt(t, `SELECT count(*) FROM billing.processed_messages`) == 1
	}, "2 通とも削除され、Inbox は 1 行")
	if n := d.QueryInt(t, `SELECT count(*) FROM billing.received`); n != 1 {
		t.Fatalf("業務の書き込み = %d", n)
	}
}

// AC-PLT-022
func TestFailingConsumerGoesToDLQAfterMaxReceive(t *testing.T) {
	d := pgtest.New(t)
	setupSchemas(t, d, "billing")
	url, dlq := awstest.CreateQueue(t, uniq("fail"), awstest.QueueOptions{MaxReceive: 5})
	q := messaging.QueueSpec{Name: "billing-fail", Process: "worker-billing", Consumer: "billing.fail", Kind: messaging.KindEvents, Concurrency: 1, VisibilityTimeoutS: 30, MaxReceive: 5}
	var calls atomic.Int64
	h := inbox.Wrap(d.Pool.Module("billing"), "billing.fail", func(context.Context, db.Tx, sk.EventEnvelope) error {
		calls.Add(1)
		return errors.New("always fails")
	})
	disp, _ := messaging.NewDispatcher(topologyFor(q), q.Name, []messaging.ConsumerSpec{{Name: "billing.fail", Handler: h}})
	tenant := sk.NewID[sk.TenantTag]()
	body, _ := messaging.EncodeEventBridgeBody(newEnv(t, "payments.payment_failed", &tenant))
	_ = messaging.SendRaw(context.Background(), awstest.SQS(t), url, body)
	runConsumer(t, disp, consumerCfg(t, url, dlq))
	eventually(t, 60*time.Second, func() bool { v, _ := awstest.ApproxCount(t, dlq); return v == 1 }, "DLQ に移る")
	if c := calls.Load(); c != 5 {
		t.Fatalf("呼び出し回数 = %d（max_receive 5）", c)
	}
	if n := d.QueryInt(t, `SELECT count(*) FROM billing.processed_messages`); n != 0 {
		t.Fatalf("processed_messages = %d", n)
	}
}

func plainDispatcher(t *testing.T, name string, concurrency, vt int, h messaging.Handler) *messaging.Dispatcher {
	q := messaging.QueueSpec{Name: name, Process: "worker-billing", Consumer: "billing.plain", Kind: messaging.KindEvents, Concurrency: concurrency, VisibilityTimeoutS: vt, MaxReceive: 5}
	disp, err := messaging.NewDispatcher(topologyFor(q), name, []messaging.ConsumerSpec{{Name: "billing.plain", Handler: h}})
	if err != nil {
		t.Fatal(err)
	}
	return disp
}

// AC-PLT-023（VT を 2 秒・処理 5 秒に縮めて同じ性質を確かめる）
func TestVisibilityIsExtendedWhileProcessing(t *testing.T) {
	url, dlq := awstest.CreateQueue(t, uniq("vt"), awstest.QueueOptions{VisibilityTimeout: 2, MaxReceive: 5})
	var calls atomic.Int64
	disp := plainDispatcher(t, "billing-vt", 1, 2, func(context.Context, sk.EventEnvelope) error {
		calls.Add(1)
		time.Sleep(5 * time.Second)
		return nil
	})
	body, _ := messaging.EncodeEventBridgeBody(newEnv(t, "billing.slow", nil))
	_ = messaging.SendRaw(context.Background(), awstest.SQS(t), url, body)
	runConsumer(t, disp, consumerCfg(t, url, dlq))
	eventually(t, 20*time.Second, func() bool { v, f := awstest.ApproxCount(t, url); return v == 0 && f == 0 && calls.Load() >= 1 }, "処理が終わって削除される")
	time.Sleep(2 * time.Second)
	if c := calls.Load(); c != 1 {
		t.Fatalf("呼び出し回数 = %d（処理中に再配信された）", c)
	}
}

// AC-PLT-024
func TestUnparseableGoesToDLQImmediately(t *testing.T) {
	url, dlq := awstest.CreateQueue(t, uniq("garbage"), awstest.QueueOptions{MaxReceive: 5})
	disp := plainDispatcher(t, "billing-garbage", 1, 30, func(context.Context, sk.EventEnvelope) error { return nil })
	_ = messaging.SendRaw(context.Background(), awstest.SQS(t), url, "{not json")
	runConsumer(t, disp, consumerCfg(t, url, dlq))
	eventually(t, 15*time.Second, func() bool {
		v, _ := awstest.ApproxCount(t, dlq)
		qv, qf := awstest.ApproxCount(t, url)
		return v == 1 && qv == 0 && qf == 0
	}, "DLQ に移り、元から消える")
}

// AC-PLT-063
func TestConcurrencyIsBounded(t *testing.T) {
	url, dlq := awstest.CreateQueue(t, uniq("collect"), awstest.QueueOptions{MaxReceive: 5})
	var cur, peak, done atomic.Int64
	disp := plainDispatcher(t, "orc-collection", 4, 30, func(context.Context, sk.EventEnvelope) error {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		cur.Add(-1)
		done.Add(1)
		return nil
	})
	sendMany(t, url, 100)
	runConsumer(t, disp, consumerCfg(t, url, dlq))
	eventually(t, heavyTimeout, func() bool { return done.Load() == 100 }, "100 件処理")
	if p := peak.Load(); p > 4 || p < 2 {
		t.Fatalf("同時実行の最大 = %d（上限 4）", p)
	}
}

func sendMany(t *testing.T, url string, n int) {
	t.Helper()
	c := awstest.SQS(t)
	for i := 0; i < n; i += 10 {
		var entries []sqsEntry
		for j := i; j < min(i+10, n); j++ {
			body, _ := messaging.EncodeEventBridgeBody(newEnv(t, "billing.invoice_collection_due", nil))
			entries = append(entries, sqsEntry{id: fmt.Sprint(j), body: body})
		}
		sendBatch(t, c, url, entries)
	}
}

type sqsEntry struct{ id, body string }

func sendBatch(t *testing.T, c *sqs.Client, url string, es []sqsEntry) {
	t.Helper()
	for _, e := range es {
		if _, err := c.SendMessage(context.Background(), &sqs.SendMessageInput{QueueUrl: aws.String(url), MessageBody: aws.String(e.body)}); err != nil {
			t.Fatal(err)
		}
	}
}

// AC-PLT-064
func TestGracefulShutdownWaitsForInFlight(t *testing.T) {
	url, dlq := awstest.CreateQueue(t, uniq("shutdown"), awstest.QueueOptions{MaxReceive: 5})
	var started, finished atomic.Int64
	release := make(chan struct{})
	disp := plainDispatcher(t, "billing-shutdown", 3, 30, func(context.Context, sk.EventEnvelope) error {
		started.Add(1)
		<-release
		time.Sleep(200 * time.Millisecond)
		finished.Add(1)
		return nil
	})
	sendMany(t, url, 5)
	stop := runConsumer(t, disp, consumerCfg(t, url, dlq))
	eventually(t, 15*time.Second, func() bool { return started.Load() == 3 }, "3 件が処理中")
	go func() { time.Sleep(time.Second); close(release) }()
	start := time.Now()
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if finished.Load() != 3 || started.Load() != 3 {
		t.Fatalf("started=%d finished=%d（新しい受信をせずに 3 件の完了を待つ）", started.Load(), finished.Load())
	}
	if time.Since(start) > 30*time.Second {
		t.Fatal("30 秒以内に終わらなかった")
	}
}

// direct モード（MESSAGING_MODE=direct）でも同じ本文で届く
func TestFanoutPublisher(t *testing.T) {
	name := uniq("fanout")
	url, _ := awstest.CreateQueue(t, name, awstest.QueueOptions{})
	tp := messaging.Topology{Bus: "b", Queues: []messaging.QueueSpec{{Name: name, Process: "worker-billing", Consumer: "billing.x", Kind: messaging.KindEvents, Events: []string{"prefix:payments."}}}}
	prefix := url[:len(url)-len(name)]
	pub := messaging.NewFanoutPublisher(awstest.SQS(t), tp, prefix)
	res, err := pub.Publish(context.Background(), []sk.EventEnvelope{newEnv(t, "payments.payment_succeeded", nil), newEnv(t, "billing.other", nil)})
	if err != nil || res[0].Err != nil || res[1].Err != nil {
		t.Fatalf("publish: %v %+v", err, res)
	}
	bodies := awstest.Drain(t, url)
	if len(bodies) != 1 {
		t.Fatalf("届いた件数 = %d", len(bodies))
	}
	var probe map[string]json.RawMessage
	_ = json.Unmarshal([]byte(bodies[0]), &probe)
	if _, ok := probe["detail-type"]; !ok {
		t.Fatalf("EventBridge 形式でない: %s", bodies[0])
	}
}
