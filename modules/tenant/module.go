// Package tenant は事業者・スタッフ・API キーのモジュール（identity.md）。cmd/** からだけ import する（規約 M7）。
package tenant

import (
	"crypto/rand"
	"io"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/outbox"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/tenant/contracts"
	tenanthttp "subscription-platform/modules/tenant/internal/adapter/http"
	"subscription-platform/modules/tenant/internal/adapter/identity"
	"subscription-platform/modules/tenant/internal/app"
)

// Deps はモジュールの依存。
type Deps struct {
	Pool    *db.Pool
	Clock   sk.Clock
	Outbox  []outbox.WriterOption
	Cursors *httpx.CursorCodec
	Rand    io.Reader // 省略時は crypto/rand
	// Identity はスタッフの認証基盤（Cognito）。nil なら開発用（メールを送らず、subject はメールアドレスから決める）
	Identity IdentityProvider
	// AllowDirectSubject は ops のスタッフ作成で subject の直接指定を受け付けるか（devtools ビルドだけ true）
	AllowDirectSubject bool
}

// IdentityProvider はスタッフのアカウントの操作（cmd が Cognito の実装を渡す）。
type IdentityProvider = identity.Provider

// CognitoIdentity は Cognito のスタッフのプールの実装。
type CognitoIdentity = identity.Cognito

// Module は tenant モジュール。
type Module struct {
	svc          *app.Service
	cursors      *httpx.CursorCodec
	allowSubject bool
}

// Name はスキーマ名。
const Name = "tenant"

// New はモジュールを組み立てる。
func New(d Deps) *Module {
	r := d.Rand
	if r == nil {
		r = rand.Reader
	}
	svc := app.New(d.Pool.Module(Name), outbox.NewWriter(Name, d.Clock, d.Outbox...), d.Clock, r, d.Identity)
	return &Module{svc: svc, cursors: d.Cursors, allowSubject: d.AllowDirectSubject}
}

// Service は他モジュールと cmd に公開する API。
func (m *Module) Service() contracts.Service { return m.svc }

// MountHTTP は ops・console のルートを登録する。
func (m *Module) MountHTTP(r *httpx.Router) error {
	return tenanthttp.Register(r, m.svc, m.cursors, tenanthttp.Options{AllowDirectSubject: m.allowSubject})
}
