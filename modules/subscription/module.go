// Package subscription は契約のモジュール（subscription.md）。cmd/** からだけ import する（規約 M7）。
package subscription

import (
	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/outbox"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
	catalogcontracts "subscription-platform/modules/catalog/contracts"
	"subscription-platform/modules/subscription/contracts"
	subhttp "subscription-platform/modules/subscription/internal/adapter/http"
	"subscription-platform/modules/subscription/internal/app"
)

// Deps はモジュールの依存。Catalog はプラン変更先の検証だけに使う（ベースラインで許された「ID の検証のみ」）。
type Deps struct {
	Pool    *db.Pool
	Clock   sk.Clock
	Outbox  []outbox.WriterOption
	Cursors *httpx.CursorCodec
	Catalog catalogcontracts.Service
}

// Module は subscription モジュール。
type Module struct {
	svc     *app.Service
	cursors *httpx.CursorCodec
}

// Name はスキーマ名。
const Name = "subscription"

func New(d Deps) *Module {
	svc := app.New(app.Deps{DB: d.Pool.Module(Name), Outbox: outbox.NewWriter(Name, d.Clock, d.Outbox...), Clock: d.Clock, Catalog: d.Catalog})
	return &Module{svc: svc, cursors: d.Cursors}
}

// Service は他モジュールと cmd に公開する API。
func (m *Module) Service() contracts.Service { return m.svc }

// MountHTTP は me・console・tenant のルートを登録する。
func (m *Module) MountHTTP(r *httpx.Router) error { return subhttp.Register(r, m.svc, m.cursors) }

// Jobs はトライアル終了の予告ジョブ（P-SUB-6）。
func (m *Module) Jobs() []scheduler.Job { return []scheduler.Job{m.svc.TrialWillEndJob()} }

// Problem は subscription のエラーを Problem にする（申込 API の配線で使う）。対応がなければ nil。
func Problem(err error) *httpx.Problem { return subhttp.Problem(err) }
