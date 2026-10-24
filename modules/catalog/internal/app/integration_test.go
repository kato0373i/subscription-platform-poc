//go:build integration

package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/db/migrate"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/outbox"
	"subscription-platform/internal/platform/scheduler"
	"subscription-platform/internal/platform/testsupport/pgtest"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/catalog/contracts"
	catalogdb "subscription-platform/modules/catalog/db"
	"subscription-platform/modules/catalog/internal/app"
	"subscription-platform/modules/catalog/internal/domain"
	"subscription-platform/modules/catalog/internal/domain/pricing"
	"subscription-platform/modules/tenant"
	tenantdb "subscription-platform/modules/tenant/db"
)

func TestMain(m *testing.M) {
	pgtest.Main(m,
		migrate.Source{Schema: "tenant", FS: migrate.SubFS(tenantdb.Migrations, "migrations")},
		migrate.Source{Schema: "catalog", FS: migrate.SubFS(catalogdb.Migrations, "migrations")},
	)
}

type fixture struct {
	d     *pgtest.DB
	svc   *app.Service
	clock *sk.ManualClock
}

// setup は本物の tenant モジュールを組み立てて catalog に渡す（P-TST-2）。
func setup(t *testing.T) *fixture {
	t.Helper()
	d := pgtest.New(t)
	clock := sk.NewManualClock(time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC))
	codec, err := httpx.NewCursorCodec(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	tm := tenant.New(tenant.Deps{Pool: d.Pool, Clock: clock, Cursors: codec})
	svc := app.New(app.Deps{DB: d.Pool.Module("catalog"), Outbox: outbox.NewWriter("catalog", clock), Clock: clock, Schemes: pricing.Default(), Tenants: tm.Service(), ReservationTTL: 24 * time.Hour})
	return &fixture{d: d, svc: svc, clock: clock}
}

// tenant は事業者の行を直接作る（API で作りにくい前提の準備）。
func (f *fixture) tenant(t *testing.T, status string) sk.TenantID {
	t.Helper()
	id := sk.NewID[sk.TenantTag]()
	shop := "shop_" + id.UUID().String()[24:]
	f.d.AsSuper(t, `INSERT INTO tenant.tenants (id, name, status, fincode_shop_id, approved_at, suspended_at, suspend_reason, created_at, updated_at)
		VALUES ('`+id.String()+`', 'T', '`+status+`', CASE WHEN '`+status+`' = 'pending_review' THEN NULL ELSE '`+shop+`' END,
		CASE WHEN '`+status+`' = 'pending_review' THEN NULL ELSE now() END,
		CASE WHEN '`+status+`' = 'suspended' THEN now() END, CASE WHEN '`+status+`' = 'suspended' THEN '停止' END, now(), now())`)
	return id
}

func scope(t sk.TenantID) context.Context { return db.WithTenantScope(context.Background(), t) }

// publishedPlan は商品と公開済みのプランを作る。
func (f *fixture) publishedPlan(t *testing.T, tid sk.TenantID, amount int64) domain.PlanState {
	t.Helper()
	ctx := scope(tid)
	prod, err := f.svc.CreateProduct(ctx, tid, "商品", "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.svc.CreatePlan(ctx, domain.PlanInput{TenantID: tid, ProductID: prod.ID, Name: "月額", Interval: sk.IntervalMonth, Amount: amount, Scheme: pricing.KindFixed})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = f.svc.PublishPlan(ctx, tid, p.ID); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f *fixture) coupon(t *testing.T, tid sk.TenantID, code string, max *int) domain.CouponState {
	t.Helper()
	c, err := f.svc.CreateCoupon(scope(tid), domain.CouponInput{
		TenantID: tid, Code: code, Name: "c", Discount: sk.Discount{Type: sk.DiscountPercent, Value: 50}, DurationCycles: 1, AppliesTo: domain.AllPlans, MaxRedemptions: max,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *fixture) redeemed(t *testing.T, couponID contracts.CouponID) int64 {
	t.Helper()
	return f.d.QueryInt(t, `SELECT redeemed_count FROM catalog.coupons WHERE id = $1`, couponID.UUID())
}

func ptr(n int) *int { return &n }

// AC-CAT-008: 価格版は追記のみ
func TestPriceVersionsAreAppendOnly(t *testing.T) {
	f := setup(t)
	tid := f.tenant(t, "active")
	p := f.publishedPlan(t, tid, 980)
	for _, sql := range []string{
		`UPDATE catalog.price_versions SET amount = 1 WHERE id = '` + p.CurrentPrice.ID.String() + `'`,
		`DELETE FROM catalog.price_versions WHERE id = '` + p.CurrentPrice.ID.String() + `'`,
	} {
		_, err := f.d.SuperPool(t).Exec(context.Background(), sql)
		if err == nil {
			t.Errorf("例外にならない: %s", sql)
		}
	}
}

// AC-CAT-013: 事業者 A の文脈では B のデータが見えない。未設定なら 0 件
func TestTenantIsolation(t *testing.T) {
	f := setup(t)
	a, b := f.tenant(t, "active"), f.tenant(t, "active")
	pb := f.publishedPlan(t, b, 980)
	cb := f.coupon(t, b, "BONLY", nil)
	f.publishedPlan(t, a, 500)
	res, err := f.svc.ReserveCoupon(context.Background(), b, "BONLY", pb.ID, sk.NewID[sk.CustomerAccountTag](), sk.NewID[sk.SubscriptionTag]())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := f.svc.GetPurchasablePlan(ctx, a, pb.ID); !errors.Is(err, contracts.ErrPlanNotFound) {
		t.Errorf("plan: %v", err)
	}
	if _, err := f.svc.QuotePrice(ctx, a, pb.CurrentPrice.ID, nil); !errors.Is(err, contracts.ErrPriceVersionNotFound) {
		t.Errorf("price: %v", err)
	}
	if _, err := f.svc.ReserveCoupon(ctx, a, "BONLY", pb.ID, sk.NewID[sk.CustomerAccountTag](), sk.NewID[sk.SubscriptionTag]()); !errors.Is(err, contracts.ErrPlanNotFound) {
		t.Errorf("coupon: %v", err)
	}
	if err := f.svc.ConfirmCouponRedemption(ctx, a, res.RedemptionID); !errors.Is(err, contracts.ErrRedemptionNotFound) {
		t.Errorf("redemption: %v", err)
	}
	if _, err := f.svc.GetCoupon(scope(a), cb.ID); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("console coupon: %v", err)
	}
	for _, table := range []string{"products", "plans", "price_versions", "coupons", "coupon_redemptions"} {
		var nA, nNone int
		_ = f.d.Pool.Module("catalog").ReadTx(scope(a), func(ctx context.Context, tx db.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM catalog.`+table+` WHERE tenant_id <> $1`, a.UUID()).Scan(&nA)
		})
		_ = f.d.Pool.Module("catalog").ReadTx(db.WithUnscoped(ctx, "test"), func(ctx context.Context, tx db.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM catalog.`+table).Scan(&nNone)
		})
		if nA != 0 || nNone != 0 {
			t.Errorf("%s: 他テナント %d、未設定 %d", table, nA, nNone)
		}
	}
	pgtest.AssertAllTenantTablesHaveRLS(t, f.d)
}

// AC-CAT-021: 残り 1 枚を 2 人が同時に予約すると 1 人だけ成功する（100 回）
func TestLastCouponRace(t *testing.T) {
	f := setup(t)
	tid := f.tenant(t, "active")
	p := f.publishedPlan(t, tid, 980)
	for i := range 100 {
		c := f.coupon(t, tid, "LAST"+string(rune('A'+i/26))+string(rune('A'+i%26)), ptr(1))
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for k := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[k] = f.svc.ReserveCoupon(context.Background(), tid, c.Code, p.ID, sk.NewID[sk.CustomerAccountTag](), sk.NewID[sk.SubscriptionTag]())
			}()
		}
		wg.Wait()
		ok, exhausted := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, contracts.ErrCouponExhausted):
				exhausted++
			default:
				t.Fatalf("想定外: %v", err)
			}
		}
		if ok != 1 || exhausted != 1 || f.redeemed(t, c.ID) != 1 {
			t.Fatalf("回 %d: ok=%d exhausted=%d count=%d", i, ok, exhausted, f.redeemed(t, c.ID))
		}
	}
}

// AC-CAT-022・023: 同じ契約者の 2 つの申込は 1 件だけ。同じ契約の再試行は同じ予約を返す
func TestSameCustomer(t *testing.T) {
	f := setup(t)
	tid := f.tenant(t, "active")
	p := f.publishedPlan(t, tid, 980)
	c := f.coupon(t, tid, "ONCE", ptr(10))
	x := sk.NewID[sk.CustomerAccountTag]()
	for range 10 {
		var wg sync.WaitGroup
		errs := make([]error, 2)
		cc := f.coupon(t, tid, "R"+sk.NewID[sk.EventTag]().String()[24:], ptr(10))
		for k := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[k] = f.svc.ReserveCoupon(context.Background(), tid, cc.Code, p.ID, x, sk.NewID[sk.SubscriptionTag]())
			}()
		}
		wg.Wait()
		used := 0
		for _, err := range errs {
			if errors.Is(err, contracts.ErrCouponAlreadyUsed) {
				used++
			} else if err != nil {
				t.Fatal(err)
			}
		}
		if used != 1 || f.redeemed(t, cc.ID) != 1 {
			t.Fatalf("used=%d count=%d", used, f.redeemed(t, cc.ID))
		}
	}
	sub := sk.NewID[sk.SubscriptionTag]()
	r1, err := f.svc.ReserveCoupon(context.Background(), tid, "once", p.ID, x, sub)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := f.svc.ReserveCoupon(context.Background(), tid, "ONCE", p.ID, x, sub)
	if err != nil || r2.RedemptionID != r1.RedemptionID || f.redeemed(t, c.ID) != 1 {
		t.Errorf("再試行: %v %v count=%d", r2, err, f.redeemed(t, c.ID))
	}
	if r1.Discount.Type != sk.DiscountPercent || r1.Discount.Value != 50 || r1.DurationCycles != 1 {
		t.Errorf("割引: %+v", r1)
	}
	if n := f.d.QueryInt(t, `SELECT count(*) FROM catalog.outbox_events WHERE event_type = 'catalog.coupon_redeemed' AND aggregate_id = $1`, r1.RedemptionID.UUID()); n != 1 {
		t.Errorf("coupon_redeemed = %d", n)
	}
	// 解放すると枠が戻り、同じ契約者が再び予約できる（P-CAT-4）
	if err := f.svc.ReleaseCouponRedemption(context.Background(), tid, r1.RedemptionID); err != nil {
		t.Fatal(err)
	}
	if f.redeemed(t, c.ID) != 0 {
		t.Error("解放で枠が戻らない")
	}
	if _, err := f.svc.ReserveCoupon(context.Background(), tid, "ONCE", p.ID, x, sk.NewID[sk.SubscriptionTag]()); err != nil {
		t.Errorf("解放後の再予約: %v", err)
	}
}

// AC-CAT-028・029: 期限切れで解放された予約の遅れた確定、無効化・期限切れ後の確定
func TestLateConfirm(t *testing.T) {
	f := setup(t)
	tid := f.tenant(t, "active")
	p := f.publishedPlan(t, tid, 980)
	ctx := context.Background()
	reserve := func(code string) contracts.CouponReservation {
		r, err := f.svc.ReserveCoupon(ctx, tid, code, p.ID, sk.NewID[sk.CustomerAccountTag](), sk.NewID[sk.SubscriptionTag]())
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	free := f.coupon(t, tid, "FREE", ptr(2))
	full := f.coupon(t, tid, "FULL", ptr(1))
	rFree, rFull := reserve("FREE"), reserve("FULL")

	f.clock.Advance(24*time.Hour + time.Minute)
	stats, err := f.svc.SweepJob().Run(ctx, schedulerRun())
	if err != nil || stats["released"] != 2 {
		t.Fatalf("sweep: %v %v", stats, err)
	}
	// 2 回目は何もしない（二重に枠を戻さない）
	if stats, _ := f.svc.SweepJob().Run(ctx, schedulerRun()); stats["released"] != 0 {
		t.Errorf("2 回目: %v", stats)
	}
	if f.redeemed(t, free.ID) != 0 || f.redeemed(t, full.ID) != 0 {
		t.Fatal("枠が戻っていない")
	}
	if n := f.d.QueryInt(t, `SELECT count(*) FROM catalog.outbox_events WHERE event_type = 'catalog.coupon_redemption_released' AND payload->>'reason' = 'expired'`); n != 2 {
		t.Errorf("解放イベント = %d", n)
	}
	// FULL の枠を別の契約者に取られた後で、遅れた確定が来る
	reserve("FULL")
	if err := f.svc.ConfirmCouponRedemption(ctx, tid, rFree.RedemptionID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ConfirmCouponRedemption(ctx, tid, rFull.RedemptionID); err != nil {
		t.Fatal(err)
	}
	if n := f.d.QueryInt(t, `SELECT count(*) FROM catalog.coupon_redemptions WHERE id = $1 AND status = 'confirmed' AND NOT over_limit`, rFree.RedemptionID.UUID()); n != 1 || f.redeemed(t, free.ID) != 1 {
		t.Error("枠が空いていれば取り直して確定する")
	}
	if n := f.d.QueryInt(t, `SELECT count(*) FROM catalog.coupon_redemptions WHERE id = $1 AND status = 'confirmed' AND over_limit`, rFull.RedemptionID.UUID()); n != 1 || f.redeemed(t, full.ID) != 1 {
		t.Error("埋まっていれば over_limit で確定し、枠は上限のまま")
	}

	// AC-CAT-029: 予約後に無効化され期限も過ぎても確定できる
	exp := f.clock.Now().Add(time.Hour)
	c, err := f.svc.CreateCoupon(scope(tid), domain.CouponInput{TenantID: tid, Code: "SOON", Name: "c", Discount: sk.Discount{Type: sk.DiscountAmount, Value: 100}, DurationCycles: 1, AppliesTo: domain.AllPlans, ExpiresAt: &exp})
	if err != nil {
		t.Fatal(err)
	}
	r := reserve("SOON")
	if _, err := f.svc.UpdateCoupon(scope(tid), c.ID, nil, true); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(2 * time.Hour)
	if err := f.svc.ConfirmCouponRedemption(ctx, tid, r.RedemptionID); err != nil {
		t.Errorf("無効化・期限切れ後の確定: %v", err)
	}
	if err := f.svc.ReleaseCouponRedemption(ctx, tid, r.RedemptionID); !errors.Is(err, contracts.ErrRedemptionAlreadyConfirmed) {
		t.Errorf("確定後の解放: %v", err)
	}
}

// contracts の参照（GetPurchasablePlan・GetPlanSnapshot・QuotePrice）と、事業者の状態による公開の可否
func TestPlanContracts(t *testing.T) {
	f := setup(t)
	tid := f.tenant(t, "active")
	pending := f.tenant(t, "pending_review")
	p := f.publishedPlan(t, tid, 980)
	ctx := context.Background()
	snap, err := f.svc.GetPurchasablePlan(ctx, tid, p.ID)
	if err != nil || snap.Amount.Int64() != 980 || snap.PriceVersionID != p.CurrentPrice.ID || snap.TaxRate.String() != "0.100" {
		t.Errorf("snapshot: %+v %v", snap, err)
	}
	q, err := f.svc.QuotePrice(ctx, tid, snap.PriceVersionID, nil)
	if err != nil || q.Amount.Int64() != 980 {
		t.Errorf("quote: %+v %v", q, err)
	}
	if _, err := f.svc.UnpublishPlan(scope(tid), p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.GetPurchasablePlan(ctx, tid, p.ID); !errors.Is(err, contracts.ErrPlanUnavailable) {
		t.Errorf("unpublished: %v", err)
	}
	if d, err := f.svc.GetPlanSnapshot(ctx, tid, p.ID); err != nil || d.Status != contracts.PlanUnpublished || d.Published {
		t.Errorf("detail: %+v %v", d, err)
	}
	prod, _ := f.svc.CreateProduct(scope(pending), pending, "p", "")
	pp, err := f.svc.CreatePlan(scope(pending), domain.PlanInput{TenantID: pending, ProductID: prod.ID, Name: "p", Interval: sk.IntervalYear, Amount: 10000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PublishPlan(scope(pending), pending, pp.ID); !errors.Is(err, domain.ErrTenantNotActive) {
		t.Errorf("審査中の publish: %v", err)
	}
}

func schedulerRun() scheduler.Run { return scheduler.Run{Job: app.SweepJobName} }
