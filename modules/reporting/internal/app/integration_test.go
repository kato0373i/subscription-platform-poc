//go:build integration

package app_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/db/migrate"
	"subscription-platform/internal/platform/testsupport/pgtest"
	sk "subscription-platform/internal/sharedkernel"
	reportingdb "subscription-platform/modules/reporting/db"
	"subscription-platform/modules/reporting/internal/app"
	"subscription-platform/modules/reporting/internal/domain"
)

// 本物の PostgreSQL（testcontainers）。価格は写しの表に先に入れておき、catalog は呼ばない（価格の取得は E2E で確かめる）。
func TestMain(m *testing.M) {
	pgtest.Main(m, migrate.Source{Schema: "reporting", FS: migrate.SubFS(reportingdb.Migrations, "migrations")})
}

type world struct {
	t      *testing.T
	d      *pgtest.DB
	clock  *sk.ManualClock
	svc    *app.Service
	tenant sk.TenantID
}

func newWorld(t *testing.T) *world {
	d := pgtest.New(t)
	clock := sk.NewManualClock(time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)) // JST 10:00
	w := &world{t: t, d: d, clock: clock, tenant: sk.NewID[sk.TenantTag]()}
	w.svc = app.New(app.Deps{DB: d.Pool.Module("reporting"), Clock: clock})
	return w
}

func (w *world) price(amount int64) string {
	pv := uuid.Must(uuid.NewV7())
	w.d.AsOwner(w.t, "INSERT INTO reporting.price_amounts (price_version_id, tenant_id, amount, created_at) VALUES ('"+pv.String()+"', '"+w.tenant.String()+"', "+itoa(amount)+", now())")
	return pv.String()
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// send は契約のイベントを 1 件流す（eventID を指定すると同じメッセージの再配信）。
func (w *world) send(eventID *uuid.UUID, typ string, sub uuid.UUID, version int, status, pv, reason string, at time.Time) uuid.UUID {
	w.t.Helper()
	snap := map[string]any{"subscription_id": sub, "tenant_id": w.tenant, "status": status, "price_version_id": pv, "interval": "month", "quantity": 1, "version": version, "trial_end": nil}
	if reason != "" {
		snap["cancel_reason"] = reason
	}
	env, err := sk.NewEnvelope(typ, "subscription", sub, int64(version), &w.tenant, at, map[string]any{"subscription": snap})
	if err != nil {
		w.t.Fatal(err)
	}
	if eventID != nil {
		env.EventID = sk.IDFromUUID[sk.EventTag](*eventID)
	}
	if err := w.svc.ProjectionHandler("reporting.projection")(context.Background(), env); err != nil {
		w.t.Fatalf("%s: %v", typ, err)
	}
	return env.EventID.UUID()
}

func (w *world) overview() app.OverviewResult {
	w.t.Helper()
	ov, err := w.svc.Overview(context.Background(), w.tenant, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	return ov
}

func TestProjectionIsIdempotentAndIgnoresOldVersions(t *testing.T) {
	w := newWorld(t)
	pv := w.price(980)
	sub := uuid.Must(uuid.NewV7())
	now := w.clock.Now()
	id := w.send(nil, "subscription.started", sub, 1, "active", pv, "", now)
	w.send(&id, "subscription.started", sub, 1, "active", pv, "", now) // 同じメッセージの再配信
	w.send(nil, "subscription.canceled", sub, 3, "canceled", pv, "customer", now)
	w.send(nil, "subscription.past_due", sub, 2, "past_due", pv, "", now) // 古い版が後から届く
	k := w.overview().Current
	if k.NewSubscriptions != 1 || k.NewMRR != 980 || k.Breakdown.Churned != 980 || k.MRR != 0 || k.Canceled.Customer != 1 || k.PastDue != 0 {
		t.Fatalf("kpis = %+v", k)
	}
}

func TestRevenueAndAttentionAndRLS(t *testing.T) {
	w := newWorld(t)
	env, _ := sk.NewEnvelope("billing.invoice_paid", "invoice", uuid.Must(uuid.NewV7()), 1, &w.tenant, w.clock.Now(), map[string]any{"amount": 1980, "kind": "initial"})
	if err := w.svc.ProjectionHandler("reporting.projection")(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	pv := w.price(980)
	w.send(nil, "subscription.past_due", uuid.Must(uuid.NewV7()), 2, "past_due", pv, "", w.clock.Now())
	ov := w.overview()
	if ov.Current.Revenue.Total != 1980 || ov.Current.Revenue.Initial != 1980 || ov.PastDue != 1 || ov.AsOf == nil {
		t.Fatalf("overview = %+v", ov)
	}
	// 他の事業者からは見えない（RLS）
	other := app.New(app.Deps{DB: w.d.Pool.Module("reporting"), Clock: w.clock})
	ov2, err := other.Overview(context.Background(), sk.NewID[sk.TenantTag](), nil)
	if err != nil || ov2.Current.Revenue.Total != 0 || ov2.PastDue != 0 || ov2.AsOf != nil {
		t.Fatalf("other tenant = %+v %v", ov2, err)
	}
	pgtest.AssertAllTenantTablesHaveRLS(t, w.d)
}

// 差分の累積（daily_metrics）と契約ごとの状態（subscription_facts）の合計が一致する（ADR-R02）。
func TestCumulativeDeltasMatchFacts(t *testing.T) {
	w := newWorld(t)
	p1, p2 := w.price(980), w.price(1980)
	base := w.clock.Now().Add(-40 * 24 * time.Hour)
	for i := range 6 {
		sub := uuid.Must(uuid.NewV7())
		at := base.Add(time.Duration(i) * 24 * time.Hour)
		w.send(nil, "subscription.started", sub, 1, "active", p1, "", at)
		switch i % 3 {
		case 0:
			w.send(nil, "subscription.plan_changed", sub, 2, "active", p2, "", at.Add(48*time.Hour))
		case 1:
			w.send(nil, "subscription.past_due", sub, 2, "past_due", p1, "", at.Add(24*time.Hour))
			w.send(nil, "subscription.canceled", sub, 3, "canceled", p1, "dunning", at.Add(30*24*time.Hour))
		}
	}
	var mrr, paying int64
	err := w.d.Pool.Module("reporting").ReadTx(db.WithTenantScope(context.Background(), w.tenant), func(ctx context.Context, tx db.Tx) error {
		return tx.QueryRow(ctx, "SELECT coalesce(sum(mrr),0), count(*) FILTER (WHERE status IN ('active','past_due')) FROM reporting.subscription_facts").Scan(&mrr, &paying)
	})
	if err != nil {
		t.Fatal(err)
	}
	k := w.overview().Current
	if k.MRR != mrr || k.Paying != paying || mrr != 2*1980+2*980 {
		t.Fatalf("cumulative mrr=%d paying=%d, facts mrr=%d paying=%d", k.MRR, k.Paying, mrr, paying)
	}
}

func TestGoals(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	m := domain.MonthOf(w.svc.Today())
	g, err := w.svc.PutGoal(ctx, w.tenant, m, 10000, "staff", nil)
	if err != nil || g.Version != 1 {
		t.Fatalf("put = %+v %v", g, err)
	}
	stale := 0
	if _, err := w.svc.PutGoal(ctx, w.tenant, m, 20000, "staff", &stale); !errors.Is(err, app.ErrVersionConflict) {
		t.Fatalf("stale put: %v", err)
	}
	v := 1
	if g, err = w.svc.PutGoal(ctx, w.tenant, m, 20000, "staff", &v); err != nil || g.Version != 2 || g.Amount != 20000 {
		t.Fatalf("put v1 = %+v %v", g, err)
	}
	if _, err := w.svc.PutGoal(ctx, w.tenant, m, 0, "staff", nil); !errors.Is(err, app.ErrInvalidAmount) {
		t.Fatalf("zero: %v", err)
	}
	if _, err := w.svc.PutGoal(ctx, w.tenant, m.Add(13), 1, "staff", nil); !errors.Is(err, app.ErrInvalidMonth) {
		t.Fatalf("far future: %v", err)
	}
	if ov := w.overview(); ov.Goal == nil || ov.Goal.Amount != 20000 {
		t.Fatalf("goal = %+v", ov.Goal)
	}
	if err := w.svc.DeleteGoal(ctx, w.tenant, m); err != nil {
		t.Fatal(err)
	}
	if ov := w.overview(); ov.Goal != nil {
		t.Fatal("goal should be deleted")
	}
	if _, err := w.svc.Overview(ctx, w.tenant, ptr(m.Add(1))); !errors.Is(err, app.ErrInvalidMonth) {
		t.Fatalf("future month: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }
