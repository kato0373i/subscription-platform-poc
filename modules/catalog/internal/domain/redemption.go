package domain

import (
	"time"

	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/catalog/contracts"
)

// RedemptionStatus はクーポンの予約の状態。
type RedemptionStatus string

const (
	RedemptionReserved  RedemptionStatus = "reserved"
	RedemptionConfirmed RedemptionStatus = "confirmed"
	RedemptionReleased  RedemptionStatus = "released"
)

// ReleaseReason は解放の理由。
type ReleaseReason string

const (
	ReleasedByCaller ReleaseReason = "caller"
	ReleasedExpired  ReleaseReason = "expired"
)

// RedemptionState は CouponRedemption の状態。
type RedemptionState struct {
	ID                   contracts.RedemptionID
	TenantID             sk.TenantID
	CouponID             contracts.CouponID
	PlanID               contracts.PlanID
	CustomerAccountID    sk.CustomerAccountID
	SubscriptionID       sk.SubscriptionID
	Status               RedemptionStatus
	ReleaseReason        *ReleaseReason
	OverLimit            bool
	ReservedAt           time.Time
	ReservationExpiresAt time.Time
	ConfirmedAt          *time.Time
	ReleasedAt           *time.Time
	Version              int
}

// Redemption はクーポンの予約（集約ルート）。
type Redemption struct{ s RedemptionState }

func RestoreRedemption(s RedemptionState) *Redemption { return &Redemption{s: s} }

// Reserve は予約を作る（枠の確保はアプリ層の条件付き UPDATE。§9.1）。
func Reserve(id contracts.RedemptionID, c *Coupon, planID contracts.PlanID, customer sk.CustomerAccountID, sub sk.SubscriptionID, ttl time.Duration, now time.Time) *Redemption {
	cs := c.State()
	return &Redemption{s: RedemptionState{
		ID: id, TenantID: cs.TenantID, CouponID: cs.ID, PlanID: planID, CustomerAccountID: customer, SubscriptionID: sub,
		Status: RedemptionReserved, ReservedAt: now, ReservationExpiresAt: now.Add(ttl), Version: 1,
	}}
}

func (r *Redemption) State() RedemptionState { return r.s }

// ConfirmResult は Confirm で枠をどう扱うか。
type ConfirmResult int

const (
	ConfirmNoop     ConfirmResult = iota // すでに confirmed（冪等）
	ConfirmKeepSlot                      // reserved → confirmed（枠は予約時に確保済み）
	ConfirmNeedSlot                      // released(expired) → 枠を取り直してから confirmed（P-CAT-2）
)

// PrepareConfirm は Confirm の可否を判定する（§3.5）。released(caller) は ErrRedemptionReleased。
func (r *Redemption) PrepareConfirm() (ConfirmResult, error) {
	switch r.s.Status {
	case RedemptionConfirmed:
		return ConfirmNoop, nil
	case RedemptionReserved:
		return ConfirmKeepSlot, nil
	case RedemptionReleased:
		if r.s.ReleaseReason != nil && *r.s.ReleaseReason == ReleasedExpired {
			return ConfirmNeedSlot, nil
		}
		return 0, contracts.ErrRedemptionReleased
	}
	return 0, contracts.ErrRedemptionReleased
}

// Confirm は confirmed にする。overLimit は期限切れ後の遅れた確定で枠を取れなかったとき。
func (r *Redemption) Confirm(overLimit bool, now time.Time) {
	r.s.Status, r.s.ConfirmedAt, r.s.OverLimit = RedemptionConfirmed, &now, overLimit
	r.s.ReleaseReason, r.s.ReleasedAt = nil, nil
}

// Release は呼び出し元からの解放。reserved なら true（枠を戻す）。released なら false（冪等）。confirmed は ErrRedemptionAlreadyConfirmed。
func (r *Redemption) Release(now time.Time) (bool, error) {
	switch r.s.Status {
	case RedemptionReserved:
		reason := ReleasedByCaller
		r.s.Status, r.s.ReleaseReason, r.s.ReleasedAt = RedemptionReleased, &reason, &now
		return true, nil
	case RedemptionReleased:
		return false, nil
	case RedemptionConfirmed:
		return false, contracts.ErrRedemptionAlreadyConfirmed
	}
	return false, nil
}

// Expire は掃除ジョブの解放。reserved かつ期限を過ぎていれば true（枠を戻す）。
func (r *Redemption) Expire(now time.Time) bool {
	if r.s.Status != RedemptionReserved || now.Before(r.s.ReservationExpiresAt) {
		return false
	}
	reason := ReleasedExpired
	r.s.Status, r.s.ReleaseReason, r.s.ReleasedAt = RedemptionReleased, &reason, &now
	return true
}
