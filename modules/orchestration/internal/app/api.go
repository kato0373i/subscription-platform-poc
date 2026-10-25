package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/auditctx"
	"subscription-platform/internal/platform/db"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/orchestration/internal/adapter/postgres"
	"subscription-platform/modules/orchestration/internal/adapter/postgres/sqlcgen"
	payments "subscription-platform/modules/payments/contracts"
	subscription "subscription-platform/modules/subscription/contracts"
)

// ---- 支払方法の変更（§4.4） ----

// ChangePaymentResult は PUT /v1/me/subscriptions/{id}/payment-method の応答。
type ChangePaymentResult struct {
	Subscription subscription.SubscriptionView
	Recollection string // requested | none
}

func (s *Service) ChangePaymentMethod(ctx context.Context, cus sk.CustomerAccountID, subID sk.SubscriptionID, pmID sk.PaymentMethodID) (ChangePaymentResult, error) {
	ctx = unscoped(ctx)
	sub, err := s.d.Subscription.Get(ctx, subID)
	if errors.Is(err, subscription.ErrNotFound) || (err == nil && sub.CustomerAccountID != cus) {
		return ChangePaymentResult{}, ErrNotFound
	}
	if err != nil {
		return ChangePaymentResult{}, err
	}
	if sub.Status.Terminal() || sub.Status == subscription.StatusPendingInitial {
		return ChangePaymentResult{}, ErrSubscriptionNotModifiable
	}
	pm, err := s.d.Payments.GetPaymentMethod(ctx, cus, pmID)
	if errors.Is(err, payments.ErrPaymentMethodNotFound) || (err == nil && pm.Status != "active") {
		return ChangePaymentResult{}, ErrPaymentMethodUnavailable
	}
	if err != nil {
		return ChangePaymentResult{}, err
	}
	info, err := s.d.Tenant.GetTenant(ctx, sub.TenantID)
	if err != nil {
		return ChangePaymentResult{}, err
	}
	if err := s.d.Payments.EnsureProviderRef(ctx, payments.EnsureProviderRefCmd{PaymentMethodID: pmID, TenantID: sub.TenantID, TenantShopID: shopID(info)}); err != nil {
		if errors.Is(err, payments.ErrProviderUnavailable) {
			return ChangePaymentResult{}, err
		}
		return ChangePaymentResult{}, ErrPaymentMethodReentry
	}
	v, err := conflictRetry(ctx, func() (subscription.SubscriptionView, error) {
		return s.d.Subscription.ChangePaymentMethod(ctx, subID, cus, pmID)
	})
	switch {
	case errors.Is(err, subscription.ErrVersionConflict):
		return ChangePaymentResult{}, ErrConflict
	case errors.Is(err, subscription.ErrTerminal), errors.Is(err, subscription.ErrStateConflict):
		return ChangePaymentResult{}, ErrSubscriptionNotModifiable
	case errors.Is(err, subscription.ErrNotFound):
		return ChangePaymentResult{}, ErrNotFound
	case err != nil:
		return ChangePaymentResult{}, err
	}
	res := ChangePaymentResult{Subscription: v, Recollection: "none"}
	if v.Status == subscription.StatusPastDue {
		res.Recollection = "requested" // 即時の再決済は payment_method_changed のハンドラ（非同期）
	}
	return res, nil
}

// ---- 支払方法の削除（§4.5） ----

func (s *Service) DeletePaymentMethod(ctx context.Context, cus sk.CustomerAccountID, pmID sk.PaymentMethodID) error {
	ctx = unscoped(ctx)
	pm, err := s.d.Payments.GetPaymentMethod(ctx, cus, pmID)
	if errors.Is(err, payments.ErrPaymentMethodNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if pm.Status == "deleted" {
		return nil
	}
	used, err := s.d.Subscription.IsPaymentMethodInUse(ctx, pmID)
	if err != nil {
		return err
	}
	if used {
		return ErrPaymentMethodInUse
	}
	return s.d.Payments.DeletePaymentMethod(ctx, cus, pmID) // 検査〜削除の間の競合は許容する（C-08）
}

// ---- 退会（§4.8） ----

func (s *Service) Withdraw(ctx context.Context, cus sk.CustomerAccountID) error {
	ctx = unscoped(ctx)
	open, err := s.d.Subscription.HasOpenSubscriptions(ctx, cus)
	if err != nil {
		return err
	}
	if open {
		return ErrHasOpenSubscriptions
	}
	if err := s.d.Customer.Withdraw(ctx, cus); err != nil {
		return err
	}
	// 認証が withdrawn を拒否するので残る窓は小さいが、退会と申込の競合は検知だけする（C-10）
	if again, err := s.d.Subscription.HasOpenSubscriptions(ctx, cus); err == nil && again {
		alert(ctx, "orchestration.withdrawn_with_open_subscription", slog.String("customer_account_id", cus.String()))
	}
	return nil
}

// ---- ops：課金バッチの再実行と照会（§4.9・§7.6・§7.7） ----

// RerunResult は POST /v1/ops/billing-runs/{date}/rerun の応答。
type RerunResult struct {
	BillingRunID uuid.UUID
	RunDate      sk.Date
	Seq          int
}

func (s *Service) Rerun(ctx context.Context, date sk.Date, only *sk.TenantID, operator string) (RerunResult, error) {
	if date.After(s.d.Clock.Today()) {
		return RerunResult{}, ErrInvalidDate
	}
	ctx = system(ctx, "ops_billing_rerun")
	id := uuid.Must(uuid.NewV7())
	var tu *uuid.UUID
	if only != nil {
		u := only.UUID()
		tu = &u
	}
	var br sqlcgen.OrchestrationBillingRun
	err := s.sagaTx(ctx, func(ctx context.Context, q *sqlcgen.Queries) error {
		if err := (&postgres.Repo{Q: q}).InsertBillingRun(ctx, id, date, "rerun", tu, &operator, s.now()); err != nil {
			return err
		}
		var err error
		br, err = q.GetBillingRun(ctx, id)
		return err
	})
	if errors.Is(err, postgres.ErrRunDispatching) {
		return RerunResult{}, ErrBillingRunInProgress
	}
	if err != nil {
		return RerunResult{}, err
	}
	params, _ := json.Marshal(DispatchParams{BillingRunID: &id})
	if _, err := s.d.EnqueueJob(ctx, RenewalDispatchJobName, date, params); err != nil {
		return RerunResult{}, err
	}
	attrs := map[string]any{"run_date": date.String(), "billing_run_id": id.String(), "seq": br.Seq}
	if only != nil {
		attrs["tenant_id"] = only.String()
	}
	auditctx.Annotate(ctx, "billing_run.rerun", auditctx.Target{Type: "billing_run", ID: id.String()}, nil, attrs)
	return RerunResult{BillingRunID: id, RunDate: date, Seq: int(br.Seq)}, nil
}

// BillingRunsView は GET /v1/ops/billing-runs/{date}。
type BillingRunsView struct {
	Runs     []sqlcgen.OrchestrationBillingRun
	Outcomes map[string]int64
}

func (s *Service) BillingRuns(ctx context.Context, date sk.Date) (BillingRunsView, error) {
	out := BillingRunsView{Outcomes: map[string]int64{"paid": 0, "payment_failed": 0, "ended": 0, "in_progress": 0, "failed": 0}}
	err := s.d.DB.ReadTx(system(ctx, "ops_billing_runs"), func(ctx context.Context, tx db.Tx) error {
		q := sqlcgen.New(tx)
		var err error
		if out.Runs, err = q.ListBillingRuns(ctx, date); err != nil {
			return err
		}
		rows, err := q.CountRunOutcomes(ctx, &date)
		for _, r := range rows {
			out.Outcomes[r.Outcome] += r.N
		}
		return err
	})
	return out, err
}

// BulkJobView は GET /v1/ops/bulk-cancellation-jobs/{id}。
type BulkJobView struct {
	Job      sqlcgen.OrchestrationBulkCancellationJob
	Failures []sqlcgen.OrchestrationBulkCancellationFailure
}

func (s *Service) BulkJob(ctx context.Context, id uuid.UUID) (BulkJobView, error) {
	var v BulkJobView
	err := s.d.DB.ReadTx(system(ctx, "ops_bulk_cancellation"), func(ctx context.Context, tx db.Tx) error {
		q := sqlcgen.New(tx)
		var err error
		if v.Job, err = q.GetBulkJob(ctx, id); err != nil {
			return postgres.NotFound(err)
		}
		v.Failures, err = q.ListBulkFailures(ctx, id)
		return err
	})
	if errors.Is(err, postgres.ErrNotFound) {
		return v, ErrNotFound
	}
	return v, err
}

func (s *Service) BulkJobsOf(ctx context.Context, t sk.TenantID) ([]BulkJobView, error) {
	var out []BulkJobView
	err := s.d.DB.ReadTx(system(ctx, "ops_bulk_cancellation"), func(ctx context.Context, tx db.Tx) error {
		q := sqlcgen.New(tx)
		jobs, err := q.ListBulkJobsByTenant(ctx, t.UUID())
		if err != nil {
			return err
		}
		for _, j := range jobs {
			f, err := q.ListBulkFailures(ctx, j.ID)
			if err != nil {
				return err
			}
			out = append(out, BulkJobView{Job: j, Failures: f})
		}
		return nil
	})
	return out, err
}
