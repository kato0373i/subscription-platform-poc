//go:build integration

package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/db/migrate"
	"subscription-platform/internal/platform/outbox"
	"subscription-platform/internal/platform/scheduler"
	"subscription-platform/internal/platform/testsupport/pgtest"
	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/subscription/contracts"
	subdb "subscription-platform/modules/subscription/db"
	"subscription-platform/modules/subscription/internal/app"
)

func TestMain(m *testing.M) {
	pgtest.Main(m, migrate.Source{Schema: "subscription", FS: migrate.SubFS(subdb.Migrations, "migrations")})
}

type fixture struct {
	d     *pgtest.DB
	svc   *app.Service
	clock *sk.ManualClock
}

// setup は catalog を使わないテスト用（プラン変更の検証は E2E で確かめる）。
func setup(t *testing.T) *fixture {
	t.Helper()
	d := pgtest.New(t)
	clock := sk.NewManualClock(time.Date(2026, 10, 15, 1, 0, 0, 0, time.UTC)) // JST 10-15 10:00
	return &fixture{d: d, clock: clock, svc: app.New(app.Deps{DB: d.Pool.Module("subscription"), Outbox: outbox.NewWriter("subscription", clock), Clock: clock})}
}

type signupOpt func(*c.CreateSignupCmd)

func (f *fixture) signup(t *testing.T, tenant sk.TenantID, customer sk.CustomerAccountID, opts ...signupOpt) c.SubscriptionView {
	t.Helper()
	v, err := f.svc.CreateForSignup(context.Background(), f.cmd(tenant, customer, opts...))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

var product = sk.NewID[sk.ProductTag]()

func (f *fixture) cmd(tenant sk.TenantID, customer sk.CustomerAccountID, opts ...signupOpt) c.CreateSignupCmd {
	cmd := c.CreateSignupCmd{
		TenantID: tenant, CustomerAccountID: customer, TenantCustomerID: "cus_" + uuid.NewString()[:24], SignupRequestID: uuid.NewString(),
		Plan:            c.PlanInput{PlanID: sk.NewID[sk.PlanTag](), ProductID: product, PriceVersionID: sk.NewID[sk.PriceVersionTag](), Interval: sk.IntervalMonth},
		PaymentMethodID: sk.NewID[sk.PaymentMethodTag](), SignupDate: f.clock.Today(),
	}
	for _, o := range opts {
		o(&cmd)
	}
	return cmd
}

func trial(days int) signupOpt { return func(cmd *c.CreateSignupCmd) { cmd.Plan.TrialDays = days } }

// AC-SUB-011: 同じ商品のトライアル付き申込が同時に 2 件 → trialing は 1 件
func TestConcurrentTrialSignups(t *testing.T) {
	f := setup(t)
	tenant := sk.NewID[sk.TenantTag]()
	for range 5 {
		customer := sk.NewID[sk.CustomerAccountTag]()
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = f.svc.CreateForSignup(context.Background(), f.cmd(tenant, customer, trial(14)))
			}()
		}
		wg.Wait()
		notEligible := 0
		for _, err := range errs {
			if errors.Is(err, c.ErrTrialNotEligible) {
				notEligible++
			} else if err != nil {
				t.Fatal(err)
			}
		}
		if notEligible != 1 || f.d.QueryInt(t, `SELECT count(*) FROM subscription.subscriptions WHERE customer_account_id = $1 AND status = 'trialing'`, customer.UUID()) != 1 {
			t.Fatalf("errs = %v", errs)
		}
	}
	// AC-SUB-010・012: 利用済みはフォールバックで pending_initial。トライアルなしの契約は利用を記録しない
	customer := sk.NewID[sk.CustomerAccountTag]()
	f.signup(t, tenant, customer)
	if v := f.signup(t, tenant, customer, trial(14)); v.Status != c.StatusTrialing {
		t.Errorf("トライアルなしの契約の後でもトライアルを使える: %s", v.Status)
	}
	if _, err := f.svc.CreateForSignup(context.Background(), f.cmd(tenant, customer, trial(14))); !errors.Is(err, c.ErrTrialNotEligible) {
		t.Errorf("利用済み: %v", err)
	}
	if v := f.signup(t, tenant, customer, trial(14), func(cmd *c.CreateSignupCmd) { cmd.AllowTrialFallback = true }); v.Status != c.StatusPendingInitial || v.InitialTerms == nil {
		t.Errorf("フォールバック: %+v", v)
	}
}

// 同じ申込（signup_request_id）の同時実行は 1 件に収束する（AC-SUB-004 の contracts 側）
func TestIdempotentSignup(t *testing.T) {
	f := setup(t)
	cmd := f.cmd(sk.NewID[sk.TenantTag](), sk.NewID[sk.CustomerAccountTag]())
	var wg sync.WaitGroup
	ids := make([]sk.SubscriptionID, 5)
	for i := range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := f.svc.CreateForSignup(context.Background(), cmd)
			if err != nil {
				t.Error(err)
			}
			ids[i] = v.ID
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("ids = %v", ids)
		}
	}
	if n := f.d.QueryInt(t, `SELECT count(*) FROM subscription.subscriptions WHERE signup_request_id = $1`, cmd.SignupRequestID); n != 1 {
		t.Errorf("rows = %d", n)
	}
}

// AC-SUB-036: BeginRenewal とプラン変更の予約（契約者）が同時でも、再読込・再適用で両方が反映される
func TestOptimisticLockRetry(t *testing.T) {
	f := setup(t)
	tenant := sk.NewID[sk.TenantTag]()
	for range 10 {
		v := f.signup(t, tenant, sk.NewID[sk.CustomerAccountTag]())
		if _, err := f.svc.Activate(context.Background(), v.ID, sk.NewID[sk.InvoiceTag]()); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var errR, errC error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, errR = f.svc.BeginRenewal(context.Background(), v.ID, *v.NextRenewalDate)
		}()
		go func() {
			defer wg.Done()
			_, errC = f.svc.ScheduleCancellation(db.WithTenantScope(context.Background(), tenant), v.ID)
		}()
		wg.Wait()
		if errR != nil || errC != nil {
			t.Fatalf("renewal=%v cancel=%v", errR, errC)
		}
		got, _ := f.svc.Get(context.Background(), v.ID)
		// どちらが先でも両方が反映される：R あり、解約予約あり。version は Activate(2) から 2 回の変更で 4
		if !got.RenewalInProgress && got.Status != c.StatusCanceled {
			t.Fatalf("R が反映されていない: %+v", got)
		}
		if got.Status != c.StatusCanceled && got.Cancellation == nil {
			t.Fatalf("解約予約が反映されていない: %+v", got)
		}
		if got.Version != 4 {
			t.Fatalf("version = %d", got.Version)
		}
	}
}

// AC-SUB-041・042: tenant_isolation と consumer_self
func TestRLS(t *testing.T) {
	f := setup(t)
	a, b := sk.NewID[sk.TenantTag](), sk.NewID[sk.TenantTag]()
	x, y := sk.NewID[sk.CustomerAccountTag](), sk.NewID[sk.CustomerAccountTag]()
	f.signup(t, a, x, trial(7))
	f.signup(t, b, x)
	vy := f.signup(t, a, y, trial(7))
	count := func(ctx context.Context, table string) int {
		var n int
		if err := f.d.Pool.Module("subscription").ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM subscription.`+table).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	bg := context.Background()
	if n := count(db.WithTenantScope(bg, a), "subscriptions"); n != 2 {
		t.Errorf("テナント A = %d", n)
	}
	if n := count(db.WithCustomerScope(bg, x), "subscriptions"); n != 2 {
		t.Errorf("契約者 X（事業者をまたぐ）= %d", n)
	}
	if n := count(db.WithCustomerScope(bg, x), "trial_usages"); n != 1 {
		t.Errorf("X の trial_usages = %d", n)
	}
	if n := count(db.WithUnscoped(bg, "test"), "subscriptions"); n != 0 {
		t.Errorf("未設定 = %d", n)
	}
	// X のスコープでは Y の契約を更新できない（API では 404）
	if _, err := f.svc.ScheduleCancellation(db.WithCustomerScope(bg, x), vy.ID); !errors.Is(err, c.ErrNotFound) {
		t.Errorf("他人の契約: %v", err)
	}
	if _, err := f.svc.ScheduleCancellation(db.WithCustomerScope(bg, y), vy.ID); err != nil {
		t.Errorf("本人の契約: %v", err)
	}
	if has, err := f.svc.HasOpenSubscriptions(bg, x); err != nil || !has {
		t.Errorf("HasOpenSubscriptions: %v %v", has, err)
	}
	pgtest.AssertAllTenantTablesHaveRLS(t, f.d)
}

// 更新対象の一覧とトライアル終了の予告ジョブ（AC-SUB-037 の contracts 側）
func TestListsAndTrialWillEndJob(t *testing.T) {
	f := setup(t)
	tenant := sk.NewID[sk.TenantTag]()
	for range 3 {
		f.signup(t, tenant, sk.NewID[sk.CustomerAccountTag](), trial(5)) // trial_end = 10-19
	}
	pi := f.signup(t, tenant, sk.NewID[sk.CustomerAccountTag]())
	page, err := f.svc.ListTrialEndingOn(context.Background(), sk.MustDate(2026, 10, 19), "", 2)
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("page1: %+v %v", page, err)
	}
	rest, _ := f.svc.ListTrialEndingOn(context.Background(), sk.MustDate(2026, 10, 19), page.NextCursor, 2)
	if len(rest.Items) != 1 {
		t.Errorf("page2: %+v", rest)
	}
	due, _ := f.svc.ListDueForRenewal(context.Background(), sk.MustDate(2026, 10, 20), "", 100)
	if len(due.Items) != 3 {
		t.Errorf("due = %d（pending_initial は含めない）", len(due.Items))
	}
	open, _ := f.svc.ListOpenByTenant(context.Background(), tenant, "", 100)
	if len(open.Items) != 4 {
		t.Errorf("open = %d", len(open.Items))
	}
	run := scheduler.Run{AsOf: sk.MustDate(2026, 10, 16)}
	for i, want := range []int64{3, 0} {
		st, err := f.svc.TrialWillEndJob().Run(context.Background(), run)
		if err != nil || st["notified"] != want {
			t.Fatalf("%d 回目: %v %v", i+1, st, err)
		}
	}
	if n := f.d.QueryInt(t, `SELECT count(*) FROM subscription.outbox_events WHERE event_type = 'subscription.trial_will_end'`); n != 3 {
		t.Errorf("trial_will_end = %d", n)
	}
	if used, _ := f.svc.IsPaymentMethodInUse(context.Background(), pi.PaymentMethodID); !used {
		t.Error("IsPaymentMethodInUse")
	}
	// イベントの payload にスナップショットと actor（system）がある
	var snapshotStatus, actorType string
	if err := f.d.SuperPool(t).QueryRow(context.Background(), `SELECT payload->'subscription'->>'status', actor->>'type' FROM subscription.outbox_events WHERE event_type = 'subscription.trial_will_end' LIMIT 1`).Scan(&snapshotStatus, &actorType); err != nil {
		t.Fatal(err)
	}
	if snapshotStatus != "trialing" || actorType != "system" {
		t.Errorf("payload: status=%s actor=%s", snapshotStatus, actorType)
	}
}
