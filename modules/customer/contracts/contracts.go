// Package contracts は customer モジュールが他のモジュールと cmd に公開する部分（identity.md §4.2）。
// 標準ライブラリと sharedkernel だけに依存する。
package contracts

import (
	"context"
	"errors"
	"time"

	sk "subscription-platform/internal/sharedkernel"
)

// TenantCustomerID は事業者ごとの契約者 ID（"cus_" + base62 24 文字）。事業者に渡す唯一の契約者識別子。
type TenantCustomerID string

// AccountStatus は契約者アカウントの状態。
type AccountStatus string

const (
	AccountActive    AccountStatus = "active"
	AccountWithdrawn AccountStatus = "withdrawn"
)

// Contact はメールの宛先。Name は "" のことがある。
type Contact struct{ Email, Name string }

// AuthIdentity は Authenticator が検証した consumer のクレーム。
type AuthIdentity struct {
	Subject       string
	Email         string
	EmailVerified *bool // クレームがなければ nil
	Name          string
}

// AccountView は契約者自身に見せるアカウント。
type AccountView struct {
	ID        sk.CustomerAccountID
	Email     string
	Name      string
	Status    AccountStatus
	CreatedAt time.Time
}

// TenantCustomerProfile は事業者に渡してよい項目だけ（FR-009）。
type TenantCustomerProfile struct {
	TenantCustomerID TenantCustomerID
	Email, Name      string // 退会済みなら ""
	Withdrawn        bool
}

// Service は customer モジュールの公開 API。
type Service interface {
	// EnsureTenantCustomerID はリンクがあれば返し、なければ作る。withdrawn なら ErrAccountWithdrawn、アカウントがなければ ErrAccountNotFound。
	EnsureTenantCustomerID(ctx context.Context, tenantID sk.TenantID, id sk.CustomerAccountID) (TenantCustomerID, error)
	// ResolveTenantCustomerID は作らない。なければ ErrLinkNotFound。withdrawn でも返す（退会前の取引のイベント変換に使う）。
	ResolveTenantCustomerID(ctx context.Context, tenantID sk.TenantID, id sk.CustomerAccountID) (TenantCustomerID, error)
	// GetContact は withdrawn なら ErrAccountWithdrawn（メールを送らせない）。
	GetContact(ctx context.Context, id sk.CustomerAccountID) (Contact, error)
	// Withdraw は退会させる（可否の判定は Orchestration）。withdrawn なら nil。
	Withdraw(ctx context.Context, id sk.CustomerAccountID) error
	// ResolveOrCreateAccount は初回ログイン時のアカウント作成（JIT。§7.3）。platform/httpx から cmd 経由で呼ばれる。
	ResolveOrCreateAccount(ctx context.Context, in AuthIdentity) (AccountView, error)
	// GetTenantCustomerProfile はそのテナントとのリンクがなければ ErrLinkNotFound（他テナントの契約者の個人情報を返さない）。
	GetTenantCustomerProfile(ctx context.Context, tenantID sk.TenantID, id sk.CustomerAccountID) (TenantCustomerProfile, error)
}

var (
	ErrAccountNotFound        = errors.New("customer: account not found")
	ErrAccountWithdrawn       = errors.New("customer: account withdrawn")
	ErrLinkNotFound           = errors.New("customer: tenant customer link not found")
	ErrEmailAlreadyRegistered = errors.New("customer: email already registered")
	ErrEmailUnverified        = errors.New("customer: email missing or unverified")
)

// EventCustomerWithdrawn は退会のイベント（source = sp.customer。envelope の tenant_id は null）。
const EventCustomerWithdrawn = "customer.withdrawn"

// CustomerWithdrawnPayload は customer.withdrawn の payload。
type CustomerWithdrawnPayload struct {
	CustomerAccountID sk.CustomerAccountID `json:"customer_account_id"`
	WithdrawnAt       time.Time            `json:"withdrawn_at"`
}
