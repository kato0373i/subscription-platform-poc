// Package outbox は Transactional Outbox の書き込み（Writer）と、EventBridge への中継（Relay）を受け持つ（platform.md §3.2）。
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/observability"
	sk "subscription-platform/internal/sharedkernel"
)

// Writer は呼び出し元のトランザクションで <schema>.outbox_events に書く。
type Writer struct {
	schema string
	clock  sk.Clock
	relay  *Relay
}

// WriterOption は Writer の設定。
type WriterOption func(*Writer)

// WithKick は同じプロセスの Relay をコミット後に起こす（任意。遅延を縮めるだけ）。
func WithKick(r *Relay) WriterOption { return func(w *Writer) { w.relay = r } }

func NewWriter(schema string, clock sk.Clock, opts ...WriterOption) *Writer {
	w := &Writer{schema: schema, clock: clock}
	for _, o := range opts {
		o(w)
	}
	return w
}

// Append はイベントを書く。event_id・occurred_at・correlation_id・causation_id・actor が空なら context と時計から埋める（M3: 集約の保存と同じ InTx で呼ぶ）。
func (w *Writer) Append(ctx context.Context, tx db.Tx, events ...sk.EventEnvelope) error {
	corr, cause := observability.CorrelationFrom(ctx)
	if corr == "" {
		corr = observability.NewCorrelationID()
	}
	actor := observability.ActorFrom(ctx)
	for _, e := range events {
		if e.EventID.IsZero() {
			e.EventID = sk.NewID[sk.EventTag]()
		}
		if e.OccurredAt.IsZero() {
			e.OccurredAt = w.clock.Now()
		}
		if e.CorrelationID == "" {
			e.CorrelationID = corr
		}
		if e.CausationID == nil {
			e.CausationID = cause
		}
		if e.Actor == nil {
			e.Actor = actor
		}
		var actorJSON []byte
		if e.Actor != nil {
			actorJSON, _ = json.Marshal(e.Actor)
		}
		var tenant *uuid.UUID
		if e.TenantID != nil {
			u := e.TenantID.UUID()
			tenant = &u
		}
		payload := e.Payload
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		_, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s.outbox_events
			(id, event_type, tenant_id, aggregate_type, aggregate_id, aggregate_version, correlation_id, causation_id, actor, payload, occurred_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, w.schema),
			e.EventID.UUID(), e.EventType, tenant, e.AggregateType, e.AggregateID, e.AggregateVersion, e.CorrelationID, e.CausationID, actorJSON, payload, e.OccurredAt.UTC())
		if err != nil {
			return fmt.Errorf("outbox: append %s: %w", e.EventType, err)
		}
	}
	if w.relay != nil {
		db.AfterCommit(ctx, func(context.Context) { w.relay.Kick() })
	}
	return nil
}

// RelayConfig は中継の設定。
type RelayConfig struct {
	BatchSize    int           // 1 回の SELECT 件数（既定 100）
	Idle         time.Duration // 未送信がないときの待ち（既定 500ms）
	MaxBackoff   time.Duration // 再送の間隔の上限（既定 5 分）
	AlertAttempt int           // この回数を超えて失敗したら error ログ（既定 10）
}

// Relay は worker-billing の常駐ループ。複数タスクで動かしても SKIP LOCKED で重複しない。
type Relay struct {
	db      *db.ModuleDB
	pub     messaging.Publisher
	schemas []string
	cfg     RelayConfig
	kick    chan struct{}
	log     *slog.Logger
}

func NewRelay(pool *db.Pool, pub messaging.Publisher, schemas []string, cfg RelayConfig) *Relay {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.Idle <= 0 {
		cfg.Idle = 500 * time.Millisecond
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 5 * time.Minute
	}
	if cfg.AlertAttempt <= 0 {
		cfg.AlertAttempt = 10
	}
	return &Relay{db: pool.Relay(), pub: pub, schemas: schemas, cfg: cfg, kick: make(chan struct{}, 1), log: observability.Logger("platform.outbox")}
}

// Kick は待ちを打ち切って次の周回に入る。
func (r *Relay) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Run は ctx が取り消されるまで中継する。
func (r *Relay) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		moved := 0
		for _, s := range r.schemas {
			n, err := r.FlushSchema(ctx, s)
			if err != nil && ctx.Err() == nil {
				r.log.LogAttrs(ctx, slog.LevelWarn, "outbox.flush_failed", slog.String("schema", s), observability.Err(err))
			}
			moved += n
		}
		if moved > 0 {
			continue
		}
		t := time.NewTimer(r.cfg.Idle)
		select {
		case <-ctx.Done():
		case <-r.kick:
		case <-t.C:
		}
		t.Stop()
	}
	return nil
}

// FlushOnce は全スキーマを 1 周する（テストと手動実行用）。処理した件数を返す。
func (r *Relay) FlushOnce(ctx context.Context) (int, error) {
	total := 0
	for _, s := range r.schemas {
		n, err := r.FlushSchema(ctx, s)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

type row struct {
	env      sk.EventEnvelope
	attempts int
}

// FlushSchema は 1 スキーマの未送信を最大 BatchSize 件取り出して送り、結果を同じトランザクションで確定する。
func (r *Relay) FlushSchema(ctx context.Context, schema string) (int, error) {
	var n int
	err := r.db.InTx(ctx, func(ctx context.Context, tx db.Tx) (err error) {
		defer observability.Recover(ctx, "outbox", &err)
		rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT id, event_type, tenant_id, aggregate_type, aggregate_id, aggregate_version, correlation_id, causation_id, actor, payload, occurred_at, attempts
			FROM %s.outbox_events WHERE published_at IS NULL AND next_attempt_at <= now()
			ORDER BY occurred_at, id LIMIT $1 FOR UPDATE SKIP LOCKED`, schema), r.cfg.BatchSize)
		if err != nil {
			return fmt.Errorf("outbox: select: %w", err)
		}
		var batch []row
		for rows.Next() {
			var rw row
			var id uuid.UUID
			var tenant *uuid.UUID
			var actor []byte
			if err := rows.Scan(&id, &rw.env.EventType, &tenant, &rw.env.AggregateType, &rw.env.AggregateID, &rw.env.AggregateVersion,
				&rw.env.CorrelationID, &rw.env.CausationID, &actor, &rw.env.Payload, &rw.env.OccurredAt, &rw.attempts); err != nil {
				rows.Close()
				return fmt.Errorf("outbox: scan: %w", err)
			}
			rw.env.EventID = sk.IDFromUUID[sk.EventTag](id)
			if tenant != nil {
				t := sk.IDFromUUID[sk.TenantTag](*tenant)
				rw.env.TenantID = &t
			}
			if len(actor) > 0 {
				var a sk.Actor
				if json.Unmarshal(actor, &a) == nil {
					rw.env.Actor = &a
				}
			}
			rw.env.OccurredAt = rw.env.OccurredAt.UTC()
			batch = append(batch, rw)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("outbox: rows: %w", err)
		}
		n = len(batch)
		for start := 0; start < len(batch); start += 10 {
			if err := r.publishChunk(ctx, tx, schema, batch[start:min(start+10, len(batch))]); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}

func (r *Relay) publishChunk(ctx context.Context, tx db.Tx, schema string, chunk []row) error {
	envs := make([]sk.EventEnvelope, len(chunk))
	for i, c := range chunk {
		envs[i] = c.env
	}
	results, err := r.pub.Publish(ctx, envs)
	if err != nil {
		results = make([]messaging.PublishResult, len(chunk))
		for i := range chunk {
			results[i] = messaging.PublishResult{EventID: chunk[i].env.EventID, Err: err}
		}
	}
	for i, c := range chunk {
		ectx := observability.WithCorrelation(ctx, c.env.CorrelationID, c.env.CausationID)
		attrs := []slog.Attr{
			slog.String("event_id", c.env.EventID.String()), slog.String("event_type", c.env.EventType),
			slog.String("aggregate_type", c.env.AggregateType), slog.String("aggregate_id", c.env.AggregateID.String()),
			slog.Int("attempts", c.attempts+1),
		}
		if c.env.TenantID != nil {
			attrs = append(attrs, slog.String("tenant_id", c.env.TenantID.String()))
		}
		var perr error
		if i < len(results) {
			perr = results[i].Err
		}
		if perr == nil {
			if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s.outbox_events SET published_at = now(), attempts = attempts + 1, last_error = NULL WHERE id = $1`, schema), c.env.EventID.UUID()); err != nil {
				return fmt.Errorf("outbox: mark published: %w", err)
			}
			r.log.LogAttrs(ectx, slog.LevelInfo, "event.published", attrs...)
			continue
		}
		backoff := min(time.Duration(1<<min(c.attempts, 20))*time.Second, r.cfg.MaxBackoff)
		backoff += time.Duration(rand.Int64N(int64(backoff/4) + 1)) //nolint:gosec // ジッターに暗号論的な乱数は要らない
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s.outbox_events SET attempts = attempts + 1, next_attempt_at = now() + $2::interval, last_error = $3 WHERE id = $1`, schema),
			c.env.EventID.UUID(), fmt.Sprintf("%d milliseconds", backoff.Milliseconds()), truncate(perr.Error(), 1000)); err != nil {
			return fmt.Errorf("outbox: mark failed: %w", err)
		}
		level := slog.LevelWarn
		if c.attempts+1 >= r.cfg.AlertAttempt {
			level = slog.LevelError
		}
		r.log.LogAttrs(ectx, level, "event.failed", append(attrs, observability.Err(perr))...)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
