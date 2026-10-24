// Package catalog は商品・プラン・価格・クーポンのモジュール（catalog.md）。cmd/** からだけ import する（規約 M7）。
package catalog

import (
	"time"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/outbox"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/catalog/contracts"
	cataloghttp "subscription-platform/modules/catalog/internal/adapter/http"
	"subscription-platform/modules/catalog/internal/app"
	"subscription-platform/modules/catalog/internal/domain/pricing"
	tenantcontracts "subscription-platform/modules/tenant/contracts"
)

// Deps はモジュールの依存。Tenants は本物の tenant モジュールの contracts（P-CAT-1）。
type Deps struct {
	Pool           *db.Pool
	Clock          sk.Clock
	Outbox         []outbox.WriterOption
	Cursors        *httpx.CursorCodec
	Tenants        tenantcontracts.Service
	ReservationTTL time.Duration
}

// Module は catalog モジュール。
type Module struct {
	svc     *app.Service
	cursors *httpx.CursorCodec
}

// Name はスキーマ名。
const Name = "catalog"

// New はモジュールを組み立てる。従量課金などの算出方式は pricing.Default() に足す（§10.1）。
func New(d Deps) *Module {
	svc := app.New(app.Deps{
		DB: d.Pool.Module(Name), Outbox: outbox.NewWriter(Name, d.Clock, d.Outbox...), Clock: d.Clock,
		Schemes: pricing.Default(), Tenants: d.Tenants, ReservationTTL: d.ReservationTTL,
	})
	return &Module{svc: svc, cursors: d.Cursors}
}

// Service は他モジュールと cmd に公開する API。
func (m *Module) Service() contracts.Service { return m.svc }

// MountHTTP は console・checkout のルートを登録する。
func (m *Module) MountHTTP(r *httpx.Router) error { return cataloghttp.Register(r, m.svc, m.cursors) }

// Jobs は放置された予約の掃除ジョブ。
func (m *Module) Jobs() []scheduler.Job { return []scheduler.Job{m.svc.SweepJob()} }

// Problem は catalog のエラーを Problem にする（申込 API の配線で使う）。対応がなければ nil。
func Problem(err error) *httpx.Problem { return cataloghttp.Problem(err) }
