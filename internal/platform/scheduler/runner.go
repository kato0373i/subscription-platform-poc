package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/observability"
	sk "subscription-platform/internal/sharedkernel"
)

// Trigger はジョブの起動元。
type Trigger string

const (
	TriggerSchedule Trigger = "schedule"
	TriggerDev      Trigger = "dev"
	TriggerOps      Trigger = "ops"
)

// Stats はジョブの件数の記録（job_runs.stats）。
type Stats map[string]any

// Job はモジュールが実装するジョブ。冪等に書く。
type Job interface {
	Name() string
	Run(ctx context.Context, run Run) (Stats, error)
}

// Run は 1 回の実行。
type Run struct {
	ID      uuid.UUID
	Job     string
	Trigger Trigger
	AsOf    sk.Date
	Params  json.RawMessage
	Attempt int
	Cursors CursorStore
}

// JobMessage は scheduler キューの本文。
type JobMessage struct {
	Job           string          `json:"job"`
	Trigger       Trigger         `json:"trigger"`
	RunID         *uuid.UUID      `json:"run_id,omitempty"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	AsOf          string          `json:"as_of,omitempty"`
	Params        json.RawMessage `json:"params,omitempty"`
}

// Runner は scheduler キューのメッセージを受けて job_runs を作成・ロックし、Job を実行して結果を記録する。
type Runner struct {
	queue     messaging.QueueSpec
	pool      *db.Pool
	platform  *db.ModuleDB
	jobs      map[string]Job
	schedules Schedules
	clock     sk.Clock
	lease     time.Duration
	log       *slog.Logger
}

// NewRunner は queue（scheduler-billing など）の Runner を作る。jobs は schedules.yaml に登録済みであること。
func NewRunner(queue messaging.QueueSpec, pool *db.Pool, s Schedules, clock sk.Clock, jobs []Job) (*Runner, error) {
	r := &Runner{queue: queue, pool: pool, platform: pool.Platform(), jobs: map[string]Job{}, schedules: s, clock: clock, lease: 5 * time.Minute, log: observability.Logger("platform.job")}
	for _, j := range jobs {
		if _, ok := s.Job(j.Name()); !ok {
			return nil, fmt.Errorf("scheduler: job %q is not in schedules.yaml", j.Name())
		}
		r.jobs[j.Name()] = j
	}
	return r, nil
}

func (r *Runner) Queue() messaging.QueueSpec { return r.queue }

// SetLease はリースの長さを変える（テスト用）。
func (r *Runner) SetLease(d time.Duration) { r.lease = d }

// Dispatch は 1 件ずつジョブを実行する。失敗したらメッセージを残して再配信させる（最大 max_receive 回で DLQ）。
func (r *Runner) Dispatch(ctx context.Context, msgs []messaging.Message) messaging.Outcome {
	var out messaging.Outcome
	for _, m := range msgs {
		err := r.handle(ctx, m)
		switch {
		case err == nil:
			out.Succeeded = append(out.Succeeded, m)
		case messaging.IsPermanent(err):
			out.Permanent = append(out.Permanent, m)
		default:
			out.Retry = append(out.Retry, m)
		}
	}
	return out
}

var errBusy = errors.New("scheduler: run is held by another worker")

type runRow struct {
	id       uuid.UUID
	job      string
	trigger  Trigger
	asOf     sk.Date
	params   json.RawMessage
	status   string
	attempts int
	corr     *string
}

func (r *Runner) handle(ctx context.Context, m messaging.Message) error {
	var msg JobMessage
	if err := json.Unmarshal([]byte(m.Body), &msg); err != nil || msg.Job == "" {
		r.log.LogAttrs(ctx, slog.LevelWarn, "job.failed", slog.String("message_id", m.ID), observability.ErrWithCode("unparseable_message", err))
		return messaging.Permanent(fmt.Errorf("scheduler: bad message: %w", err))
	}
	spec, ok := r.schedules.Job(msg.Job)
	if !ok {
		return messaging.Permanent(fmt.Errorf("scheduler: unknown job %q", msg.Job))
	}
	sctx := db.WithSystemScope(ctx, "scheduler")
	run, err := r.ensureRun(sctx, spec, msg)
	if err != nil {
		return err
	}
	if run == nil { // 重複（同じ日のスケジュール起動が 2 通・完了済み）
		return nil
	}
	return r.execute(ctx, run)
}

// ensureRun は run を作成（または読み込み）して running にする。何もしなくてよければ nil を返す。
func (r *Runner) ensureRun(ctx context.Context, spec JobSpec, msg JobMessage) (*runRow, error) {
	var run *runRow
	err := r.platform.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		var id uuid.UUID
		if msg.RunID != nil {
			id = *msg.RunID
		} else {
			asOf := r.clock.Today()
			if msg.AsOf != "" {
				d, err := sk.ParseDate(msg.AsOf)
				if err != nil {
					return messaging.Permanent(err)
				}
				asOf = d
			}
			trigger := msg.Trigger
			if trigger == "" {
				trigger = TriggerSchedule
			}
			var dedupe *string
			if trigger == TriggerSchedule {
				dedupe = dedupeKey(spec, asOf, r.clock.Now())
			}
			newID := uuid.Must(uuid.NewV7())
			params := msg.Params
			if len(params) == 0 {
				params = json.RawMessage(`{}`)
			}
			var corr *string
			if msg.CorrelationID != "" {
				corr = &msg.CorrelationID
			}
			err := tx.QueryRow(ctx, `INSERT INTO platform.job_runs (id, job, trigger, dedupe_key, as_of, params, status, correlation_id)
				VALUES ($1, $2, $3, $4, $5, $6, 'queued', $7)
				ON CONFLICT (job, dedupe_key) WHERE dedupe_key IS NOT NULL DO NOTHING RETURNING id`, newID, spec.Name, trigger, dedupe, asOf, params, corr).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				if err := tx.QueryRow(ctx, `SELECT id FROM platform.job_runs WHERE job = $1 AND dedupe_key = $2`, spec.Name, dedupe).Scan(&id); err != nil {
					return fmt.Errorf("scheduler: find deduped run: %w", err)
				}
			} else if err != nil {
				return fmt.Errorf("scheduler: create run: %w", err)
			}
		}
		rw := &runRow{}
		err := tx.QueryRow(ctx, `UPDATE platform.job_runs
			SET status = 'running', attempts = attempts + 1, lease_until = now() + $2::interval, started_at = coalesce(started_at, now()), error = NULL
			WHERE id = $1 AND (status IN ('queued', 'failed') OR (status = 'running' AND lease_until < now()))
			RETURNING id, job, trigger, as_of, params, status, attempts, correlation_id`, id, fmt.Sprintf("%d milliseconds", r.lease.Milliseconds())).
			Scan(&rw.id, &rw.job, &rw.trigger, &rw.asOf, &rw.params, &rw.status, &rw.attempts, &rw.corr)
		if errors.Is(err, pgx.ErrNoRows) {
			var status string
			if err := tx.QueryRow(ctx, `SELECT status FROM platform.job_runs WHERE id = $1`, id).Scan(&status); err != nil {
				return messaging.Permanent(fmt.Errorf("scheduler: run %s not found: %w", id, err))
			}
			if status == "succeeded" {
				return nil
			}
			return errBusy
		}
		if err != nil {
			return fmt.Errorf("scheduler: lock run: %w", err)
		}
		run = rw
		return nil
	})
	return run, err
}

func dedupeKey(spec JobSpec, asOf sk.Date, now time.Time) *string {
	var k string
	switch spec.Dedupe {
	case "daily":
		k = spec.Name + ":" + asOf.String()
	case "hourly":
		k = fmt.Sprintf("%s:%s:%02d", spec.Name, asOf, now.In(sk.JST).Hour())
	default:
		return nil
	}
	return &k
}

func (r *Runner) execute(ctx context.Context, run *runRow) (err error) {
	corr := "job:" + run.job + ":" + run.id.String()
	if run.corr != nil && *run.corr != "" {
		corr = *run.corr
	}
	runID := run.id.String()
	ctx = observability.WithCorrelation(ctx, corr, &runID)
	ctx = observability.WithActor(ctx, sk.Actor{Type: "system", ID: "job:" + run.job})
	ctx = observability.WithFields(ctx, slog.String("job", run.job), slog.String("job_run_id", runID))
	store := &cursorStore{platform: r.platform, job: run.job, runID: run.id}
	resumed := false
	if st, lerr := store.anyCursor(db.WithSystemScope(ctx, "scheduler")); lerr == nil {
		resumed = st
	}
	start := time.Now()
	r.log.LogAttrs(ctx, slog.LevelInfo, "job.started", slog.String("trigger", string(run.trigger)),
		slog.String("as_of", run.asOf.String()), slog.Int("attempts", run.attempts), slog.Bool("cursor_resumed", resumed))

	job, ok := r.jobs[run.job]
	var stats Stats
	if !ok {
		err = fmt.Errorf("scheduler: job %q is not implemented in this process", run.job)
	} else {
		stop := r.heartbeat(ctx, run.id)
		func() {
			defer observability.Recover(ctx, "job", &err)
			stats, err = job.Run(ctx, Run{ID: run.id, Job: run.job, Trigger: run.trigger, AsOf: run.asOf, Params: run.params, Attempt: run.attempts, Cursors: store})
		}()
		stop()
	}
	dur := time.Since(start).Milliseconds()
	sctx := db.WithSystemScope(context.WithoutCancel(ctx), "scheduler")
	if err != nil {
		_ = r.platform.InTx(sctx, func(ctx context.Context, tx db.Tx) error {
			_, e := tx.Exec(ctx, `UPDATE platform.job_runs SET status = 'failed', error = $2, lease_until = NULL, finished_at = now() WHERE id = $1`, run.id, err.Error())
			return e
		})
		r.log.LogAttrs(ctx, slog.LevelError, "job.failed", slog.Int("attempts", run.attempts), slog.Int64("duration_ms", dur), observability.Err(err))
		return err
	}
	statsJSON, _ := json.Marshal(stats)
	if uerr := r.platform.InTx(sctx, func(ctx context.Context, tx db.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE platform.job_runs SET status = 'succeeded', stats = $2, lease_until = NULL, finished_at = now() WHERE id = $1`, run.id, statsJSON)
		return e
	}); uerr != nil {
		return fmt.Errorf("scheduler: record success: %w", uerr)
	}
	r.log.LogAttrs(ctx, slog.LevelInfo, "job.completed", slog.Int("attempts", run.attempts), slog.Int64("duration_ms", dur), slog.Any("stats", stats))
	return nil
}

// heartbeat は実行中にリースを延ばす（別のワーカーが引き継がないように）。
func (r *Runner) heartbeat(ctx context.Context, id uuid.UUID) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(r.lease / 2)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = r.platform.InTx(db.WithSystemScope(context.WithoutCancel(ctx), "scheduler"), func(ctx context.Context, tx db.Tx) error {
					_, e := tx.Exec(ctx, `UPDATE platform.job_runs SET lease_until = now() + $2::interval WHERE id = $1 AND status = 'running'`, id, fmt.Sprintf("%d milliseconds", r.lease.Milliseconds()))
					return e
				})
			}
		}
	}()
	return func() { close(done) }
}
