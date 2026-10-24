package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"subscription-platform/internal/platform/db"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/catalog/contracts"
	"subscription-platform/modules/catalog/internal/adapter/postgres"
	"subscription-platform/modules/catalog/internal/domain"
)

// contracts.Service の実装。呼び出し元（Orchestration など）は worker からも呼ぶので、自分で tenant スコープを設定する。

func (s *Service) readPlan(ctx context.Context, t sk.TenantID, id contracts.PlanID) (*domain.Plan, error) {
	var p *domain.Plan
	err := s.d.DB.ReadTx(db.WithTenantScope(ctx, t), func(ctx context.Context, tx db.Tx) error {
		var err error
		p, err = postgres.New(tx).GetPlan(ctx, id, false)
		return mapNotFound(err, contracts.ErrPlanNotFound)
	})
	return p, err
}

// GetPurchasablePlan は published でなければ ErrPlanUnavailable。
func (s *Service) GetPurchasablePlan(ctx context.Context, t sk.TenantID, id contracts.PlanID) (contracts.PlanSnapshot, error) {
	p, err := s.readPlan(ctx, t, id)
	if err != nil {
		return contracts.PlanSnapshot{}, err
	}
	if !p.Purchasable() {
		return contracts.PlanSnapshot{}, contracts.ErrPlanUnavailable
	}
	return p.Snapshot(), nil
}

// GetPlanSnapshot は状態に関係なく返す。
func (s *Service) GetPlanSnapshot(ctx context.Context, t sk.TenantID, id contracts.PlanID) (contracts.PlanDetail, error) {
	p, err := s.readPlan(ctx, t, id)
	if err != nil {
		return contracts.PlanDetail{}, err
	}
	st := p.State()
	return contracts.PlanDetail{PlanSnapshot: p.Snapshot(), Status: st.Status, Published: st.Status == contracts.PlanPublished}, nil
}

// QuotePrice は価格版のスキームで価格を算出する（§3.3）。
func (s *Service) QuotePrice(ctx context.Context, t sk.TenantID, id contracts.PriceVersionID, usage *contracts.UsageInput) (contracts.Quote, error) {
	var pv domain.PriceVersion
	err := s.d.DB.ReadTx(db.WithTenantScope(ctx, t), func(ctx context.Context, tx db.Tx) error {
		var err error
		pv, err = postgres.New(tx).GetPriceVersion(ctx, id)
		return mapNotFound(err, contracts.ErrPriceVersionNotFound)
	})
	if err != nil {
		return contracts.Quote{}, err
	}
	scheme, err := s.d.Schemes.Get(pv.Scheme)
	if err != nil {
		return contracts.Quote{}, err
	}
	amount, err := scheme.Quote(pv.Price(), usage)
	if err != nil {
		return contracts.Quote{}, err
	}
	return contracts.Quote{Amount: amount, TaxRate: pv.TaxRate}, nil
}

// ReserveCoupon はクーポンの枠を 1 つ押さえる（§3.5 の 1〜5、1 トランザクション）。
func (s *Service) ReserveCoupon(ctx context.Context, t sk.TenantID, code string, planID contracts.PlanID, customer sk.CustomerAccountID, sub sk.SubscriptionID) (contracts.CouponReservation, error) {
	var out contracts.CouponReservation
	now := s.d.Clock.Now()
	err := s.d.DB.InTx(db.WithTenantScope(ctx, t), func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		if _, err := repo.GetPlan(ctx, planID, false); err != nil {
			return mapNotFound(err, contracts.ErrPlanNotFound)
		}
		c, err := repo.GetCouponByCode(ctx, domain.NormalizeCode(code))
		if err != nil {
			return mapNotFound(err, contracts.ErrCouponNotFound)
		}
		cs := c.State()
		reservation := func(id contracts.RedemptionID) contracts.CouponReservation {
			return contracts.CouponReservation{RedemptionID: id, CouponID: cs.ID, Discount: cs.Discount, DurationCycles: cs.DurationCycles}
		}
		// 1. 同じ (coupon, customer, subscription) の予約があれば返す（Orchestration の再試行。枠は増やさない）
		if existing, err := repo.FindActiveRedemption(ctx, cs.ID, customer, sub); err == nil {
			out = reservation(existing.State().ID)
			return nil
		} else if !errors.Is(err, postgres.ErrNotFound) {
			return err
		}
		// 2. 検証 #1〜#4（#5 は条件付き UPDATE で判定する）
		held, err := repo.CustomerHoldsCoupon(ctx, cs.ID, customer)
		if err != nil {
			return err
		}
		if err := c.CheckRedeemable(planID, held, now); err != nil && !errors.Is(err, contracts.ErrCouponExhausted) {
			return err
		}
		// 3. 枠を取る。0 行なら読み直して期限か上限を返す
		ok, err := repo.ClaimSlot(ctx, cs.ID, now)
		if err != nil {
			return err
		}
		if !ok {
			cur, err := repo.GetCoupon(ctx, cs.ID)
			if err != nil {
				return err
			}
			if err := cur.CheckRedeemable(planID, false, now); err != nil {
				return err
			}
			return contracts.ErrCouponExhausted
		}
		// 4. 予約を保存する。部分一意に当たればトランザクションごと戻す（枠も戻る）
		red := domain.Reserve(sk.NewID[contracts.RedemptionTag](), c, planID, customer, sub, s.d.ReservationTTL, now)
		if err := repo.InsertRedemption(ctx, red); err != nil {
			if errors.Is(err, postgres.ErrDuplicateActive) {
				return contracts.ErrCouponAlreadyUsed
			}
			return fmt.Errorf("catalog: insert redemption: %w", err)
		}
		// 5. 予約時にイベントを出す（確定時は出さない）
		rs := red.State()
		env, err := sk.NewEnvelope(contracts.EventCouponRedeemed, "coupon_redemption", rs.ID.UUID(), 1, &t, now, contracts.CouponRedeemedPayload{
			CouponID: cs.ID, RedemptionID: rs.ID, CustomerAccountID: customer, SubscriptionID: sub, PlanID: planID,
		})
		if err != nil {
			return err
		}
		out = reservation(rs.ID)
		return s.d.Outbox.Append(ctx, tx, env)
	})
	return out, err
}

// ConfirmCouponRedemption は予約を確定する。期限切れで解放済みなら枠を取り直す（取れなければ over_limit。P-CAT-2）。
func (s *Service) ConfirmCouponRedemption(ctx context.Context, t sk.TenantID, id contracts.RedemptionID) error {
	now := s.d.Clock.Now()
	return s.d.DB.InTx(db.WithTenantScope(ctx, t), func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		red, err := repo.GetRedemption(ctx, id, postgres.ForUpdate)
		if err != nil {
			return mapNotFound(err, contracts.ErrRedemptionNotFound)
		}
		from := red.State().Status
		result, err := red.PrepareConfirm()
		if err != nil {
			return err
		}
		overLimit := false
		switch result {
		case domain.ConfirmNoop:
			return nil
		case domain.ConfirmKeepSlot:
		case domain.ConfirmNeedSlot:
			ok, err := repo.ReclaimSlot(ctx, red.State().CouponID, now)
			if err != nil {
				return err
			}
			overLimit = !ok
		}
		red.Confirm(overLimit, now)
		return repo.UpdateRedemption(ctx, red, from)
	})
}

// ReleaseCouponRedemption は呼び出し元からの解放（申込の不成立）。
func (s *Service) ReleaseCouponRedemption(ctx context.Context, t sk.TenantID, id contracts.RedemptionID) error {
	now := s.d.Clock.Now()
	return s.d.DB.InTx(db.WithTenantScope(ctx, t), func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		red, err := repo.GetRedemption(ctx, id, postgres.ForUpdate)
		if err != nil {
			return mapNotFound(err, contracts.ErrRedemptionNotFound)
		}
		from := red.State().Status
		released, err := red.Release(now)
		if err != nil || !released {
			return err
		}
		return s.releaseSlot(ctx, tx, repo, red, from, now)
	})
}

// releaseSlot は解放した予約の枠を戻し、イベントを出す（呼び出し元の解放と掃除ジョブで共通）。
func (s *Service) releaseSlot(ctx context.Context, tx db.Tx, repo *postgres.Repo, red *domain.Redemption, from domain.RedemptionStatus, now time.Time) error {
	rs := red.State()
	if err := repo.UpdateRedemption(ctx, red, from); err != nil {
		return err
	}
	if err := repo.ReturnSlot(ctx, rs.CouponID, now); err != nil {
		return fmt.Errorf("catalog: return slot: %w", err)
	}
	reason := ""
	if rs.ReleaseReason != nil {
		reason = string(*rs.ReleaseReason)
	}
	env, err := sk.NewEnvelope(contracts.EventCouponRedemptionReleased, "coupon_redemption", rs.ID.UUID(), int64(rs.Version+1), &rs.TenantID, now,
		contracts.CouponRedemptionReleasedPayload{CouponID: rs.CouponID, RedemptionID: rs.ID, CustomerAccountID: rs.CustomerAccountID, SubscriptionID: rs.SubscriptionID, Reason: reason})
	if err != nil {
		return err
	}
	return s.d.Outbox.Append(ctx, tx, env)
}
