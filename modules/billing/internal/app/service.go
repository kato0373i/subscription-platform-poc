// Package app は billing モジュールのユースケース（contracts.Service・me / console / ops の API・結果の購読・回収時刻のジョブ）。
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"subscription-platform/internal/platform/auditctx"
	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/observability"
	"subscription-platform/internal/platform/outbox"
	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/billing/contracts"
	"subscription-platform/modules/billing/internal/adapter/postgres"
	"subscription-platform/modules/billing/internal/domain"
)

// Deps はユースケースの依存。
type Deps struct {
	DB     *db.ModuleDB
	Outbox *outbox.Writer
	Clock  sk.Clock
	// Lease は回収要求のリース（BILLING_COLLECTION_LEASE。既定 6 時間。§8）。
	Lease time.Duration
	// DuePageSize は回収時刻ジョブの 1 ページの件数（既定 500）。
	DuePageSize int
}

// Service はユースケースの入口。
type Service struct{ d Deps }

func New(d Deps) *Service {
	if d.Lease <= 0 {
		d.Lease = 6 * time.Hour
	}
	if d.DuePageSize <= 0 {
		d.DuePageSize = 500
	}
	return &Service{d: d}
}

var _ c.Service = (*Service)(nil)

const maxAttempts = 3 // 楽観ロックの再試行（§9-11）

func mapNotFound(err error) error {
	if errors.Is(err, postgres.ErrNotFound) {
		return c.ErrInvoiceNotFound
	}
	return err
}

// system は BYPASSRLS のスコープ（Orchestration は ID だけを持つ。ジョブの走査・ops の API。§6）。
func system(ctx context.Context, reason string) context.Context {
	return db.WithSystemScope(ctx, reason)
}

// invoiceTenant は請求の事業者を引く。
func (s *Service) invoiceTenant(ctx context.Context, id sk.InvoiceID) (sk.TenantID, error) {
	var t sk.TenantID
	err := s.d.DB.ReadTx(system(ctx, "billing_lookup"), func(ctx context.Context, tx db.Tx) error {
		var err error
		t, err = postgres.New(tx).InvoiceTenant(ctx, id)
		return mapNotFound(err)
	})
	return t, err
}

// appendEvents は記録したイベントを Outbox に書く。aggregate_version は保存後の version。
func (s *Service) appendEvents(ctx context.Context, tx db.Tx, x *domain.Invoice, events []domain.Event) error {
	st := x.State()
	for _, e := range events {
		env, err := sk.NewEnvelope(e.Type, "invoice", st.ID.UUID(), int64(st.Version), &st.TenantID, s.d.Clock.Now(), e.Payload)
		if err != nil {
			return err
		}
		if err := s.d.Outbox.Append(ctx, tx, env); err != nil {
			return fmt.Errorf("billing: outbox: %w", err)
		}
	}
	return nil
}

// save は変化した請求を保存してイベントを書く（同じトランザクション）。
func (s *Service) save(ctx context.Context, tx db.Tx, repo *postgres.Repo, x *domain.Invoice) error {
	events := x.Events()
	if err := repo.Update(ctx, x, s.d.Clock.Now()); err != nil {
		return err
	}
	x.MarkSaved()
	return s.appendEvents(ctx, tx, x, events)
}

// retry は楽観ロックの衝突を読み直して再実行する（最大 3 回）。
func retry(fn func() error) error {
	for attempt := 1; ; attempt++ {
		err := fn()
		if errors.Is(err, postgres.ErrVersionConflict) && attempt < maxAttempts {
			continue
		}
		return err
	}
}

// ---- IssueInvoice ----

func (s *Service) IssueInvoice(ctx context.Context, cmd c.IssueInvoiceCmd) (c.InvoiceView, error) {
	if cmd.Discount != nil && cmd.Discount.Type != sk.DiscountAmount && cmd.Discount.Type != sk.DiscountPercent {
		return c.InvoiceView{}, fmt.Errorf("%w: discount type %q", c.ErrValidation, cmd.Discount.Type)
	}
	now := s.d.Clock.Now()
	x, err := domain.NewInvoice(sk.NewID[sk.InvoiceTag](), cmd, now, s.d.Lease)
	if err != nil {
		return c.InvoiceView{}, err
	}
	ctx = db.WithTenantScope(ctx, cmd.TenantID)
	var out *domain.Invoice
	for attempt := 1; ; attempt++ {
		err = s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
			repo := postgres.New(tx)
			existing, err := repo.GetByPeriod(ctx, cmd.SubscriptionID, cmd.Period.Start)
			switch {
			case err == nil:
				if !existing.SameRequest(cmd) {
					observability.Logger("billing.issue").LogAttrs(ctx, slog.LevelError, "billing.idempotency_conflict",
						slog.String("invoice_id", existing.State().ID.String()), slog.String("subscription_id", cmd.SubscriptionID.String()))
					return c.ErrIdempotencyConflict
				}
				out = existing
				return nil
			case !errors.Is(err, postgres.ErrNotFound):
				return err
			}
			if err := repo.Insert(ctx, x); err != nil {
				return err
			}
			out = x
			return s.appendEvents(ctx, tx, x, x.Events())
		})
		// 同時実行で同じ期間の請求が先に作られたら読み直す（§9-1）
		if errors.Is(err, postgres.ErrPeriodExists) && attempt < maxAttempts {
			continue
		}
		break
	}
	switch {
	case errors.Is(err, postgres.ErrUnsettledExists):
		return c.InvoiceView{}, c.ErrUnsettledInvoiceExists
	case err != nil:
		return c.InvoiceView{}, err
	}
	return out.View(), nil
}

// ---- RecordPaymentOutcome ----

func (s *Service) RecordPaymentOutcome(ctx context.Context, invoiceID sk.InvoiceID, attemptID sk.PaymentAttemptID, o c.PaymentOutcome) (c.InvoiceView, error) {
	if o.Status == c.OutcomeFailed && o.Retryable == nil {
		return c.InvoiceView{}, fmt.Errorf("%w: retryable is required for failed", c.ErrValidation)
	}
	tenant, err := s.invoiceTenant(ctx, invoiceID)
	if err != nil {
		return c.InvoiceView{}, err
	}
	var view c.InvoiceView
	err = retry(func() error {
		return s.d.DB.InTx(db.WithTenantScope(ctx, tenant), func(ctx context.Context, tx db.Tx) error {
			var err error
			view, err = s.RecordOutcomeTx(ctx, tx, invoiceID, attemptID, o)
			return err
		})
	})
	return view, err
}

// RecordOutcomeTx は結果を反映する（呼び出し元のトランザクション。購読者は Inbox と同じトランザクションで使う。§5）。
// (attempt_id, outcome) の記録が既にあれば何もせず現在の請求を返す。
func (s *Service) RecordOutcomeTx(ctx context.Context, tx db.Tx, invoiceID sk.InvoiceID, attemptID sk.PaymentAttemptID, o c.PaymentOutcome) (c.InvoiceView, error) {
	repo := postgres.New(tx)
	x, err := repo.GetForUpdate(ctx, invoiceID)
	if err != nil {
		return c.InvoiceView{}, mapNotFound(err)
	}
	st := x.State()
	finalized, err := repo.AttemptFinalized(ctx, attemptID)
	if err != nil {
		return c.InvoiceView{}, err
	}
	policy, err := repo.GetPolicy(ctx, st.TenantID)
	if err != nil {
		return c.InvoiceView{}, err
	}
	retryable := o.Retryable != nil && *o.Retryable
	now := s.d.Clock.Now()
	applied, anomaly := x.RecordOutcome(domain.Outcome{
		AttemptID: attemptID, Status: o.Status, FailureCode: o.FailureCode, Retryable: retryable, AttemptFinalized: finalized,
	}, policy, s.d.Clock.Today(), now)
	if anomaly == "" && o.Amount.Int64() != st.Total.Int64() {
		anomaly = "amount_mismatch" // 金額が違っても反映する（succeeded を捨てると二重決済の危険がある。§5）
	}
	rec := postgres.Record{
		AttemptID: attemptID, Outcome: o.Status, InvoiceID: invoiceID, TenantID: st.TenantID, Amount: o.Amount.Int64(),
		Source: o.Source, Applied: applied, Anomaly: anomaly, RecordedAt: now,
	}
	if o.Status == c.OutcomeFailed {
		code := o.FailureCode
		rec.FailureCode, rec.Retryable = &code, &retryable
	}
	fresh, err := repo.InsertRecord(ctx, rec)
	if err != nil {
		return c.InvoiceView{}, err
	}
	if !fresh {
		return repoView(ctx, repo, invoiceID) // 同じ attempt_id・同じ結果の再送（=）
	}
	if anomaly != "" {
		observability.Logger("billing.outcome").LogAttrs(ctx, slog.LevelError, "billing.payment_anomaly",
			slog.String("anomaly", anomaly), slog.String("invoice_id", invoiceID.String()), slog.String("attempt_id", attemptID.String()),
			slog.String("outcome", string(o.Status)), slog.String("invoice_status", string(x.State().Status)))
	}
	if !applied {
		return x.View(), nil
	}
	if err := s.save(ctx, tx, repo, x); err != nil {
		return c.InvoiceView{}, err
	}
	return x.View(), nil
}

func repoView(ctx context.Context, repo *postgres.Repo, id sk.InvoiceID) (c.InvoiceView, error) {
	x, err := repo.Get(ctx, id)
	if err != nil {
		return c.InvoiceView{}, mapNotFound(err)
	}
	return x.View(), nil
}

// ---- RequestImmediateCollection ----

func (s *Service) RequestImmediateCollection(ctx context.Context, invoiceID sk.InvoiceID, req c.CollectionRequest) (c.InvoiceView, error) {
	if req.Reason != c.ReasonImmediate && req.Reason != c.ReasonOps {
		return c.InvoiceView{}, fmt.Errorf("%w: reason %q", c.ErrValidation, req.Reason)
	}
	tenant, err := s.invoiceTenant(ctx, invoiceID)
	if err != nil {
		return c.InvoiceView{}, err
	}
	var view c.InvoiceView
	err = retry(func() error {
		return s.d.DB.InTx(db.WithTenantScope(ctx, tenant), func(ctx context.Context, tx db.Tx) error {
			repo := postgres.New(tx)
			x, err := repo.GetForUpdate(ctx, invoiceID)
			if err != nil {
				return mapNotFound(err)
			}
			seq := x.State().CollectionSeq
			if err := x.RequestCollection(req.Reason, req.RequestedBy, s.d.Clock.Now(), s.d.Lease); err != nil {
				return err
			}
			view = x.View()
			if x.State().CollectionSeq == seq {
				return nil // リース中は既存の要求を返す
			}
			if err := s.save(ctx, tx, repo, x); err != nil {
				return err
			}
			view = x.View()
			return nil
		})
	})
	return view, err
}

// ---- VoidOpenInvoices / GetOpenInvoice ----

func (s *Service) subscriptionTenant(ctx context.Context, sub sk.SubscriptionID) (sk.TenantID, bool, error) {
	var t sk.TenantID
	found := true
	err := s.d.DB.ReadTx(system(ctx, "billing_lookup"), func(ctx context.Context, tx db.Tx) error {
		var err error
		t, err = postgres.New(tx).SubscriptionTenant(ctx, sub)
		if errors.Is(err, postgres.ErrNotFound) {
			found = false
			return nil
		}
		return err
	})
	return t, found, err
}

func (s *Service) VoidOpenInvoices(ctx context.Context, sub sk.SubscriptionID, reason c.VoidReason) ([]c.InvoiceView, error) {
	switch reason {
	case c.VoidSignupFailed, c.VoidSubscriptionCanceled, c.VoidTenantSuspended:
	default:
		return nil, fmt.Errorf("%w: void reason %q", c.ErrValidation, reason)
	}
	tenant, found, err := s.subscriptionTenant(ctx, sub)
	if err != nil || !found {
		return nil, err
	}
	var out []c.InvoiceView
	err = retry(func() error {
		out = nil
		return s.d.DB.InTx(db.WithTenantScope(ctx, tenant), func(ctx context.Context, tx db.Tx) error {
			repo := postgres.New(tx)
			x, err := repo.GetUnsettled(ctx, sub, true)
			if errors.Is(err, postgres.ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			if !x.Void(reason, s.d.Clock.Now()) {
				return nil
			}
			if err := s.save(ctx, tx, repo, x); err != nil {
				return err
			}
			out = append(out, x.View())
			return nil
		})
	})
	return out, err
}

func (s *Service) GetOpenInvoice(ctx context.Context, sub sk.SubscriptionID) (*c.InvoiceView, error) {
	tenant, found, err := s.subscriptionTenant(ctx, sub)
	if err != nil || !found {
		return nil, err
	}
	var out *c.InvoiceView
	err = s.d.DB.ReadTx(db.WithTenantScope(ctx, tenant), func(ctx context.Context, tx db.Tx) error {
		x, err := postgres.New(tx).GetUnsettled(ctx, sub, false)
		if errors.Is(err, postgres.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		v := x.View()
		out = &v
		return nil
	})
	return out, err
}

// GetInvoice は ID で請求を読む（contracts。事業者を引いてからテナントのスコープで読む）。
func (s *Service) GetInvoice(ctx context.Context, id sk.InvoiceID) (c.InvoiceView, error) {
	tenant, err := s.invoiceTenant(ctx, id)
	if err != nil {
		return c.InvoiceView{}, err
	}
	st, err := s.GetInvoiceState(db.WithTenantScope(ctx, tenant), id)
	if err != nil {
		return c.InvoiceView{}, err
	}
	return domain.Restore(st).View(), nil
}

// ---- API 用 ----

// ListInvoices は ctx のスコープ（tenant または customer）で見える契約の請求の一覧（limit+1 件読んで続きの有無を返す）。
func (s *Service) ListInvoices(ctx context.Context, sub sk.SubscriptionID, p postgres.Page) ([]domain.State, bool, error) {
	var out []domain.State
	more := false
	err := s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		lim := p.Limit
		p.Limit = lim + 1
		xs, err := postgres.New(tx).ListBySubscription(ctx, sub, p)
		if err != nil {
			return err
		}
		if len(xs) > lim {
			xs, more = xs[:lim], true
		}
		for _, x := range xs {
			out = append(out, x.State())
		}
		return nil
	})
	return out, more, err
}

// GetInvoiceState は ctx のスコープで請求を読む。見えなければ ErrInvoiceNotFound。
func (s *Service) GetInvoiceState(ctx context.Context, id sk.InvoiceID) (domain.State, error) {
	var st domain.State
	err := s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		x, err := postgres.New(tx).Get(ctx, id)
		if err != nil {
			return mapNotFound(err)
		}
		st = x.State()
		return nil
	})
	return st, err
}

// OpsCollect は ops の再決済（§7.3）。監査の注釈に invoice_id・collection_seq・note を残す。
func (s *Service) OpsCollect(ctx context.Context, id sk.InvoiceID, operator, note string) (c.InvoiceView, time.Time, error) {
	v, err := s.RequestImmediateCollection(ctx, id, c.CollectionRequest{Reason: c.ReasonOps, RequestedBy: operator})
	if err != nil {
		return c.InvoiceView{}, time.Time{}, err
	}
	st, err := s.GetInvoiceState(db.WithTenantScope(ctx, v.TenantID), id)
	if err != nil {
		return c.InvoiceView{}, time.Time{}, err
	}
	requested := s.d.Clock.Now()
	if st.Pending != nil {
		requested = st.Pending.RequestedAt
	}
	auditctx.Annotate(ctx, "invoice.collect", auditctx.Target{Type: "invoice", ID: id.String()}, nil,
		map[string]any{"collection_seq": v.CollectionSeq, "note": note}, auditctx.WithTenant(v.TenantID))
	return v, requested, nil
}

// Policy は ctx の事業者の方針（なければデフォルト）。
func (s *Service) Policy(ctx context.Context, t sk.TenantID) (domain.DunningPolicy, error) {
	var p domain.DunningPolicy
	err := s.d.DB.ReadTx(db.WithTenantScope(ctx, t), func(ctx context.Context, tx db.Tx) error {
		var err error
		p, err = postgres.New(tx).GetPolicy(ctx, t)
		return err
	})
	return p, err
}

// UpdatePolicy は方針を保存する（DP-1〜DP-3 を検証）。監査の注釈に変更前後の offsets を残す（P-X-2）。
// expected は If-Match（任意。未設定の既定は 0）。違えば postgres.ErrVersionConflict（business-console FR-015）。
func (s *Service) UpdatePolicy(ctx context.Context, t sk.TenantID, offsets []int, by string, expected *int) (domain.DunningPolicy, error) {
	if err := domain.ValidateOffsets(offsets); err != nil {
		return domain.DunningPolicy{}, err
	}
	var before, after domain.DunningPolicy
	err := s.d.DB.InTx(db.WithTenantScope(ctx, t), func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		var err error
		if before, err = repo.GetPolicy(ctx, t); err != nil {
			return err
		}
		if expected != nil && *expected != before.Version {
			return postgres.ErrVersionConflict
		}
		after, err = repo.UpsertPolicy(ctx, t, offsets, by, s.d.Clock.Now())
		return err
	})
	if err != nil {
		return domain.DunningPolicy{}, err
	}
	auditctx.Annotate(ctx, "dunning_policy.update", auditctx.Target{Type: "dunning_policy", ID: t.String()},
		map[string]any{"retry_offsets_days": before.Offsets}, map[string]any{"retry_offsets_days": after.Offsets}, auditctx.WithTenant(t))
	return after, nil
}
