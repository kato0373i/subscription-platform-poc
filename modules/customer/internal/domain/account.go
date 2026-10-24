// Package domain は customer モジュールの集約（CustomerAccount・TenantCustomerLink。identity.md §3.5・§3.6）。
package domain

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/customer/contracts"
)

// ErrInvalidIdentity は認証済みのクレームが形式に合わない（subject の長さなど）。
var ErrInvalidIdentity = errors.New("customer: invalid identity")

// NormalizeEmail は小文字にし、最大 254 文字・@ を含むことを確かめる。合わなければ "", false。
func NormalizeEmail(s string) (string, bool) {
	e := strings.ToLower(strings.TrimSpace(s))
	if e == "" || utf8.RuneCountInString(e) > 254 || !strings.Contains(e, "@") || strings.HasPrefix(e, "@") || strings.HasSuffix(e, "@") {
		return "", false
	}
	return e, true
}

// normalizeName は name クレームを最大 100 文字に切り詰める（空なら nil）。
func normalizeName(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if utf8.RuneCountInString(s) > 100 {
		s = string([]rune(s)[:100])
	}
	return &s
}

// AccountState は CustomerAccount の永続化される状態。
type AccountState struct {
	ID          sk.CustomerAccountID
	Subject     string
	Email       *string
	Name        *string
	Status      contracts.AccountStatus
	WithdrawnAt *time.Time
	Version     int
	CreatedAt   time.Time
}

// Account は契約者のアカウント（集約ルート）。
type Account struct{ s AccountState }

// RestoreAccount は保存済みの状態から復元する。
func RestoreAccount(s AccountState) *Account { return &Account{s: s} }

// VerifiedEmail は J3 の判定：email クレームがあり、email_verified が false でなければ正規化した値を返す。
func VerifiedEmail(in contracts.AuthIdentity) (string, bool) {
	if in.EmailVerified != nil && !*in.EmailVerified {
		return "", false
	}
	return NormalizeEmail(in.Email)
}

// ProvisionAccount は初回ログインで active のアカウントを作る（J5）。email は検証済みであること。
func ProvisionAccount(id sk.CustomerAccountID, in contracts.AuthIdentity, now time.Time) (*Account, error) {
	sub := in.Subject
	if sub == "" || utf8.RuneCountInString(sub) > 255 {
		return nil, ErrInvalidIdentity
	}
	email, ok := VerifiedEmail(in)
	if !ok {
		return nil, contracts.ErrEmailUnverified
	}
	return &Account{s: AccountState{ID: id, Subject: sub, Email: &email, Name: normalizeName(in.Name), Status: contracts.AccountActive, Version: 1, CreatedAt: now}}, nil
}

func (a *Account) State() AccountState             { return a.s }
func (a *Account) ID() sk.CustomerAccountID        { return a.s.ID }
func (a *Account) Status() contracts.AccountStatus { return a.s.Status }

// SyncProfile はクレームの email（検証済みのとき）・name が変わっていれば更新する（J1）。
// 戻り値は email・name それぞれが変わったか。withdrawn なら ErrAccountWithdrawn。
func (a *Account) SyncProfile(in contracts.AuthIdentity) (emailChanged, nameChanged bool, err error) {
	if a.s.Status == contracts.AccountWithdrawn {
		return false, false, contracts.ErrAccountWithdrawn
	}
	if e, ok := VerifiedEmail(in); ok && (a.s.Email == nil || *a.s.Email != e) {
		a.s.Email = &e
		emailChanged = true
	}
	if n := normalizeName(in.Name); n != nil && (a.s.Name == nil || *a.s.Name != *n) {
		a.s.Name = n
		nameChanged = true
	}
	return emailChanged, nameChanged, nil
}

// RevertEmail は email の更新をやめる（他の active アカウントと衝突したとき。J1）。
func (a *Account) RevertEmail(prev *string) { a.s.Email = prev }

// Withdraw は退会させ、氏名・メールアドレスを消す（NFR-015）。withdrawn なら false（何もしない）。
func (a *Account) Withdraw(now time.Time) bool {
	if a.s.Status == contracts.AccountWithdrawn {
		return false
	}
	a.s.Status, a.s.Email, a.s.Name, a.s.WithdrawnAt = contracts.AccountWithdrawn, nil, nil, &now
	return true
}

// Contact はメールの宛先。withdrawn なら ErrAccountWithdrawn。
func (a *Account) Contact() (contracts.Contact, error) {
	if a.s.Status == contracts.AccountWithdrawn || a.s.Email == nil {
		return contracts.Contact{}, contracts.ErrAccountWithdrawn
	}
	c := contracts.Contact{Email: *a.s.Email}
	if a.s.Name != nil {
		c.Name = *a.s.Name
	}
	return c, nil
}

// View は契約者自身に見せる形。
func (a *Account) View() contracts.AccountView {
	v := contracts.AccountView{ID: a.s.ID, Status: a.s.Status, CreatedAt: a.s.CreatedAt}
	if a.s.Email != nil {
		v.Email = *a.s.Email
	}
	if a.s.Name != nil {
		v.Name = *a.s.Name
	}
	return v
}

// NewTenantCustomerID は "cus_" + base62 24 文字（crypto/rand、約 143 ビット）。
func NewTenantCustomerID() contracts.TenantCustomerID {
	return contracts.TenantCustomerID(sk.NewOpaqueID("cus", 24))
}
