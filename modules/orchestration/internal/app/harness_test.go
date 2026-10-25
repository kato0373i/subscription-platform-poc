//go:build integration

package app_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/rand/v2"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/db/migrate"
	"subscription-platform/internal/platform/inbox"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/outbox"
	"subscription-platform/internal/platform/scheduler"
	"subscription-platform/internal/platform/testsupport/pgtest"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/billing"
	billingdb "subscription-platform/modules/billing/db"
	"subscription-platform/modules/catalog"
	catalogdb "subscription-platform/modules/catalog/db"
	"subscription-platform/modules/customer"
	customerc "subscription-platform/modules/customer/contracts"
	customerdb "subscription-platform/modules/customer/db"
	orchdb "subscription-platform/modules/orchestration/db"
	"subscription-platform/modules/orchestration/internal/app"
	"subscription-platform/modules/payments"
	paymentsc "subscription-platform/modules/payments/contracts"
	paymentsdb "subscription-platform/modules/payments/db"
	"subscription-platform/modules/subscription"
	subdb "subscription-platform/modules/subscription/db"
	"subscription-platform/modules/tenant"
	tenantdb "subscription-platform/modules/tenant/db"
)

// 全モジュールを本物で組み立てる（モック・フェイクの contracts を使わない。P-TST-2）。決済は StubAdapter、SQS の代わりに
// Outbox の中継を手で回して購読者に配る（配線は deploy/messaging.yaml の購読の定義をそのまま使う）。
func TestMain(m *testing.M) {
	src := func(schema string, fsys migrate.Source) migrate.Source { fsys.Schema = schema; return fsys }
	pgtest.Main(m,
		src("tenant", migrate.Source{FS: migrate.SubFS(tenantdb.Migrations, "migrations")}),
		src("customer", migrate.Source{FS: migrate.SubFS(customerdb.Migrations, "migrations")}),
		src("catalog", migrate.Source{FS: migrate.SubFS(catalogdb.Migrations, "migrations")}),
		src("subscription", migrate.Source{FS: migrate.SubFS(subdb.Migrations, "migrations")}),
		src("billing", migrate.Source{FS: migrate.SubFS(billingdb.Migrations, "migrations")}),
		src("payments", migrate.Source{FS: migrate.SubFS(paymentsdb.Migrations, "migrations")}),
		migrate.Source{Schema: "payments", Table: "payments.goose_devtools_version", FS: migrate.SubFS(paymentsdb.DevtoolsMigrations, "migrations_devtools")},
		src("orchestration", migrate.Source{FS: migrate.SubFS(orchdb.Migrations, "migrations")}),
	)
}

type memPublisher struct {
	mu  sync.Mutex
	evs []sk.EventEnvelope
}

func (p *memPublisher) Publish(_ context.Context, evs []sk.EventEnvelope) ([]messaging.PublishResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evs = append(p.evs, evs...)
	res := make([]messaging.PublishResult, len(evs))
	for i, e := range evs {
		res[i] = messaging.PublishResult{EventID: e.EventID}
	}
	return res, nil
}

func (p *memPublisher) drain() []sk.EventEnvelope {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.evs
	p.evs = nil
	return out
}

type world struct {
	t        *testing.T
	d        *pgtest.DB
	clock    *sk.ManualClock
	svc      *app.Service
	hooks    *app.Hooks
	tenant   *tenant.Module
	customer *customer.Module
	catalog  *catalog.Module
	sub      *subscription.Module
	billing  *billing.Module
	payments *payments.Module
	relay    *outbox.Relay
	pub      *memPublisher
	topo     messaging.Topology
	handlers map[string]messaging.Handler

	mu       sync.Mutex
	renewals []messaging.FIFOMessage
	enqueued []string
}

type worldOpt func(*app.Deps)

func newWorld(t *testing.T, opts ...worldOpt) *world {
	t.Helper()
	d := pgtest.New(t)
	clock := sk.NewManualClock(time.Date(2026, 11, 15, 1, 0, 0, 0, time.UTC)) // JST 11-15 10:00
	w := &world{t: t, d: d, clock: clock, pub: &memPublisher{}, hooks: &app.Hooks{}}
	w.tenant = tenant.New(tenant.Deps{Pool: d.Pool, Clock: clock})
	w.customer = customer.New(customer.Deps{Pool: d.Pool, Clock: clock})
	w.catalog = catalog.New(catalog.Deps{Pool: d.Pool, Clock: clock, Tenants: w.tenant.Service(), ReservationTTL: 24 * time.Hour})
	w.sub = subscription.New(subscription.Deps{Pool: d.Pool, Clock: clock, Catalog: w.catalog.Service()})
	w.billing = billing.New(billing.Deps{Pool: d.Pool, Clock: clock, Lease: time.Minute})
	var err error
	w.payments, err = payments.New(payments.Deps{Pool: d.Pool, Clock: clock, Methods: "card:stub", EnableStub: true, StubSecret: "secret",
		SendProviderEvent: func(context.Context, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	deps := app.Deps{
		DB: d.Pool.Module("orchestration"), Clock: clock, Tenant: w.tenant.Service(), Customer: w.customer.Service(), Catalog: w.catalog.Service(),
		Subscription: w.sub.Service(), Billing: w.billing.Service(), Payments: w.payments.Service(),
		SendRenewal: func(_ context.Context, msgs []messaging.FIFOMessage) error {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.renewals = append(w.renewals, msgs...)
			return nil
		},
		EnqueueJob: func(_ context.Context, job string, _ sk.Date, _ []byte) (uuid.UUID, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.enqueued = append(w.enqueued, job)
			return uuid.New(), nil
		},
		Hooks: app.Hooks{
			AfterIssueInvoice:   func(ctx context.Context) error { return callHook(w.hooks.AfterIssueInvoice, ctx) },
			BeforeSignupCollect: func(ctx context.Context) error { return callHook(w.hooks.BeforeSignupCollect, ctx) },
			AfterDispatchChunk:  func(ctx context.Context, n int) error { return callHook2(w.hooks.AfterDispatchChunk, ctx, n) },
			AfterBulkSubscriber: func(ctx context.Context, n int) error { return callHook2(w.hooks.AfterBulkSubscriber, ctx, n) },
		},
	}
	for _, o := range opts {
		o(&deps)
	}
	w.svc = app.New(deps)
	w.relay = outbox.NewRelay(d.Pool, w.pub, []string{"tenant", "customer", "catalog", "subscription", "billing", "payments", "orchestration"}, outbox.RelayConfig{})
	w.topo, err = messaging.LoadTopology(os.DirFS("../../../../deploy"))
	if err != nil {
		t.Fatal(err)
	}
	orc := d.Pool.Module("orchestration")
	w.handlers = map[string]messaging.Handler{
		"orchestration.events":     inbox.Guard(orc, "orchestration.events", w.svc.HandleEvent),
		"orchestration.collection": inbox.Guard(orc, "orchestration.collection", w.svc.HandleCollectionDue),
		"orchestration.renewal":    inbox.Guard(orc, "orchestration.renewal", w.svc.HandleRenewal),
	}
	for _, c := range append(w.billing.Consumers(), w.payments.Consumers()...) {
		w.handlers[c.Name] = c.Handler
	}
	return w
}

func callHook(f func(context.Context) error, ctx context.Context) error {
	if f == nil {
		return nil
	}
	return f(ctx)
}

func callHook2(f func(context.Context, int) error, ctx context.Context, n int) error {
	if f == nil {
		return nil
	}
	return f(ctx, n)
}

// pump は Outbox を中継して購読者に配る。届いた更新の投入（FIFO）も処理する。何も動かなくなるまで繰り返す。
// 購読者のエラー（60 秒後の再配信など）は返し値で集める。
func (w *world) pump() []error {
	w.t.Helper()
	ctx := context.Background()
	var errs []error
	for range 50 {
		if _, err := w.relay.FlushOnce(ctx); err != nil {
			w.t.Fatal(err)
		}
		evs := w.pub.drain()
		w.mu.Lock()
		fifo := w.renewals
		w.renewals = nil
		w.mu.Unlock()
		if len(evs) == 0 && len(fifo) == 0 {
			return errs
		}
		for _, e := range evs {
			for _, q := range w.topo.Subscribers(e.EventType) {
				h, ok := w.handlers[q.Consumer]
				if !ok {
					continue // integration など未実装の購読者
				}
				if err := h(messaging.ContextFromEnvelope(ctx, e), e); err != nil {
					errs = append(errs, fmt.Errorf("%s %s: %w", q.Consumer, e.EventType, err))
				}
			}
		}
		for _, m := range fifo {
			if err := w.handlers["orchestration.renewal"](ctx, m.Env); err != nil {
				errs = append(errs, fmt.Errorf("renewal: %w", err))
			}
		}
	}
	w.t.Fatal("pump: did not settle")
	return nil
}

func (w *world) mustPump() {
	w.t.Helper()
	if errs := w.pump(); len(errs) > 0 {
		w.t.Fatalf("pump: %v", errs)
	}
}

func (w *world) count(sql string, args ...any) int64 {
	w.t.Helper()
	return w.d.QueryInt(w.t, sql, args...)
}

// ---- データの準備（事業者・プランは SQL で入れる。業務の操作は contracts と app を通す） ----

type fixtureTenant struct {
	id   sk.TenantID
	plan sk.PlanID
}

func (w *world) newTenant(amount int64, trialDays int) fixtureTenant {
	w.t.Helper()
	tid, prod, plan, pv := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := w.clock.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	w.d.AsSuper(w.t,
		fmt.Sprintf(`INSERT INTO tenant.tenants (id, name, status, fincode_shop_id, approved_at, created_at, updated_at) VALUES ('%s', 'T', 'active', 'shop_%s', '%s', '%s', '%s')`, tid, tid.String()[:8]+tid.String()[9:13], now, now, now),
		fmt.Sprintf(`INSERT INTO catalog.products (id, tenant_id, name, created_at, updated_at) VALUES ('%s', '%s', 'P', '%s', '%s')`, prod, tid, now, now),
		fmt.Sprintf(`INSERT INTO catalog.plans (id, tenant_id, product_id, name, billing_interval, trial_days, pricing_scheme, status, published_at, created_at, updated_at)
			VALUES ('%s', '%s', '%s', 'M', 'month', %d, 'fixed', 'published', '%s', '%s', '%s')`, plan, tid, prod, trialDays, now, now, now),
		fmt.Sprintf(`INSERT INTO catalog.price_versions (id, tenant_id, plan_id, version_no, pricing_scheme, amount, tax_rate, effective_from, created_at)
			VALUES ('%s', '%s', '%s', 1, 'fixed', %d, 0.100, '%s', '%s')`, pv, tid, plan, amount, now, now),
	)
	return fixtureTenant{id: sk.IDFromUUID[sk.TenantTag](tid), plan: sk.IDFromUUID[sk.PlanTag](plan)}
}

type fixtureCustomer struct {
	id sk.CustomerAccountID
	pm sk.PaymentMethodID
}

func (w *world) newCustomer(scenario string) fixtureCustomer {
	w.t.Helper()
	sub := "it-" + uuid.NewString()
	yes := true
	acct, err := w.customer.Service().ResolveOrCreateAccount(context.Background(), customerc.AuthIdentity{Subject: sub, Email: sub + "@example.invalid", EmailVerified: &yes})
	if err != nil {
		w.t.Fatal(err)
	}
	pm, err := w.payments.Service().RegisterPaymentMethod(context.Background(), paymentsc.RegisterPaymentMethodCmd{
		CustomerAccountID: acct.ID, MethodType: paymentsc.MethodCard, Provider: "stub", Token: fmt.Sprintf("tok_stub_%s_4242_1230_%08x", scenario, rand.Uint32()), //nolint:gosec // テスト
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return fixtureCustomer{id: acct.ID, pm: pm.ID}
}

func (w *world) checkoutInput(ft fixtureTenant, fc fixtureCustomer, key string) app.CheckoutInput {
	h := sha256.Sum256([]byte(key + ft.plan.String()))
	return app.CheckoutInput{CustomerAccountID: fc.id, IdempotencyKey: key, RequestHash: h[:], TenantID: ft.id, PlanID: ft.plan, PaymentMethodID: fc.pm}
}

// checkout は申込を成功させる（201）。
func (w *world) checkout(ft fixtureTenant, fc fixtureCustomer) app.CheckoutResult {
	w.t.Helper()
	res, err := w.svc.Checkout(context.Background(), w.checkoutInput(ft, fc, uuid.NewString()))
	if err != nil {
		w.t.Fatal(err)
	}
	w.mustPump()
	return res
}

// runJob はジョブを 1 回実行する（カーソルの保存先は本物の platform.job_cursors）。
func (w *world) runJob(j scheduler.Job, asOf sk.Date, attempt int) (scheduler.Stats, error) {
	id := uuid.Must(uuid.NewV7())
	return j.Run(context.Background(), scheduler.Run{ID: id, Job: j.Name(), AsOf: asOf, Attempt: attempt, Cursors: scheduler.NewCursorStore(w.d.Pool, j.Name(), id)})
}

func (w *world) systemCtx() context.Context { return db.WithSystemScope(context.Background(), "test") }
