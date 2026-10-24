package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/observability"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/billing/contracts"
	"subscription-platform/modules/billing/internal/adapter/postgres"
)

// ---- payments.* の購読（§5） ----

// 購読する payments.* のイベント（Billing は payments/contracts に依存しないので文字列で持つ）。
const (
	EventPaymentSucceeded      = "payments.payment_succeeded"
	EventPaymentFailed         = "payments.payment_failed"
	EventPaymentPending        = "payments.payment_pending"
	EventPaymentOutcomeUnknown = "payments.payment_outcome_unknown"
)

type paymentEvent struct {
	AttemptID sk.PaymentAttemptID `json:"attempt_id"`
	Payable   struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	} `json:"payable"`
	Amount      int64   `json:"amount"`
	FailureCode *string `json:"failure_code"`
	Retryable   *bool   `json:"retryable"`
}

// HandlePaymentEvent は決済の結果を請求に反映する（Inbox と同じトランザクション。§5 の手順 4〜6）。
func (s *Service) HandlePaymentEvent(ctx context.Context, tx db.Tx, env sk.EventEnvelope) error {
	var o c.PaymentOutcome
	switch env.EventType {
	case EventPaymentSucceeded:
		o.Status = c.OutcomeSucceeded
	case EventPaymentFailed:
		o.Status = c.OutcomeFailed
	case EventPaymentPending:
		o.Status = c.OutcomePending
	case EventPaymentOutcomeUnknown:
		o.Status = c.OutcomeUnknown
	default:
		return nil
	}
	p, err := sk.DecodePayload[paymentEvent](env)
	if err != nil {
		return messaging.Permanent(err)
	}
	if p.Payable.Type != "invoice" {
		return nil
	}
	id, err := sk.ParseID[sk.InvoiceTag](p.Payable.ID)
	if err != nil {
		return messaging.Permanent(err)
	}
	amount, err := sk.NewMoney(p.Amount)
	if err != nil {
		return messaging.Permanent(err)
	}
	o.Amount, o.Source = amount, c.SourceEvent
	if o.Status == c.OutcomeFailed {
		f := false // retryable が欠けていたら false とみなす（§5）
		o.Retryable = &f
		if p.Retryable != nil {
			o.Retryable = p.Retryable
		}
		if p.FailureCode != nil {
			o.FailureCode = *p.FailureCode
		}
	}
	_, err = s.RecordOutcomeTx(ctx, tx, id, p.AttemptID, o)
	// 見つからない・楽観ロックの衝突はエラーを返して再配信に任せる（最大 5 回で DLQ。§9-7・§9-11）
	return err
}

// ---- 回収時刻のジョブ（§8） ----

// CollectionDueJobName はジョブ名（deploy/schedules.yaml と同じ）。
const CollectionDueJobName = "billing-collection-due"

// CollectionDueJob は回収時刻が来た請求に回収要求を出す。
type CollectionDueJob struct{ s *Service }

func (s *Service) CollectionDueJob() scheduler.Job { return CollectionDueJob{s: s} }

func (CollectionDueJob) Name() string { return CollectionDueJobName }

type dueCursor struct {
	At time.Time `json:"at"`
	ID uuid.UUID `json:"id"`
}

func (j CollectionDueJob) Run(ctx context.Context, run scheduler.Run) (scheduler.Stats, error) {
	s := j.s
	ctx = observability.WithActor(ctx, sk.Actor{Type: "system", ID: CollectionDueJobName})
	scope := run.ID.String() // 同じ実行の再試行は続きから再開する（P-ARC-11 ⑥）
	state, err := run.Cursors.Load(ctx, scope)
	if err != nil {
		return nil, err
	}
	var emitted, skipped int64
	stats := func() scheduler.Stats { return scheduler.Stats{"emitted": emitted, "skipped": skipped} }
	if state.Completed {
		return stats(), nil
	}
	var cur *dueCursor
	if len(state.Cursor) > 0 {
		cur = &dueCursor{}
		_ = json.Unmarshal(state.Cursor, cur)
	}
	now := s.d.Clock.Now()
	for {
		var page []postgres.DueRef
		err := s.d.DB.ReadTx(system(ctx, CollectionDueJobName), func(ctx context.Context, tx db.Tx) error {
			var afterAt *time.Time
			var afterID *uuid.UUID
			if cur != nil {
				afterAt, afterID = &cur.At, &cur.ID
			}
			var err error
			page, err = postgres.New(tx).ListDue(ctx, now, afterAt, afterID, s.d.DuePageSize)
			return err
		})
		if err != nil {
			return stats(), err
		}
		for _, ref := range page {
			cur = &dueCursor{At: ref.At, ID: ref.ID.UUID()}
			b, _ := json.Marshal(cur)
			done := false
			err := s.d.DB.InTx(db.WithTenantScope(ctx, ref.TenantID), func(ctx context.Context, tx db.Tx) error {
				repo := postgres.New(tx)
				x, err := repo.GetForUpdateSkipLocked(ctx, ref.ID)
				if errors.Is(err, postgres.ErrNotFound) {
					return run.Cursors.Save(ctx, tx, scope, b) // 他で処理中
				}
				if err != nil {
					return err
				}
				if x.EmitDueCollection(s.d.Clock.Now(), s.d.Lease) {
					if err := s.save(ctx, tx, repo, x); err != nil {
						return err
					}
					done = true
				}
				return run.Cursors.Save(ctx, tx, scope, b)
			})
			if err != nil {
				return stats(), err
			}
			if done {
				emitted++
			} else {
				skipped++
			}
		}
		if len(page) < s.d.DuePageSize {
			break
		}
	}
	if err := run.Cursors.Complete(ctx, nil, scope); err != nil {
		return stats(), err
	}
	observability.Logger("billing.collection_due").LogAttrs(ctx, slog.LevelInfo, "billing.collection_due_emitted",
		slog.Int64("emitted", emitted), slog.Int64("skipped", skipped))
	return stats(), nil
}
