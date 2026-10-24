package app

import (
	"context"
	"errors"

	"subscription-platform/internal/platform/db"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/catalog/contracts"
	"subscription-platform/modules/catalog/internal/adapter/postgres"
	"subscription-platform/modules/catalog/internal/domain"
)

// 申込ページ用の公開 API（§7.5）。未認証なので、事業者は SECURITY DEFINER 関数で特定し、中身は改めて RLS の下で読む。

// CheckoutPlan は申込ページに見せるプラン。
type CheckoutPlan struct {
	Plan        domain.PlanState
	ProductName string
}

// GetCheckoutPlan は存在しない・draft なら ErrNotFound、unpublished・事業者が active でなければ contracts.ErrPlanUnavailable。
func (s *Service) GetCheckoutPlan(ctx context.Context, planID contracts.PlanID) (CheckoutPlan, sk.TenantID, error) {
	var t sk.TenantID
	err := s.d.DB.ReadTx(db.WithUnscoped(ctx, "checkout_plan"), func(ctx context.Context, tx db.Tx) error {
		var err error
		t, err = postgres.New(tx).ResolveCheckoutPlanTenant(ctx, planID.UUID())
		return mapNotFound(err, ErrNotFound)
	})
	if err != nil {
		return CheckoutPlan{}, t, err
	}
	var out CheckoutPlan
	err = s.d.DB.ReadTx(db.WithTenantScope(ctx, t), func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		p, err := repo.GetPlan(ctx, planID, false)
		if err != nil {
			return mapNotFound(err, ErrNotFound)
		}
		prod, err := repo.GetProduct(ctx, p.State().ProductID)
		if err != nil {
			return err
		}
		out = CheckoutPlan{Plan: p.State(), ProductName: prod.State().Name}
		return nil
	})
	if err != nil {
		return CheckoutPlan{}, t, err
	}
	if out.Plan.Status != contracts.PlanPublished {
		return CheckoutPlan{}, t, contracts.ErrPlanUnavailable
	}
	active, err := s.tenantActive(ctx, t)
	if err != nil {
		return CheckoutPlan{}, t, err // 安全側に倒して 500（§9.2）
	}
	if !active {
		return CheckoutPlan{}, t, contracts.ErrPlanUnavailable
	}
	return out, t, nil
}

// CouponPreview はクーポンの検証とプレビュー（予約はしない）。
type CouponPreview struct {
	Coupon  domain.CouponState
	Preview domain.Preview
}

// PreviewCoupon は §3.4 の順に検証する。customer があれば #4（使用済み）も判定する。
func (s *Service) PreviewCoupon(ctx context.Context, planID contracts.PlanID, code string, customer *sk.CustomerAccountID) (CouponPreview, error) {
	cp, t, err := s.GetCheckoutPlan(ctx, planID)
	if err != nil {
		return CouponPreview{}, err
	}
	var out CouponPreview
	err = s.d.DB.ReadTx(db.WithTenantScope(ctx, t), func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		c, err := repo.GetCouponByCode(ctx, domain.NormalizeCode(code))
		if err != nil {
			return mapNotFound(err, contracts.ErrCouponNotFound)
		}
		held := false
		if customer != nil {
			if held, err = repo.CustomerHoldsCoupon(ctx, c.State().ID, *customer); err != nil {
				return err
			}
		}
		if err := c.CheckRedeemable(planID, held, s.d.Clock.Now()); err != nil {
			return err
		}
		price := cp.Plan.CurrentPrice.Amount
		if price == nil {
			return errors.New("catalog: preview requires a fixed price")
		}
		out = CouponPreview{Coupon: c.State(), Preview: c.PreviewFor(*price)}
		return nil
	})
	return out, err
}
