// Package reporting は集計（ダッシュボードの KGI・KPI）のモジュール（console-dashboard 設計）。cmd/** からだけ import する（規約 M7）。
package reporting

import (
	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/messaging"
	sk "subscription-platform/internal/sharedkernel"
	catalog "subscription-platform/modules/catalog/contracts"
	rephttp "subscription-platform/modules/reporting/internal/adapter/http"
	"subscription-platform/modules/reporting/internal/app"
)

// Deps はモジュールの依存（他モジュールは contracts だけ）。
type Deps struct {
	Pool    *db.Pool
	Clock   sk.Clock
	Catalog catalog.Service
}

// Module は reporting モジュール。
type Module struct{ svc *app.Service }

// Name はスキーマ名。
const Name = "reporting"

// ConsumerProjection は reporting-projection の購読者名（deploy/messaging.yaml と同じ）。
const ConsumerProjection = "reporting.projection"

// New はモジュールを組み立てる。
func New(d Deps) *Module {
	return &Module{svc: app.New(app.Deps{DB: d.Pool.Module(Name), Clock: d.Clock, Catalog: d.Catalog})}
}

// MountHTTP は console の API を登録する。
func (m *Module) MountHTTP(r *httpx.Router) error { return rephttp.Register(r, m.svc) }

// Consumers は reporting-projection（worker-projection）。
func (m *Module) Consumers() []messaging.ConsumerSpec {
	return []messaging.ConsumerSpec{{Name: ConsumerProjection, Handler: m.svc.ProjectionHandler(ConsumerProjection)}}
}

// Problem は reporting のエラーを Problem にする。
func Problem(err error) *httpx.Problem { return rephttp.Problem(err) }

// ProjectionEventTypes は購読するイベント（deploy/messaging.yaml の検査に使う）。
func ProjectionEventTypes() []string { return append([]string(nil), app.ProjectionEventTypes...) }
