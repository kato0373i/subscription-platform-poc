package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/db"
	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/subscription/contracts"
	"subscription-platform/modules/subscription/internal/adapter/postgres"
	"subscription-platform/modules/subscription/internal/domain"
)

// contracts.Service の実装。呼び出し元（Orchestration・ジョブ）は契約 ID だけを持つので、
// 事業者を引いてから tenant スコープのトランザクションで変更する（§6 の RLS）。

// command は契約の事業者のスコープで mutate する。
func (s *Service) command(ctx context.Context, id sk.SubscriptionID, fn func(x *domain.Subscription) error) (*domain.Subscription, error) {
	t, err := s.tenantOf(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.mutate(db.WithTenantScope(ctx, t), id, fn)
}

// CreateForSignup は申込の契約を作る（§4.2 の判定）。同じ (customer, signup_request_id) は既存を返す。
func (s *Service) CreateForSignup(ctx context.Context, cmd c.CreateSignupCmd) (c.SubscriptionView, error) {
	if cmd.TenantCustomerID == "" || cmd.SignupRequestID == "" || !cmd.Plan.Interval.Valid() || cmd.PaymentMethodID.IsZero() {
		return c.SubscriptionView{}, fmt.Errorf("subscription: invalid signup command")
	}
	ctx = db.WithTenantScope(ctx, cmd.TenantID)
	var out c.SubscriptionView
	err := s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		if existing, err := repo.GetBySignupRequest(ctx, cmd.CustomerAccountID, cmd.SignupRequestID); err == nil {
			out = existing.View()
			return nil
		} else if !errors.Is(err, postgres.ErrNotFound) {
			return err
		}
		applyTrial := false
		if cmd.Plan.TrialDays > 0 {
			used, err := repo.HasTrialUsage(ctx, cmd.CustomerAccountID, cmd.Plan.ProductID)
			if err != nil {
				return err
			}
			if used && !cmd.AllowTrialFallback {
				return c.ErrTrialNotEligible
			}
			applyTrial = !used
		}
		now := s.d.Clock.Now()
		id := cmd.SubscriptionID
		if id.IsZero() {
			id = sk.NewID[sk.SubscriptionTag]()
		}
		x := domain.NewForSignup(domain.SignupInput{
			ID: id, TenantID: cmd.TenantID, CustomerAccountID: cmd.CustomerAccountID, TenantCustomerID: cmd.TenantCustomerID,
			SignupRequestID: cmd.SignupRequestID, Plan: cmd.Plan, PaymentMethodID: cmd.PaymentMethodID, Discount: cmd.Discount,
			SignupDate: cmd.SignupDate, ApplyTrial: applyTrial, Now: now,
		})
		if err := repo.Insert(ctx, x, now); err != nil {
			return err
		}
		if x.State().Status == c.StatusTrialing {
			if err := repo.InsertTrialUsage(ctx, x.State(), now); err != nil {
				if errors.Is(err, postgres.ErrTrialAlreadyUsed) {
					return c.ErrTrialNotEligible // 同時のトライアル申込（X-11）
				}
				return err
			}
		}
		out = x.View()
		return s.appendEvents(ctx, tx, x, x.Events())
	})
	if errors.Is(err, postgres.ErrDuplicateSignup) {
		// 同じ申込の同時実行。先に作られた方を返す
		err = s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
			x, err := postgres.New(tx).GetBySignupRequest(ctx, cmd.CustomerAccountID, cmd.SignupRequestID)
			if err != nil {
				return mapNotFound(err)
			}
			out = x.View()
			return nil
		})
	}
	return out, err
}

func (s *Service) Activate(ctx context.Context, id sk.SubscriptionID, inv sk.InvoiceID) (c.SubscriptionView, error) {
	x, err := s.command(ctx, id, func(x *domain.Subscription) error { return x.Activate(inv) })
	return viewOf(x), err
}

func (s *Service) MarkSignupFailed(ctx context.Context, id sk.SubscriptionID, reason string) error {
	_, err := s.command(ctx, id, func(x *domain.Subscription) error { return x.MarkSignupFailed(reason) })
	return err
}

func (s *Service) BeginRenewal(ctx context.Context, id sk.SubscriptionID, date sk.Date) (c.RenewalOutcome, error) {
	var ended bool
	var terms *c.Terms
	x, err := s.command(ctx, id, func(x *domain.Subscription) error {
		var err error
		ended, terms, err = x.BeginRenewal(date, s.d.Clock.Now())
		return err
	})
	if err != nil {
		return c.RenewalOutcome{}, err
	}
	if ended {
		return c.RenewalOutcome{Kind: c.RenewalEnded, Subscription: x.View()}, nil
	}
	return c.RenewalOutcome{Kind: c.RenewalTerms, Terms: terms, Subscription: x.View()}, nil
}

func (s *Service) ConfirmRenewal(ctx context.Context, id sk.SubscriptionID, inv sk.InvoiceID) (c.SubscriptionView, error) {
	x, err := s.command(ctx, id, func(x *domain.Subscription) error { return x.ConfirmRenewal(inv) })
	return viewOf(x), err
}

func (s *Service) MarkPastDue(ctx context.Context, id sk.SubscriptionID, inv sk.InvoiceID) (c.SubscriptionView, error) {
	x, err := s.command(ctx, id, func(x *domain.Subscription) error { return x.MarkPastDue(inv, s.d.Clock.Now()) })
	return viewOf(x), err
}

func (s *Service) Recover(ctx context.Context, id sk.SubscriptionID, inv sk.InvoiceID) (c.SubscriptionView, error) {
	x, err := s.command(ctx, id, func(x *domain.Subscription) error { return x.Recover(inv) })
	return viewOf(x), err
}

func (s *Service) Cancel(ctx context.Context, id sk.SubscriptionID, reason c.CancelReason) (c.SubscriptionView, error) {
	x, err := s.command(ctx, id, func(x *domain.Subscription) error { return x.Cancel(reason, s.d.Clock.Now()) })
	return viewOf(x), err
}

func (s *Service) ScheduleSystemCancellation(ctx context.Context, id sk.SubscriptionID, reason c.CancelReason) (c.SystemCancellationResult, error) {
	var res domain.SystemCancellation
	x, err := s.command(ctx, id, func(x *domain.Subscription) error {
		res = x.ScheduleSystemCancellation(reason, s.d.Clock.Now())
		return nil
	})
	if err != nil {
		return c.SystemCancellationResult{}, err
	}
	return c.SystemCancellationResult{Kind: res.Kind, EffectiveDate: res.EffectiveDate, Subscription: x.View()}, nil
}

// ChangePaymentMethod は契約者本人の契約だけを付け替える（他人の契約は ErrNotFound）。
func (s *Service) ChangePaymentMethod(ctx context.Context, id sk.SubscriptionID, customer sk.CustomerAccountID, pm sk.PaymentMethodID) (c.SubscriptionView, error) {
	x, err := s.command(ctx, id, func(x *domain.Subscription) error {
		if x.State().CustomerAccountID != customer {
			return c.ErrNotFound
		}
		return x.ChangePaymentMethod(pm)
	})
	return viewOf(x), err
}

func (s *Service) Get(ctx context.Context, id sk.SubscriptionID) (c.SubscriptionView, error) {
	var v c.SubscriptionView
	err := s.d.DB.ReadTx(db.WithSystemScope(ctx, "subscription_get"), func(ctx context.Context, tx db.Tx) error {
		x, err := postgres.New(tx).Get(ctx, id)
		if err != nil {
			return mapNotFound(err)
		}
		v = x.View()
		return nil
	})
	return v, err
}

func viewOf(x *domain.Subscription) c.SubscriptionView {
	if x == nil {
		return c.SubscriptionView{}
	}
	return x.View()
}

// ---- 一覧（ジョブ用。事業者をまたぐので BYPASSRLS のロールで読む。規約 M5） ----

func (s *Service) system(ctx context.Context, reason string, fn func(ctx context.Context, repo *postgres.Repo) error) error {
	return s.d.DB.ReadTx(db.WithSystemScope(ctx, reason), func(ctx context.Context, tx db.Tx) error { return fn(ctx, postgres.New(tx)) })
}

func clampLimit(n int) int {
	if n <= 0 || n > 1000 {
		return 500
	}
	return n
}

// ListDueForRenewal は更新日が date 以前の trialing / active（R 済みも含め、再実行を冪等に返す。past_due は含めない。D-02）。
// cursor は "日付|ID"。
func (s *Service) ListDueForRenewal(ctx context.Context, date sk.Date, cursor string, limit int) (c.Page[c.SubscriptionRef], error) {
	var afterDate *sk.Date
	var afterID *uuid.UUID
	if cursor != "" {
		ds, is, ok := strings.Cut(cursor, "|")
		dd, err1 := sk.ParseDate(ds)
		u, err2 := uuid.Parse(is)
		if !ok || err1 != nil || err2 != nil {
			return c.Page[c.SubscriptionRef]{}, fmt.Errorf("subscription: bad cursor %q", cursor)
		}
		afterDate, afterID = &dd, &u
	}
	limit = clampLimit(limit)
	var items []c.SubscriptionRef
	err := s.system(ctx, "list_due_for_renewal", func(ctx context.Context, repo *postgres.Repo) error {
		var err error
		items, err = repo.ListDueForRenewal(ctx, date, afterDate, afterID, limit)
		return err
	})
	page := c.Page[c.SubscriptionRef]{Items: items}
	if len(items) == limit {
		last := items[len(items)-1]
		page.NextCursor = last.NextRenewalDate.String() + "|" + last.SubscriptionID.String()
	}
	return page, err
}

func idCursor(cursor string) (*uuid.UUID, error) {
	if cursor == "" {
		return nil, nil
	}
	u, err := uuid.Parse(cursor)
	if err != nil {
		return nil, fmt.Errorf("subscription: bad cursor %q", cursor)
	}
	return &u, nil
}

func idPage(items []c.SubscriptionRef, limit int) c.Page[c.SubscriptionRef] {
	page := c.Page[c.SubscriptionRef]{Items: items}
	if len(items) == limit {
		page.NextCursor = items[len(items)-1].SubscriptionID.String()
	}
	return page
}

func (s *Service) ListTrialEndingOn(ctx context.Context, date sk.Date, cursor string, limit int) (c.Page[c.SubscriptionRef], error) {
	after, err := idCursor(cursor)
	if err != nil {
		return c.Page[c.SubscriptionRef]{}, err
	}
	limit = clampLimit(limit)
	var items []c.SubscriptionRef
	err = s.system(ctx, "list_trial_ending", func(ctx context.Context, repo *postgres.Repo) error {
		items, err = repo.ListTrialEndingOn(ctx, date, after, limit)
		return err
	})
	return idPage(items, limit), err
}

func (s *Service) ListOpenByTenant(ctx context.Context, tenant sk.TenantID, cursor string, limit int) (c.Page[c.SubscriptionRef], error) {
	after, err := idCursor(cursor)
	if err != nil {
		return c.Page[c.SubscriptionRef]{}, err
	}
	limit = clampLimit(limit)
	var items []c.SubscriptionRef
	err = s.d.DB.ReadTx(db.WithTenantScope(ctx, tenant), func(ctx context.Context, tx db.Tx) error {
		items, err = postgres.New(tx).ListOpenByTenant(ctx, tenant, after, limit)
		return err
	})
	return idPage(items, limit), err
}

// HasOpenSubscriptions は退会の判定（D-05）。契約者のスコープ（consumer_self）で読む。
func (s *Service) HasOpenSubscriptions(ctx context.Context, customer sk.CustomerAccountID) (bool, error) {
	var has bool
	err := s.d.DB.ReadTx(db.WithCustomerScope(ctx, customer), func(ctx context.Context, tx db.Tx) error {
		var err error
		has, err = postgres.New(tx).HasOpenSubscriptions(ctx, customer)
		return err
	})
	return has, err
}

// IsPaymentMethodInUse は支払方法の削除の判定。支払方法 ID だけなので BYPASSRLS のロールで読む。
func (s *Service) IsPaymentMethodInUse(ctx context.Context, pm sk.PaymentMethodID) (bool, error) {
	var used bool
	err := s.system(ctx, "payment_method_in_use", func(ctx context.Context, repo *postgres.Repo) error {
		var err error
		used, err = repo.IsPaymentMethodInUse(ctx, pm)
		return err
	})
	return used, err
}
