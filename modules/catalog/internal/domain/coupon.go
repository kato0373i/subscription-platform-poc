package domain

import (
	"regexp"
	"strings"
	"time"

	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/catalog/contracts"
)

// AppliesTo はクーポンの対象。
type AppliesTo string

const (
	AllPlans      AppliesTo = "all_plans"
	SelectedPlans AppliesTo = "selected_plans"
)

// CouponStatus はクーポンの状態（inactive は戻せない）。
type CouponStatus string

const (
	CouponActive   CouponStatus = "active"
	CouponInactive CouponStatus = "inactive"
)

var couponCodePattern = regexp.MustCompile(`^[A-Z0-9_-]{3,32}$`)

// NormalizeCode は大文字にする（事業者内で一意。AC-CAT-016）。
func NormalizeCode(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }

// CouponState は Coupon の状態。
type CouponState struct {
	ID             contracts.CouponID
	TenantID       sk.TenantID
	Code           string
	Name           string
	Discount       sk.Discount
	DurationCycles int
	AppliesTo      AppliesTo
	PlanIDs        []contracts.PlanID
	ExpiresAt      *time.Time
	MaxRedemptions *int
	RedeemedCount  int
	Status         CouponStatus
	Version        int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Coupon はクーポン（集約ルート）。
type Coupon struct{ s CouponState }

func RestoreCoupon(s CouponState) *Coupon { return &Coupon{s: s} }

// CouponInput はクーポンの作成の入力。
type CouponInput struct {
	ID             contracts.CouponID
	TenantID       sk.TenantID
	Code           string
	Name           string
	Discount       sk.Discount
	DurationCycles int
	AppliesTo      AppliesTo
	PlanIDs        []contracts.PlanID
	ExpiresAt      *time.Time
	MaxRedemptions *int
}

// NewCoupon はクーポンを作る（AC-CAT-015）。plan_ids が同じ事業者のものかは、アプリ層が RLS の下で確かめる。
func NewCoupon(in CouponInput, now time.Time) (*Coupon, error) {
	code := NormalizeCode(in.Code)
	if !couponCodePattern.MatchString(code) {
		return nil, invalid("code", "invalid")
	}
	name, err := text("name", in.Name, 1, 100)
	if err != nil {
		return nil, err
	}
	d := in.Discount
	switch d.Type {
	case sk.DiscountAmount:
		if d.Value < 1 || d.Value > 1_000_000 {
			return nil, invalid("discount.value", "out_of_range")
		}
	case sk.DiscountPercent:
		if d.Value < 1 || d.Value > 100 {
			return nil, invalid("discount.value", "out_of_range")
		}
	default:
		return nil, invalid("discount.type", "invalid")
	}
	if in.DurationCycles < 1 || in.DurationCycles > 24 {
		return nil, invalid("discount.duration_cycles", "out_of_range")
	}
	switch in.AppliesTo {
	case AllPlans:
		if len(in.PlanIDs) > 0 {
			return nil, invalid("plan_ids", "not_allowed")
		}
	case SelectedPlans:
		if len(in.PlanIDs) == 0 {
			return nil, invalid("plan_ids", "required")
		}
	default:
		return nil, invalid("applies_to", "invalid")
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(now) {
		return nil, invalid("expires_at", "in_the_past")
	}
	if in.MaxRedemptions != nil && (*in.MaxRedemptions < 1 || *in.MaxRedemptions > 1_000_000) {
		return nil, invalid("max_redemptions", "out_of_range")
	}
	ids := dedupe(in.PlanIDs)
	return &Coupon{s: CouponState{
		ID: in.ID, TenantID: in.TenantID, Code: code, Name: name, Discount: d, DurationCycles: in.DurationCycles,
		AppliesTo: in.AppliesTo, PlanIDs: ids, ExpiresAt: in.ExpiresAt, MaxRedemptions: in.MaxRedemptions,
		Status: CouponActive, Version: 1, CreatedAt: now, UpdatedAt: now,
	}}, nil
}

func dedupe(ids []contracts.PlanID) []contracts.PlanID {
	seen := map[contracts.PlanID]bool{}
	var out []contracts.PlanID
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func (c *Coupon) State() CouponState { return c.s }

// Rename は管理用の名前を変える。
func (c *Coupon) Rename(name string) (bool, error) {
	n, err := text("name", name, 1, 100)
	if err != nil {
		return false, err
	}
	changed := n != c.s.Name
	c.s.Name = n
	return changed, nil
}

// Deactivate は active → inactive（冪等）。既存の予約・確定には影響しない。
func (c *Coupon) Deactivate() bool {
	if c.s.Status == CouponInactive {
		return false
	}
	c.s.Status = CouponInactive
	return true
}

// Expired は now >= expiresAt か（ちょうどの時刻も期限切れ）。
func (c *Coupon) Expired(now time.Time) bool {
	return c.s.ExpiresAt != nil && !now.Before(*c.s.ExpiresAt)
}

// AppliesToPlan はプランが対象か。
func (c *Coupon) AppliesToPlan(planID contracts.PlanID) bool {
	if c.s.AppliesTo == AllPlans {
		return true
	}
	for _, id := range c.s.PlanIDs {
		if id == planID {
			return true
		}
	}
	return false
}

// Exhausted は消費枠数が上限に達しているか。
func (c *Coupon) Exhausted() bool {
	return c.s.MaxRedemptions != nil && c.s.RedeemedCount >= *c.s.MaxRedemptions
}

// CheckRedeemable は検証 #1〜#3・#5 を順に行う（#4 の使用済みは DB を見るアプリ層が #3 と #5 の間で行う。§3.4）。
// alreadyUsed は #4 の結果（判定しないときは false）。
func (c *Coupon) CheckRedeemable(planID contracts.PlanID, alreadyUsed bool, now time.Time) error {
	switch {
	case c.s.Status != CouponActive:
		return contracts.ErrCouponNotFound
	case c.Expired(now):
		return contracts.ErrCouponExpired
	case !c.AppliesToPlan(planID):
		return contracts.ErrCouponNotApplicable
	case alreadyUsed:
		return contracts.ErrCouponAlreadyUsed
	case c.Exhausted():
		return contracts.ErrCouponExhausted
	}
	return nil
}

// Preview は申込ページに見せる割引の参考値（確定額は Billing。§7.5）。
type Preview struct {
	ListAmount       int64
	DiscountAmount   int64
	FirstAmount      int64
	DiscountedCycles int
}

// PreviewFor は価格に割引を当てた参考値（D-10 の式は sharedkernel.Discount.Apply だけ。P-CAT-5）。
func (c *Coupon) PreviewFor(price sk.Money) Preview {
	disc, net := c.s.Discount.Apply(price)
	return Preview{ListAmount: price.Int64(), DiscountAmount: disc.Int64(), FirstAmount: net.Int64(), DiscountedCycles: c.s.DurationCycles}
}
