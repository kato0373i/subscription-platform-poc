// Package billing は請求とダニングのモジュール（billing.md）。cmd/** からだけ import する（規約 M7）。
package billing

import (
	"context"
	"time"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/inbox"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/outbox"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/billing/contracts"
	bilhttp "subscription-platform/modules/billing/internal/adapter/http"
	"subscription-platform/modules/billing/internal/app"
)

// Deps はモジュールの依存。
type Deps struct {
	Pool    *db.Pool
	Clock   sk.Clock
	Outbox  []outbox.WriterOption
	Cursors *httpx.CursorCodec
	// Lease は回収要求のリース（BILLING_COLLECTION_LEASE）。
	Lease time.Duration
}

// Module は billing モジュール。
type Module struct {
	svc     *app.Service
	db      *db.ModuleDB
	cursors *httpx.CursorCodec
}

// Name はスキーマ名。
const Name = "billing"

// ConsumerPaymentOutcome は payments.* の購読者名（deploy/messaging.yaml と同じ。§5）。
const ConsumerPaymentOutcome = "billing.payment_outcome"

// New はモジュールを組み立てる。
func New(d Deps) *Module {
	mdb := d.Pool.Module(Name)
	return &Module{
		db: mdb, cursors: d.Cursors,
		svc: app.New(app.Deps{DB: mdb, Outbox: outbox.NewWriter(Name, d.Clock, d.Outbox...), Clock: d.Clock, Lease: d.Lease}),
	}
}

// Service は他モジュールと cmd に公開する API。
func (m *Module) Service() contracts.Service { return m.svc }

// MountHTTP は me・console・ops の API を登録する。
func (m *Module) MountHTTP(r *httpx.Router) error { return bilhttp.Register(r, m.svc, m.cursors) }

// Consumers は決済の結果の購読（worker-billing）。
func (m *Module) Consumers() []messaging.ConsumerSpec {
	return []messaging.ConsumerSpec{
		{Name: ConsumerPaymentOutcome, Handler: inbox.Wrap(m.db, ConsumerPaymentOutcome, func(ctx context.Context, tx db.Tx, env sk.EventEnvelope) error {
			return m.svc.HandlePaymentEvent(ctx, tx, env)
		})},
	}
}

// Jobs は回収時刻のジョブ（worker-billing）。
func (m *Module) Jobs() []scheduler.Job { return []scheduler.Job{m.svc.CollectionDueJob()} }

// Problem は billing のエラーを Problem にする。対応がなければ nil。
func Problem(err error) *httpx.Problem { return bilhttp.Problem(err) }
