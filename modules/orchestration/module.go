// Package orchestration は課金サイクルの順番を持つプロセスマネージャー（orchestration.md、ADR-002）。cmd/** からだけ import する（規約 M7）。
package orchestration

import (
	"context"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/inbox"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
	billing "subscription-platform/modules/billing/contracts"
	catalog "subscription-platform/modules/catalog/contracts"
	customer "subscription-platform/modules/customer/contracts"
	orchttp "subscription-platform/modules/orchestration/internal/adapter/http"
	"subscription-platform/modules/orchestration/internal/app"
	payments "subscription-platform/modules/payments/contracts"
	subscription "subscription-platform/modules/subscription/contracts"
	tenant "subscription-platform/modules/tenant/contracts"
)

// Deps はモジュールの依存（他モジュールは contracts だけ）。
type Deps struct {
	Pool         *db.Pool
	Clock        sk.Clock
	Tenant       tenant.Service
	Customer     customer.Service
	Catalog      catalog.Service
	Subscription subscription.Service
	Billing      billing.Service
	Payments     payments.Service
	SendRenewal  func(ctx context.Context, msgs []messaging.FIFOMessage) error
	EnqueueJob   func(ctx context.Context, job string, asOf sk.Date, params []byte) (uuid.UUID, error)
}

// Module は orchestration モジュール。
type Module struct {
	svc *app.Service
	db  *db.ModuleDB
}

// Name はスキーマ名。
const Name = "orchestration"

// 購読者の名前（deploy/messaging.yaml と同じ）。
const (
	ConsumerEvents     = "orchestration.events"
	ConsumerCollection = "orchestration.collection"
	ConsumerRenewal    = "orchestration.renewal"
)

// New はモジュールを組み立てる。
func New(d Deps) *Module {
	mdb := d.Pool.Module(Name)
	return &Module{db: mdb, svc: app.New(app.Deps{
		DB: mdb, Clock: d.Clock, Tenant: d.Tenant, Customer: d.Customer, Catalog: d.Catalog, Subscription: d.Subscription, Billing: d.Billing,
		Payments: d.Payments, SendRenewal: d.SendRenewal, EnqueueJob: d.EnqueueJob,
	})}
}

// MountHTTP は me・ops の API を登録する。
func (m *Module) MountHTTP(r *httpx.Router) error { return orchttp.Register(r, m.svc) }

// Consumers は orc-events・orc-collection・orc-renewal.fifo（worker-billing）。他モジュールの呼び出しは別トランザクションなので、
// Inbox は「処理の後に記録」する（§5 の共通の手順）。
func (m *Module) Consumers() []messaging.ConsumerSpec {
	return []messaging.ConsumerSpec{
		{Name: ConsumerEvents, Handler: inbox.Guard(m.db, ConsumerEvents, m.svc.HandleEvent)},
		{Name: ConsumerCollection, Handler: inbox.Guard(m.db, ConsumerCollection, m.svc.HandleCollectionDue)},
		{Name: ConsumerRenewal, Handler: inbox.Guard(m.db, ConsumerRenewal, m.svc.HandleRenewal)},
	}
}

// Jobs は更新の投入・再開・一括解約・サガの掃除（worker-billing）。
func (m *Module) Jobs() []scheduler.Job {
	return []scheduler.Job{m.svc.RenewalDispatchJob(), m.svc.SweeperJob(), m.svc.BulkCancellationJob(), m.svc.SagaGCJob()}
}

// Problem は Orchestration のエラーを Problem にする。
func Problem(err error) *httpx.Problem { return orchttp.Problem(err) }
