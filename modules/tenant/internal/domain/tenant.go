// Package domain は tenant モジュールの集約（Tenant・StaffMember・ApiKey）と不変条件（identity.md §3）。
// 標準ライブラリ・sharedkernel・自モジュールの contracts だけに依存する。
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/tenant/contracts"
)

var (
	ErrTenantStateConflict = errors.New("tenant: state conflict")
	ErrTenantNotWritable   = errors.New("tenant: suspended")
	ErrLastOwner           = errors.New("tenant: last owner required")
	ErrAPIKeyLimit         = errors.New("tenant: api key limit exceeded")
)

// ValidationError は入力の誤り（HTTP では validation_failed）。
type ValidationError struct{ Field, Code string }

func (e *ValidationError) Error() string {
	return fmt.Sprintf("tenant: invalid %s (%s)", e.Field, e.Code)
}

func invalid(field, code string) error { return &ValidationError{Field: field, Code: code} }

// textInRange は前後の空白を除いて 1〜max 文字か確かめる。
func textInRange(field, s string, max int) (string, error) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	switch {
	case n == 0:
		return "", invalid(field, "required")
	case n > max:
		return "", invalid(field, "too_long")
	}
	return s, nil
}

var shopIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// TenantState は Tenant の永続化される状態（adapter が読み書きする）。
type TenantState struct {
	ID            sk.TenantID
	Name          string
	Status        contracts.TenantStatus
	FincodeShopID *string
	ApprovedAt    *time.Time
	SuspendedAt   *time.Time
	SuspendReason *string
	Version       int
	CreatedAt     time.Time
}

// Tenant は事業者（集約ルート）。
type Tenant struct{ s TenantState }

// RestoreTenant は保存済みの状態から復元する。
func RestoreTenant(s TenantState) *Tenant { return &Tenant{s: s} }

// RegisterTenant は pending_review の事業者を作る。
func RegisterTenant(id sk.TenantID, name string, now time.Time) (*Tenant, error) {
	n, err := textInRange("name", name, 100)
	if err != nil {
		return nil, err
	}
	return &Tenant{s: TenantState{ID: id, Name: n, Status: contracts.TenantPendingReview, Version: 1, CreatedAt: now}}, nil
}

// State は現在の状態（コピー）。
func (t *Tenant) State() TenantState { return t.s }

func (t *Tenant) ID() sk.TenantID                { return t.s.ID }
func (t *Tenant) Status() contracts.TenantStatus { return t.s.Status }

// CanSell は販売できるか（active のときだけ）。
func (t *Tenant) CanSell() bool { return t.s.Status == contracts.TenantActive }

// AllowsStaffWrite は書き込み系の console 操作ができるか（suspended 以外）。
func (t *Tenant) AllowsStaffWrite() bool { return t.s.Status != contracts.TenantSuspended }

// Approved は Approve で状態が変わったときのイベント。
type Approved struct {
	ShopID string
	At     time.Time
}

// Suspended は Suspend で状態が変わったときのイベント。
type Suspended struct {
	Reason string
	At     time.Time
}

// Approve は pending_review → active。同じ shopID での再実行は何もしない（nil を返す）。
func (t *Tenant) Approve(shopID string, now time.Time) (*Approved, error) {
	if !shopIDPattern.MatchString(shopID) {
		return nil, invalid("fincode_shop_id", "invalid")
	}
	switch t.s.Status {
	case contracts.TenantPendingReview:
		t.s.Status = contracts.TenantActive
		t.s.FincodeShopID = &shopID
		t.s.ApprovedAt = &now
		return &Approved{ShopID: shopID, At: now}, nil
	case contracts.TenantActive:
		if t.s.FincodeShopID != nil && *t.s.FincodeShopID == shopID {
			return nil, nil
		}
		return nil, ErrTenantStateConflict
	case contracts.TenantSuspended:
		return nil, ErrTenantStateConflict
	}
	return nil, ErrTenantStateConflict
}

// Suspend は active → suspended。suspended への再実行は何もしない（reason は上書きしない）。
func (t *Tenant) Suspend(reason string, now time.Time) (*Suspended, error) {
	r, err := textInRange("reason", reason, 500)
	if err != nil {
		return nil, err
	}
	switch t.s.Status {
	case contracts.TenantActive:
		t.s.Status = contracts.TenantSuspended
		t.s.SuspendedAt = &now
		t.s.SuspendReason = &r
		return &Suspended{Reason: r, At: now}, nil
	case contracts.TenantSuspended:
		return nil, nil
	case contracts.TenantPendingReview:
		return nil, ErrTenantStateConflict
	}
	return nil, ErrTenantStateConflict
}
