package app

import (
	"context"

	"subscription-platform/internal/platform/auditctx"
	"subscription-platform/internal/platform/db"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/catalog/contracts"
	"subscription-platform/modules/catalog/internal/adapter/postgres"
	"subscription-platform/modules/catalog/internal/domain"
)

// console のユースケース。router が tenant スコープを設定し、書き込みの可否（停止中の事業者）は Platform の認可が判定する。

func target(kind string, id interface{ String() string }) auditctx.Target {
	return auditctx.Target{Type: kind, ID: id.String()}
}

// ---- products ----

func (s *Service) CreateProduct(ctx context.Context, t sk.TenantID, name, desc string) (domain.ProductState, error) {
	p, err := domain.NewProduct(sk.NewID[contracts.ProductTag](), t, name, desc, s.d.Clock.Now())
	if err != nil {
		return domain.ProductState{}, err
	}
	if err := s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error { return postgres.New(tx).InsertProduct(ctx, p) }); err != nil {
		return domain.ProductState{}, err
	}
	st := p.State()
	auditctx.Annotate(ctx, "product.create", target("product", st.ID), nil, map[string]any{"product_name": st.Name, "description": st.Description})
	return st, nil
}

func (s *Service) GetProduct(ctx context.Context, id contracts.ProductID) (domain.ProductState, error) {
	var st domain.ProductState
	err := s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		p, err := postgres.New(tx).GetProduct(ctx, id)
		if err != nil {
			return mapNotFound(err, ErrNotFound)
		}
		st = p.State()
		return nil
	})
	return st, err
}

// expected は If-Match（任意）。違えば postgres.ErrVersionConflict（business-console FR-015）。
func (s *Service) UpdateProduct(ctx context.Context, id contracts.ProductID, name, desc *string, expected *int) (domain.ProductState, error) {
	var before, after domain.ProductState
	var changed bool
	err := s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		p, err := repo.GetProduct(ctx, id)
		if err != nil {
			return mapNotFound(err, ErrNotFound)
		}
		before = p.State()
		if expected != nil && *expected != before.Version {
			return postgres.ErrVersionConflict
		}
		if changed, err = p.Rename(name, desc); err != nil || !changed {
			after = p.State()
			return err
		}
		after = p.State()
		return repo.UpdateProduct(ctx, p, s.d.Clock.Now())
	})
	if err != nil {
		return domain.ProductState{}, err
	}
	if changed {
		after.Version++
		auditctx.Annotate(ctx, "product.update", target("product", id),
			map[string]any{"product_name": before.Name, "description": before.Description},
			map[string]any{"product_name": after.Name, "description": after.Description})
	}
	return after, nil
}

func (s *Service) ListProducts(ctx context.Context, after *postgres.Cursor, limit int) ([]domain.ProductState, bool, error) {
	var out []domain.ProductState
	err := s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		ps, err := postgres.New(tx).ListProducts(ctx, after, limit+1)
		for _, p := range ps {
			out = append(out, p.State())
		}
		return err
	})
	if err != nil {
		return nil, false, err
	}
	out, more := page(out, limit)
	return out, more, nil
}

// ---- plans ----

// CreatePlan はプランと v1 の価格版を作る。商品が見えなければ ErrNotFound（他の事業者の商品も。AC-CAT-003）。
func (s *Service) CreatePlan(ctx context.Context, in domain.PlanInput) (domain.PlanState, error) {
	in.ID, in.PriceID = sk.NewID[contracts.PlanTag](), sk.NewID[contracts.PriceVersionTag]()
	p, err := domain.NewPlan(s.d.Schemes, in, s.d.Clock.Now())
	if err != nil {
		return domain.PlanState{}, err
	}
	err = s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		if _, err := repo.GetProduct(ctx, in.ProductID); err != nil {
			return mapNotFound(err, ErrNotFound)
		}
		if err := repo.InsertPlan(ctx, p); err != nil {
			return err
		}
		return s.appendPriceAdded(ctx, tx, p, p.State().CurrentPrice, 1)
	})
	if err != nil {
		return domain.PlanState{}, err
	}
	st := p.State()
	auditctx.Annotate(ctx, "plan.create", target("plan", st.ID), nil, map[string]any{
		"plan_name": st.Name, "description": st.Description, "trial_days": st.TrialDays, "amount": st.CurrentPrice.AmountYen(),
	})
	return st, nil
}

func (s *Service) appendPriceAdded(ctx context.Context, tx db.Tx, p *domain.Plan, pv domain.PriceVersion, aggVersion int) error {
	st := p.State()
	env, err := sk.NewEnvelope(contracts.EventPriceVersionAdded, "plan", st.ID.UUID(), int64(aggVersion), &st.TenantID, pv.EffectiveFrom,
		contracts.PriceVersionAddedPayload{PlanID: st.ID, PriceVersionID: pv.ID, VersionNo: pv.VersionNo, Amount: pv.AmountYen(), TaxRate: pv.TaxRate.String()})
	if err != nil {
		return err
	}
	return s.d.Outbox.Append(ctx, tx, env)
}

// GetPlan は console・公開 API の参照。なければ ErrNotFound。
func (s *Service) GetPlan(ctx context.Context, id contracts.PlanID) (domain.PlanState, error) {
	var st domain.PlanState
	err := s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		p, err := postgres.New(tx).GetPlan(ctx, id, false)
		if err != nil {
			return mapNotFound(err, ErrNotFound)
		}
		st = p.State()
		return nil
	})
	return st, err
}

func (s *Service) ListPlans(ctx context.Context, f postgres.PlanFilter, after *postgres.Cursor, limit int) ([]domain.PlanState, bool, error) {
	var out []domain.PlanState
	err := s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		ps, err := postgres.New(tx).ListPlans(ctx, f, after, limit+1)
		for _, p := range ps {
			out = append(out, p.State())
		}
		return err
	})
	if err != nil {
		return nil, false, err
	}
	out, more := page(out, limit)
	return out, more, nil
}

// withPlan は行をロックしたプランに fn を適用し、変わったら保存する（同じプランへの変更の直列化。§7.3）。
func (s *Service) withPlan(ctx context.Context, id contracts.PlanID, fn func(ctx context.Context, tx db.Tx, repo *postgres.Repo, p *domain.Plan) (bool, error)) (before, after domain.PlanState, changed bool, err error) {
	err = s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		p, err := repo.GetPlan(ctx, id, true)
		if err != nil {
			return mapNotFound(err, ErrNotFound)
		}
		before = p.State()
		if changed, err = fn(ctx, tx, repo, p); err != nil {
			return err
		}
		after = p.State()
		if !changed {
			return nil
		}
		return repo.UpdatePlan(ctx, p, before.Status, s.d.Clock.Now())
	})
	if changed {
		after.Version++
	}
	return before, after, changed, err
}

// expected は If-Match（任意）。違えば postgres.ErrVersionConflict（business-console FR-015）。
func (s *Service) UpdatePlan(ctx context.Context, id contracts.PlanID, name, desc *string, trial *int, expected *int) (domain.PlanState, error) {
	before, after, changed, err := s.withPlan(ctx, id, func(_ context.Context, _ db.Tx, _ *postgres.Repo, p *domain.Plan) (bool, error) {
		if expected != nil && *expected != p.State().Version {
			return false, postgres.ErrVersionConflict
		}
		return p.UpdateAttributes(name, desc, trial)
	})
	if err != nil {
		return domain.PlanState{}, err
	}
	if changed {
		auditctx.Annotate(ctx, "plan.update", target("plan", id),
			map[string]any{"plan_name": before.Name, "description": before.Description, "trial_days": before.TrialDays},
			map[string]any{"plan_name": after.Name, "description": after.Description, "trial_days": after.TrialDays})
	}
	return after, nil
}

// AddPriceVersion は価格を改定する（即時）。同じ金額は domain.ErrPriceUnchanged。
func (s *Service) AddPriceVersion(ctx context.Context, id contracts.PlanID, amount int64) (domain.PriceVersion, error) {
	var pv domain.PriceVersion
	before, _, _, err := s.withPlan(ctx, id, func(ctx context.Context, tx db.Tx, repo *postgres.Repo, p *domain.Plan) (bool, error) {
		var err error
		if pv, err = p.AddPriceVersion(s.d.Schemes, sk.NewID[contracts.PriceVersionTag](), amount, s.d.Clock.Now()); err != nil {
			return false, err
		}
		if err := repo.InsertPriceVersion(ctx, p, pv); err != nil {
			return false, err
		}
		// プラン行の version も上げる（価格版の追加は集約の変更）
		return true, s.appendPriceAdded(ctx, tx, p, pv, p.State().Version+1)
	})
	if err != nil {
		return domain.PriceVersion{}, err
	}
	old := before.CurrentPrice
	auditctx.Annotate(ctx, "plan.price_version_add", target("plan", id),
		map[string]any{"price_version_id": old.ID.String(), "amount": old.AmountYen(), "tax_rate": old.TaxRate.String()},
		map[string]any{"price_version_id": pv.ID.String(), "amount": pv.AmountYen(), "tax_rate": pv.TaxRate.String()})
	return pv, nil
}

// ListPriceVersions は version_no の降順。プランが見えなければ ErrNotFound。
func (s *Service) ListPriceVersions(ctx context.Context, id contracts.PlanID, beforeNo *int, limit int) ([]domain.PriceVersion, bool, error) {
	var out []domain.PriceVersion
	err := s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		if _, err := repo.GetPlan(ctx, id, false); err != nil {
			return mapNotFound(err, ErrNotFound)
		}
		var err error
		out, err = repo.ListPriceVersions(ctx, id, beforeNo, limit+1)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	out, more := page(out, limit)
	return out, more, nil
}

// PublishPlan は公開する。事業者が active でなければ domain.ErrTenantNotActive（AC-CAT-004）。
func (s *Service) PublishPlan(ctx context.Context, t sk.TenantID, id contracts.PlanID) (domain.PlanState, error) {
	active, err := s.tenantActive(ctx, t)
	if err != nil {
		return domain.PlanState{}, err
	}
	before, after, changed, err := s.withPlan(ctx, id, func(ctx context.Context, tx db.Tx, _ *postgres.Repo, p *domain.Plan) (bool, error) {
		changed, err := p.Publish(active, s.d.Clock.Now())
		if err != nil || !changed {
			return changed, err
		}
		st := p.State()
		env, err := sk.NewEnvelope(contracts.EventPlanPublished, "plan", st.ID.UUID(), int64(st.Version+1), &st.TenantID, s.d.Clock.Now(),
			contracts.PlanPublishedPayload{PlanID: st.ID, ProductID: st.ProductID, PriceVersionID: st.CurrentPrice.ID})
		if err != nil {
			return false, err
		}
		return true, s.d.Outbox.Append(ctx, tx, env)
	})
	if err != nil {
		return domain.PlanState{}, err
	}
	if changed {
		auditctx.Annotate(ctx, "plan.publish", target("plan", id), map[string]any{"published": false, "status": string(before.Status)},
			map[string]any{"published": true, "status": string(after.Status)})
	}
	return after, nil
}

// UnpublishPlan は非公開にする。draft は domain.ErrInvalidPlanTransition。
func (s *Service) UnpublishPlan(ctx context.Context, id contracts.PlanID) (domain.PlanState, error) {
	_, after, changed, err := s.withPlan(ctx, id, func(ctx context.Context, tx db.Tx, _ *postgres.Repo, p *domain.Plan) (bool, error) {
		changed, err := p.Unpublish()
		if err != nil || !changed {
			return changed, err
		}
		st := p.State()
		env, err := sk.NewEnvelope(contracts.EventPlanUnpublished, "plan", st.ID.UUID(), int64(st.Version+1), &st.TenantID, s.d.Clock.Now(),
			contracts.PlanUnpublishedPayload{PlanID: st.ID, ProductID: st.ProductID})
		if err != nil {
			return false, err
		}
		return true, s.d.Outbox.Append(ctx, tx, env)
	})
	if err != nil {
		return domain.PlanState{}, err
	}
	if changed {
		auditctx.Annotate(ctx, "plan.unpublish", target("plan", id), map[string]any{"published": true}, map[string]any{"published": false})
	}
	return after, nil
}

// ---- coupons ----

// CreateCoupon はクーポンを作る。対象プランが見えなければ ErrNotFound、同じコードは postgres.ErrCodeTaken。
func (s *Service) CreateCoupon(ctx context.Context, in domain.CouponInput) (domain.CouponState, error) {
	in.ID = sk.NewID[contracts.CouponTag]()
	c, err := domain.NewCoupon(in, s.d.Clock.Now())
	if err != nil {
		return domain.CouponState{}, err
	}
	err = s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		for _, pid := range c.State().PlanIDs {
			if _, err := repo.GetPlan(ctx, pid, false); err != nil {
				return mapNotFound(err, ErrNotFound)
			}
		}
		return repo.InsertCoupon(ctx, c)
	})
	if err != nil {
		return domain.CouponState{}, err
	}
	st := c.State()
	auditctx.Annotate(ctx, "coupon.create", target("coupon", st.ID), nil, couponAudit(st))
	return st, nil
}

func couponAudit(st domain.CouponState) map[string]any {
	ids := make([]string, len(st.PlanIDs))
	for i, id := range st.PlanIDs {
		ids[i] = id.String()
	}
	m := map[string]any{
		"code": st.Code, "discount_type": string(st.Discount.Type), "discount_value": st.Discount.Value, "duration_cycles": st.DurationCycles,
		"applies_to": string(st.AppliesTo), "plan_ids": ids, "status": string(st.Status),
	}
	if st.ExpiresAt != nil {
		m["expires_at"] = st.ExpiresAt.UTC()
	}
	if st.MaxRedemptions != nil {
		m["max_redemptions"] = *st.MaxRedemptions
	}
	return m
}

func (s *Service) GetCoupon(ctx context.Context, id contracts.CouponID) (domain.CouponState, error) {
	var st domain.CouponState
	err := s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		c, err := postgres.New(tx).GetCoupon(ctx, id)
		if err != nil {
			return mapNotFound(err, ErrNotFound)
		}
		st = c.State()
		return nil
	})
	return st, err
}

func (s *Service) ListCoupons(ctx context.Context, status *string, after *postgres.Cursor, limit int) ([]domain.CouponState, bool, error) {
	var out []domain.CouponState
	err := s.d.DB.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
		cs, err := postgres.New(tx).ListCoupons(ctx, status, after, limit+1)
		for _, c := range cs {
			out = append(out, c.State())
		}
		return err
	})
	if err != nil {
		return nil, false, err
	}
	out, more := page(out, limit)
	return out, more, nil
}

// UpdateCoupon は名前の変更と無効化（inactive への変更だけ）。
func (s *Service) UpdateCoupon(ctx context.Context, id contracts.CouponID, name *string, deactivate bool) (domain.CouponState, error) {
	var st domain.CouponState
	var renamed, disabled bool
	err := s.d.DB.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		c, err := repo.GetCoupon(ctx, id)
		if err != nil {
			return mapNotFound(err, ErrNotFound)
		}
		if name != nil {
			if renamed, err = c.Rename(*name); err != nil {
				return err
			}
		}
		if deactivate {
			disabled = c.Deactivate()
		}
		st = c.State()
		if !renamed && !disabled {
			return nil
		}
		return repo.UpdateCoupon(ctx, c, s.d.Clock.Now())
	})
	if err != nil {
		return domain.CouponState{}, err
	}
	if renamed || disabled {
		st.Version++
	}
	if disabled {
		auditctx.Annotate(ctx, "coupon.disable", target("coupon", id), map[string]any{"status": string(domain.CouponActive)}, map[string]any{"status": string(st.Status)})
	}
	return st, nil
}
