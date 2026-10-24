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
	"subscription-platform/internal/platform/observability"
	sk "subscription-platform/internal/sharedkernel"
)

// Sender は scheduler キューに本文を送る。
type Sender func(ctx context.Context, queue, body string) error

// Service はジョブの投入と照会（dev のジョブ手動実行・ops の再実行）。API プロセスの中では実行しない。
type Service struct {
	platform  *db.ModuleDB
	schedules Schedules
	clock     sk.Clock
	send      Sender
}

func NewService(pool *db.Pool, s Schedules, clock sk.Clock, send Sender) *Service {
	return &Service{platform: pool.Platform(), schedules: s, clock: clock, send: send}
}

// ErrUnknownJob は schedules.yaml にないジョブ。
var ErrUnknownJob = errors.New("scheduler: unknown job")

// ErrRunNotFound は job_runs にない run。
var ErrRunNotFound = errors.New("scheduler: run not found")

// EnqueueOptions は投入の設定。
type EnqueueOptions struct {
	Trigger Trigger
	AsOf    *sk.Date
	Params  json.RawMessage
	Force   bool // true なら重複排除しない
}

// EnqueueResult は投入の結果。
type EnqueueResult struct {
	RunID        uuid.UUID
	Deduplicated bool
}

// Enqueue は job_runs を作り、ジョブを持つプロセスの scheduler キューに投入する。
// Force=false なら dedupe_key で重複排除し、既存の run を Deduplicated=true で返す。
func (s *Service) Enqueue(ctx context.Context, job string, o EnqueueOptions) (EnqueueResult, error) {
	spec, ok := s.schedules.Job(job)
	if !ok {
		return EnqueueResult{}, fmt.Errorf("%w: %s", ErrUnknownJob, job)
	}
	asOf := s.clock.Today()
	if o.AsOf != nil {
		asOf = *o.AsOf
	}
	if o.Trigger == "" {
		o.Trigger = TriggerDev
	}
	params := o.Params
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	var dedupe *string
	if !o.Force {
		dedupe = dedupeKey(spec, asOf, s.clock.Now())
	}
	corr, _ := observability.CorrelationFrom(ctx)
	var corrPtr *string
	if corr != "" {
		corrPtr = &corr
	}
	res := EnqueueResult{RunID: uuid.Must(uuid.NewV7())}
	resend := true
	err := s.platform.InTx(db.WithSystemScope(ctx, "scheduler"), func(ctx context.Context, tx db.Tx) error {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `INSERT INTO platform.job_runs (id, job, trigger, dedupe_key, as_of, params, status, correlation_id)
			VALUES ($1, $2, $3, $4, $5, $6, 'queued', $7)
			ON CONFLICT (job, dedupe_key) WHERE dedupe_key IS NOT NULL DO NOTHING RETURNING id`, res.RunID, job, o.Trigger, dedupe, asOf, params, corrPtr).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			var status string
			if err := tx.QueryRow(ctx, `SELECT id, status FROM platform.job_runs WHERE job = $1 AND dedupe_key = $2`, job, dedupe).Scan(&res.RunID, &status); err != nil {
				return fmt.Errorf("scheduler: find deduped run: %w", err)
			}
			res.Deduplicated = true
			resend = status == "failed" // 失敗したまま止まっている run は再投入して再試行させる
			return nil
		}
		if err != nil {
			return fmt.Errorf("scheduler: create run: %w", err)
		}
		return nil
	})
	if err != nil {
		return EnqueueResult{}, err
	}
	if resend {
		body, _ := json.Marshal(JobMessage{Job: job, Trigger: o.Trigger, RunID: &res.RunID, CorrelationID: corr})
		if err := s.send(ctx, SchedulerQueue(spec.Process), string(body)); err != nil {
			return EnqueueResult{}, err
		}
	}
	observability.Logger("platform.job").LogAttrs(ctx, slog.LevelInfo, "job.enqueued", slog.String("job", job), slog.String("job_run_id", res.RunID.String()),
		slog.String("trigger", string(o.Trigger)), slog.String("as_of", asOf.String()), slog.Bool("deduplicated", res.Deduplicated))
	return res, nil
}

// RunView は job_runs の 1 行。
type RunView struct {
	ID         uuid.UUID       `json:"run_id"`
	Job        string          `json:"job"`
	Trigger    Trigger         `json:"trigger"`
	Status     string          `json:"status"`
	AsOf       sk.Date         `json:"as_of"`
	Attempts   int             `json:"attempts"`
	Stats      json.RawMessage `json:"stats"`
	Error      *string         `json:"error"`
	QueuedAt   time.Time       `json:"queued_at"`
	FinishedAt *time.Time      `json:"finished_at"`
}

// Done は終わった（succeeded か failed）か。
func (v RunView) Done() bool { return v.Status == "succeeded" || v.Status == "failed" }

// Get は run を読む。
func (s *Service) Get(ctx context.Context, runID uuid.UUID) (RunView, error) {
	var v RunView
	err := s.platform.ReadTx(db.WithSystemScope(ctx, "scheduler"), func(ctx context.Context, tx db.Tx) error {
		err := tx.QueryRow(ctx, `SELECT id, job, trigger, status, as_of, attempts, stats, error, queued_at, finished_at FROM platform.job_runs WHERE id = $1`, runID).
			Scan(&v.ID, &v.Job, &v.Trigger, &v.Status, &v.AsOf, &v.Attempts, &v.Stats, &v.Error, &v.QueuedAt, &v.FinishedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRunNotFound
		}
		return err
	})
	return v, err //nolint:wrapcheck
}

// Wait は run が終わるか timeout まで待つ。終わっていなければ最後の状態を返す。
// failed でも、再配信で再試行される途中（attempts < maxAttempts）は待ち続ける。
func (s *Service) Wait(ctx context.Context, runID uuid.UUID, timeout time.Duration) (RunView, error) {
	deadline := time.Now().Add(timeout)
	for {
		v, err := s.Get(ctx, runID)
		if err != nil {
			return v, err
		}
		if v.Status == "succeeded" || (v.Status == "failed" && v.Attempts >= 3) || time.Now().After(deadline) {
			return v, nil
		}
		select {
		case <-ctx.Done():
			return v, ctx.Err() //nolint:wrapcheck
		case <-time.After(200 * time.Millisecond):
		}
	}
}
