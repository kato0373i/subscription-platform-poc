// Package customer は契約者アカウントと事業者ごとの契約者 ID のモジュール（identity.md）。cmd/** からだけ import する（規約 M7）。
package customer

import (
	"context"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/outbox"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/customer/contracts"
	customerhttp "subscription-platform/modules/customer/internal/adapter/http"
	"subscription-platform/modules/customer/internal/app"
)

// Deps はモジュールの依存。
type Deps struct {
	Pool   *db.Pool
	Clock  sk.Clock
	Outbox []outbox.WriterOption
}

// Module は customer モジュール。
type Module struct{ svc *app.Service }

// Name はスキーマ名。
const Name = "customer"

// New はモジュールを組み立てる。
func New(d Deps) *Module {
	return &Module{svc: app.New(d.Pool.Module(Name), outbox.NewWriter(Name, d.Clock, d.Outbox...), d.Clock)}
}

// Service は他モジュールと cmd に公開する API。
func (m *Module) Service() contracts.Service { return m.svc }

// MountHTTP は /v1/me のルートを登録する。
func (m *Module) MountHTTP(r *httpx.Router) error { return customerhttp.Register(r, m.svc) }

// ResolveConsumer は consumer 認証の直後に呼ぶ JIT（§7.3）。エラーは Problem（account_withdrawn など）にして返す。
func (m *Module) ResolveConsumer(ctx context.Context, c httpx.Claims) (sk.CustomerAccountID, error) {
	v, err := m.svc.ResolveOrCreateAccount(ctx, contracts.AuthIdentity{Subject: c.Subject, Email: c.Email, EmailVerified: c.EmailVerified, Name: c.Name})
	if err != nil {
		if p := customerhttp.Problem(err); p != nil {
			return sk.CustomerAccountID{}, p
		}
		return sk.CustomerAccountID{}, err
	}
	return v.ID, nil
}
