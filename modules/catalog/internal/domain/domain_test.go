package domain_test

import (
	"errors"
	"testing"
	"time"

	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/catalog/contracts"
	"subscription-platform/modules/catalog/internal/domain"
	"subscription-platform/modules/catalog/internal/domain/pricing"
)

var now = time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)

func plan(t *testing.T, amount int64) *domain.Plan {
	t.Helper()
	p, err := domain.NewPlan(pricing.Default(), domain.PlanInput{
		ID: sk.NewID[contracts.PlanTag](), TenantID: sk.NewID[sk.TenantTag](), ProductID: sk.NewID[contracts.ProductTag](),
		Name: "月額", Interval: sk.IntervalMonth, TrialDays: 14, Amount: amount, PriceID: sk.NewID[contracts.PriceVersionTag](),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// AC-CAT-002: 金額の範囲（D-08）
func TestAmountRange(t *testing.T) {
	for amount, ok := range map[int64]bool{0: false, -1: false, 1_000_001: false, 1: true, 1_000_000: true} {
		_, err := domain.NewPlan(pricing.Default(), domain.PlanInput{
			ID: sk.NewID[contracts.PlanTag](), Name: "p", Interval: sk.IntervalMonth, Amount: amount,
		}, now)
		var ve *domain.ValidationError
		if ok != (err == nil) || (!ok && (!errors.As(err, &ve) || ve.Code != "out_of_range" || ve.Detail == "")) {
			t.Errorf("NewPlan(%d): %v", amount, err)
		}
		p := plan(t, 500)
		_, err = p.AddPriceVersion(pricing.Default(), sk.NewID[contracts.PriceVersionTag](), amount, now)
		if ok != (err == nil) {
			t.Errorf("AddPriceVersion(%d): %v", amount, err)
		}
	}
	p := plan(t, 980)
	if _, err := p.AddPriceVersion(pricing.Default(), sk.NewID[contracts.PriceVersionTag](), 980, now); !errors.Is(err, domain.ErrPriceUnchanged) {
		t.Errorf("同じ金額: %v", err)
	}
	pv, err := p.AddPriceVersion(pricing.Default(), sk.NewID[contracts.PriceVersionTag](), 1280, now)
	if err != nil || pv.VersionNo != 2 || p.State().CurrentPrice.AmountYen() != 1280 || pv.TaxRate != domain.DefaultTaxRate {
		t.Errorf("改定: %+v %v", pv, err)
	}
	if _, err := domain.NewPlan(pricing.Default(), domain.PlanInput{Name: "p", Interval: sk.IntervalMonth, TrialDays: 366, Amount: 1}, now); err == nil {
		t.Error("trial_days 366 を受け付けた")
	}
}

// AC-CAT-005: §3.2 の状態遷移
func TestPlanTransitions(t *testing.T) {
	p := plan(t, 980)
	if _, err := p.Unpublish(); !errors.Is(err, domain.ErrInvalidPlanTransition) {
		t.Errorf("draft の unpublish: %v", err)
	}
	if _, err := p.Publish(false, now); !errors.Is(err, domain.ErrTenantNotActive) {
		t.Errorf("事業者が active でない: %v", err)
	}
	steps := []struct {
		name    string
		op      func() (bool, error)
		changed bool
		want    contracts.PlanStatus
	}{
		{"draft→publish", func() (bool, error) { return p.Publish(true, now) }, true, contracts.PlanPublished},
		{"published→publish", func() (bool, error) { return p.Publish(true, now) }, false, contracts.PlanPublished},
		{"published→unpublish", p.Unpublish, true, contracts.PlanUnpublished},
		{"unpublished→unpublish", p.Unpublish, false, contracts.PlanUnpublished},
		{"unpublished→publish", func() (bool, error) { return p.Publish(true, now) }, true, contracts.PlanPublished},
	}
	for _, s := range steps {
		changed, err := s.op()
		if err != nil || changed != s.changed || p.State().Status != s.want {
			t.Errorf("%s: changed=%v err=%v status=%s", s.name, changed, err, p.State().Status)
		}
	}
}

// AC-CAT-014
func TestFixedQuote(t *testing.T) {
	p := plan(t, 980)
	f := pricing.Fixed{}
	if m, err := f.Quote(p.State().CurrentPrice.Price(), nil); err != nil || m.Int64() != 980 {
		t.Errorf("Quote: %v %v", m, err)
	}
	if _, err := f.Quote(p.State().CurrentPrice.Price(), &contracts.UsageInput{}); !errors.Is(err, contracts.ErrUsageNotSupported) {
		t.Errorf("usage: %v", err)
	}
}

func couponIn(mut func(*domain.CouponInput)) domain.CouponInput {
	in := domain.CouponInput{
		ID: sk.NewID[contracts.CouponTag](), TenantID: sk.NewID[sk.TenantTag](), Code: "welcome", Name: "初回",
		Discount: sk.Discount{Type: sk.DiscountPercent, Value: 50}, DurationCycles: 1, AppliesTo: domain.AllPlans,
	}
	if mut != nil {
		mut(&in)
	}
	return in
}

// AC-CAT-015
func TestCouponValidation(t *testing.T) {
	past := now.Add(-time.Second)
	cases := map[string]func(*domain.CouponInput){
		"percent 0":         func(in *domain.CouponInput) { in.Discount.Value = 0 },
		"percent 101":       func(in *domain.CouponInput) { in.Discount.Value = 101 },
		"amount 0":          func(in *domain.CouponInput) { in.Discount = sk.Discount{Type: sk.DiscountAmount, Value: 0} },
		"duration 0":        func(in *domain.CouponInput) { in.DurationCycles = 0 },
		"duration 25":       func(in *domain.CouponInput) { in.DurationCycles = 25 },
		"selected で空":       func(in *domain.CouponInput) { in.AppliesTo = domain.SelectedPlans },
		"過去の expires_at":    func(in *domain.CouponInput) { in.ExpiresAt = &past },
		"code が短い":          func(in *domain.CouponInput) { in.Code = "ab" },
		"code に記号":          func(in *domain.CouponInput) { in.Code = "WEL COME" },
		"max_redemptions 0": func(in *domain.CouponInput) { z := 0; in.MaxRedemptions = &z },
	}
	for name, mut := range cases {
		if _, err := domain.NewCoupon(couponIn(mut), now); err == nil {
			t.Errorf("%s を受け付けた", name)
		}
	}
	c, err := domain.NewCoupon(couponIn(nil), now)
	if err != nil || c.State().Code != "WELCOME" {
		t.Errorf("正規化: %v %v", c, err)
	}
}

// AC-CAT-018・§9.2: プレビューの計算（D-10）
func TestPreview(t *testing.T) {
	price := sk.MustMoney(980)
	for _, c := range []struct {
		d        sk.Discount
		disc, ok int64
	}{
		{sk.Discount{Type: sk.DiscountPercent, Value: 50}, 490, 490},
		{sk.Discount{Type: sk.DiscountPercent, Value: 33}, 323, 657},
		{sk.Discount{Type: sk.DiscountAmount, Value: 1500}, 980, 0},
		{sk.Discount{Type: sk.DiscountPercent, Value: 100}, 980, 0},
	} {
		cp, err := domain.NewCoupon(couponIn(func(in *domain.CouponInput) { in.Discount = c.d; in.DurationCycles = 3 }), now)
		if err != nil {
			t.Fatal(err)
		}
		pv := cp.PreviewFor(price)
		if pv.DiscountAmount != c.disc || pv.FirstAmount != c.ok || pv.ListAmount != 980 || pv.DiscountedCycles != 3 {
			t.Errorf("%+v: %+v", c.d, pv)
		}
	}
}

// §3.4 の検証の順序
func TestCheckRedeemableOrder(t *testing.T) {
	exp := now.Add(time.Hour)
	one := 1
	other := sk.NewID[contracts.PlanTag]()
	target := sk.NewID[contracts.PlanTag]()
	c, _ := domain.NewCoupon(couponIn(func(in *domain.CouponInput) {
		in.AppliesTo, in.PlanIDs, in.ExpiresAt, in.MaxRedemptions = domain.SelectedPlans, []contracts.PlanID{target}, &exp, &one
	}), now)
	st := c.State()
	st.RedeemedCount = 1
	c = domain.RestoreCoupon(st)
	if err := c.CheckRedeemable(target, false, now); !errors.Is(err, contracts.ErrCouponExhausted) {
		t.Errorf("#5: %v", err)
	}
	if err := c.CheckRedeemable(target, true, now); !errors.Is(err, contracts.ErrCouponAlreadyUsed) {
		t.Errorf("#4 は #5 より先: %v", err)
	}
	if err := c.CheckRedeemable(other, true, now); !errors.Is(err, contracts.ErrCouponNotApplicable) {
		t.Errorf("#3 は #4 より先: %v", err)
	}
	if err := c.CheckRedeemable(other, true, exp); !errors.Is(err, contracts.ErrCouponExpired) {
		t.Errorf("#2（ちょうどの時刻も期限切れ）: %v", err)
	}
	c.Deactivate()
	if err := c.CheckRedeemable(other, true, exp); !errors.Is(err, contracts.ErrCouponNotFound) {
		t.Errorf("#1: %v", err)
	}
}

// AC-CAT-026: §3.5 の状態遷移
func TestRedemptionTransitions(t *testing.T) {
	c, _ := domain.NewCoupon(couponIn(nil), now)
	reserve := func() *domain.Redemption {
		return domain.Reserve(sk.NewID[contracts.RedemptionTag](), c, sk.NewID[contracts.PlanTag](), sk.NewID[sk.CustomerAccountTag](), sk.NewID[sk.SubscriptionTag](), 24*time.Hour, now)
	}
	r := reserve()
	if res, err := r.PrepareConfirm(); res != domain.ConfirmKeepSlot || err != nil {
		t.Errorf("reserved の Confirm: %v %v", res, err)
	}
	r.Confirm(false, now)
	if res, err := r.PrepareConfirm(); res != domain.ConfirmNoop || err != nil {
		t.Errorf("confirmed の Confirm（冪等）: %v %v", res, err)
	}
	if _, err := r.Release(now); !errors.Is(err, contracts.ErrRedemptionAlreadyConfirmed) {
		t.Errorf("confirmed の Release: %v", err)
	}
	if r.Expire(now.Add(48 * time.Hour)) {
		t.Error("confirmed は掃除の対象外")
	}

	r = reserve()
	if released, err := r.Release(now); !released || err != nil {
		t.Errorf("reserved の Release: %v %v", released, err)
	}
	if released, err := r.Release(now); released || err != nil {
		t.Errorf("released の Release（冪等）: %v %v", released, err)
	}
	if _, err := r.PrepareConfirm(); !errors.Is(err, contracts.ErrRedemptionReleased) {
		t.Errorf("released(caller) の Confirm: %v", err)
	}

	r = reserve()
	if r.Expire(now.Add(24*time.Hour - time.Second)) {
		t.Error("期限前に解放した")
	}
	if !r.Expire(now.Add(24 * time.Hour)) {
		t.Error("期限で解放されない")
	}
	if res, err := r.PrepareConfirm(); res != domain.ConfirmNeedSlot || err != nil {
		t.Errorf("released(expired) の Confirm: %v %v", res, err)
	}
	r.Confirm(true, now)
	if st := r.State(); st.Status != domain.RedemptionConfirmed || !st.OverLimit || st.ReleaseReason != nil || st.ReleasedAt != nil {
		t.Errorf("遅れた確定: %+v", st)
	}
}
