package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/observability"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
	billing "subscription-platform/modules/billing/contracts"
	"subscription-platform/modules/orchestration/internal/adapter/postgres"
	"subscription-platform/modules/orchestration/internal/adapter/postgres/sqlcgen"
	"subscription-platform/modules/orchestration/internal/domain"
	subscription "subscription-platform/modules/subscription/contracts"
)

// ジョブ名（Platform の規約 <module>-<name>。spec の renewal-dispatch などを置き換える。P-IMP-17）。
const (
	RenewalDispatchJobName = "orchestration-renewal-dispatch"
	SweeperJobName         = "orchestration-process-sweeper"
	BulkCancellationJob    = "orchestration-bulk-cancellation"
	SagaGCJobName          = "orchestration-checkout-saga-gc"
)

// RenewalQueue は更新処理の投入先（FIFO。C-01）。
const RenewalQueue = "orc-renewal.fifo"

// EventRenewalRequested は orc-renewal.fifo のメッセージの種類（Orchestration の内部の命令。バスには流さない）。
const EventRenewalRequested = "orchestration.renewal_requested"

// RenewalMessage は orc-renewal.fifo の本文の payload。
type RenewalMessage struct {
	SubscriptionID sk.SubscriptionID `json:"subscription_id"`
	TenantID       sk.TenantID       `json:"tenant_id"`
	RunDate        sk.Date           `json:"run_date"`
	BillingRunID   *uuid.UUID        `json:"billing_run_id,omitempty"`
}

// DispatchParams は renewal-dispatch の params（rerun は billing_run_id を渡す）。
type DispatchParams struct {
	BillingRunID *uuid.UUID `json:"billing_run_id,omitempty"`
}

func (s *Service) renewalMessage(ctx context.Context, m RenewalMessage, dedup string) (messaging.FIFOMessage, error) {
	env, err := sk.NewEnvelope(EventRenewalRequested, "subscription", m.SubscriptionID.UUID(), 1, &m.TenantID, s.now(), m)
	if err != nil {
		return messaging.FIFOMessage{}, err
	}
	env.CorrelationID = correlation(ctx)
	return messaging.FIFOMessage{Env: env, GroupID: m.SubscriptionID.String(), DedupID: dedup}, nil
}

// ---- renewal-dispatch（§4.2） ----

type dispatchJob struct{ s *Service }

func (s *Service) RenewalDispatchJob() scheduler.Job { return dispatchJob{s: s} }

func (dispatchJob) Name() string { return RenewalDispatchJobName }

func (j dispatchJob) Run(ctx context.Context, run scheduler.Run) (scheduler.Stats, error) {
	s := j.s
	ctx = system(observability.WithActor(ctx, sk.Actor{Type: "system", ID: RenewalDispatchJobName}), RenewalDispatchJobName)
	date := run.AsOf
	if date.IsZero() {
		date = s.d.Clock.Today()
	}
	var params DispatchParams
	if len(run.Params) > 0 {
		_ = json.Unmarshal(run.Params, &params)
	}
	br, resumed, err := s.dispatchRun(ctx, date, params.BillingRunID)
	if err != nil {
		return nil, err
	}
	stats := func() scheduler.Stats {
		return scheduler.Stats{"billing_run_id": br.ID.String(), "run_date": date.String(), "seq": int64(br.Seq), "enqueued": int64(br.EnqueuedCount), "resumed": resumed}
	}
	if br.Status != "dispatching" {
		return stats(), nil
	}
	if err := s.dispatch(ctx, &br); err != nil {
		if run.Attempt >= 3 {
			_ = s.sagaTx(ctx, func(ctx context.Context, q *sqlcgen.Queries) error {
				return q.FinishRun(ctx, sqlcgen.FinishRunParams{ID: br.ID, Status: "failed", FinishedAt: ptrNow(s)})
			})
			alert(ctx, "orchestration.billing_run_failed", slog.String("billing_run_id", br.ID.String()), observability.Err(err))
		}
		return stats(), err
	}
	return stats(), nil
}

func ptrNow(s *Service) *time.Time { t := s.now(); return &t }

// dispatchRun は投入する BillingRun を決める：指定（rerun）→ 同じ課金日の dispatching（途中で落ちた前回。続きから）→ 新しく作る。
func (s *Service) dispatchRun(ctx context.Context, date sk.Date, id *uuid.UUID) (sqlcgen.OrchestrationBillingRun, bool, error) {
	var br sqlcgen.OrchestrationBillingRun
	resumed := false
	err := s.sagaTx(ctx, func(ctx context.Context, q *sqlcgen.Queries) error {
		var err error
		if id != nil {
			br, err = q.GetBillingRun(ctx, *id)
			if err == nil && br.Status == "dispatching" && br.Cursor != nil {
				resumed = true
				err = q.IncrementRunResume(ctx, br.ID)
			}
			return postgres.NotFound(err)
		}
		br, err = q.GetDispatchingRun(ctx, date)
		if err == nil {
			resumed = true
			return q.IncrementRunResume(ctx, br.ID)
		}
		if !errors.Is(postgres.NotFound(err), postgres.ErrNotFound) {
			return err
		}
		newID := uuid.Must(uuid.NewV7())
		if err := (&postgres.Repo{Q: q}).InsertBillingRun(ctx, newID, date, "scheduled", nil, nil, s.now()); err != nil {
			return err
		}
		br, err = q.GetBillingRun(ctx, newID)
		return err
	})
	return br, resumed, err
}

// dispatch はカーソルから終端まで、500 件ずつ「取得 → 投入 → cursor・enqueued_count の保存」を繰り返す。
func (s *Service) dispatch(ctx context.Context, br *sqlcgen.OrchestrationBillingRun) error {
	cursor := ""
	if br.Cursor != nil {
		cursor = *br.Cursor
	}
	count := int(br.EnqueuedCount)
	for {
		page, err := s.d.Subscription.ListDueForRenewal(ctx, br.RunDate, cursor, s.d.DispatchPageSize)
		if err != nil {
			return err
		}
		msgs := make([]messaging.FIFOMessage, 0, len(page.Items))
		for _, ref := range page.Items {
			if br.OnlyTenantID != nil && ref.TenantID.UUID() != *br.OnlyTenantID {
				continue
			}
			id := br.ID
			m, err := s.renewalMessage(ctx, RenewalMessage{SubscriptionID: ref.SubscriptionID, TenantID: ref.TenantID, RunDate: br.RunDate, BillingRunID: &id},
				fmt.Sprintf("%s:%s:%d", ref.SubscriptionID, br.RunDate, br.Seq))
			if err != nil {
				return err
			}
			msgs = append(msgs, m)
		}
		if len(msgs) > 0 {
			if err := s.d.SendRenewal(ctx, msgs); err != nil {
				return err
			}
		}
		count += len(msgs)
		if h := s.d.Hooks.AfterDispatchChunk; h != nil {
			if err := h(ctx, count); err != nil {
				return err
			}
		}
		next := page.NextCursor
		err = s.sagaTx(ctx, func(ctx context.Context, q *sqlcgen.Queries) error {
			var c *string
			if next != "" {
				c = &next
			}
			if err := q.SaveRunCursor(ctx, sqlcgen.SaveRunCursorParams{ID: br.ID, Cursor: c, EnqueuedCount: int32(count)}); err != nil { //nolint:gosec // 件数
				return err
			}
			if next == "" {
				return q.FinishRun(ctx, sqlcgen.FinishRunParams{ID: br.ID, Status: "dispatched", FinishedAt: ptrNow(s)})
			}
			return nil
		})
		if err != nil {
			return err
		}
		br.EnqueuedCount = int32(count) //nolint:gosec // 件数
		if next == "" {
			br.Status = "dispatched"
			return nil
		}
		cursor = next
	}
}

// ---- 更新処理（orc-renewal.fifo。§4.2 の開始・条件確定・請求） ----

// HandleRenewal は orc-renewal.fifo のメッセージ。
func (s *Service) HandleRenewal(ctx context.Context, env sk.EventEnvelope) error {
	m, err := sk.DecodePayload[RenewalMessage](env)
	if err != nil {
		return messaging.Permanent(err)
	}
	ctx = unscoped(observability.WithActor(ctx, sk.Actor{Type: "system", ID: "orchestration.renewal"}))
	date := m.RunDate
	p := &postgres.Process{TenantID: m.TenantID, SubscriptionID: m.SubscriptionID, Kind: domain.KindRenewal, State: domain.StateStarted, RunDate: &date, BillingRunID: m.BillingRunID, CorrelationID: correlation(ctx)}
	existing, err := s.insertProcess(ctx, p)
	switch {
	case errors.Is(err, errActiveProcessOnSubscription) && existing != nil:
		if existing.Kind != domain.KindRenewal || existing.RunDate == nil || !existing.RunDate.Equal(date) {
			return messaging.RetryAfter(err, retryDelay)
		}
		p = existing // 同じ課金日の更新を再開する
	case err != nil:
		return err
	}
	return s.advanceRenewal(ctx, p)
}

// advanceRenewal は started → renewal_begun → invoice_issued と進める（各段階は冪等）。
func (s *Service) advanceRenewal(ctx context.Context, p *postgres.Process) error {
	if p.State != domain.StateStarted && p.State != domain.StateRenewalBegun {
		return nil // invoice_issued 以降は collection_due が進める
	}
	out, err := conflictRetry(ctx, func() (subscription.RenewalOutcome, error) {
		return s.d.Subscription.BeginRenewal(ctx, p.SubscriptionID, *p.RunDate)
	})
	switch {
	case errors.Is(err, subscription.ErrNotDue), errors.Is(err, subscription.ErrNotRenewable), invalidTransition(err), errors.Is(err, subscription.ErrNotFound):
		warn(ctx, "orchestration.renewal_skipped", err, slog.String("subscription_id", p.SubscriptionID.String()))
		return s.complete(ctx, p, domain.OutcomeSkipped)
	case err != nil:
		return err
	}
	if out.Kind == subscription.RenewalEnded {
		return s.complete(ctx, p, domain.OutcomeEnded)
	}
	if p.State == domain.StateStarted {
		p.State = domain.StateRenewalBegun
		if err := s.save(ctx, p); err != nil {
			return err
		}
	}
	t := out.Terms
	q, err := s.d.Catalog.QuotePrice(ctx, p.TenantID, t.PriceVersionID, nil)
	if err != nil {
		return err
	}
	inv, err := s.d.Billing.IssueInvoice(ctx, billing.IssueInvoiceCmd{
		TenantID: p.TenantID, SubscriptionID: p.SubscriptionID, CustomerAccountID: out.Subscription.CustomerAccountID, Period: t.Period,
		PlanLine: billing.PlanLine{PriceVersionID: t.PriceVersionID, Amount: q.Amount, TaxRate: q.TaxRate},
		Discount: appliedDiscount(t.Discount), Kind: billing.KindRenewal, CollectionMode: billing.ModeScheduled,
	})
	if err != nil {
		return err
	}
	if p.State.Terminal() {
		return nil
	}
	id := inv.ID
	p.InvoiceID, p.State = &id, domain.StateInvoiceIssued
	err = s.save(ctx, p)
	if errors.Is(err, postgres.ErrVersionConflict) {
		return nil // 0 円の更新などで invoice_paid が先に閉じた
	}
	return err
}
