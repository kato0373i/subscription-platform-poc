//go:build integration

package db_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/db/migrate"
	"subscription-platform/internal/platform/observability"
	"subscription-platform/internal/platform/testsupport/pgtest"
	sk "subscription-platform/internal/sharedkernel"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

// billing スキーマにテスト用のテナントの表を作る。
func withItems(t *testing.T, d *pgtest.DB) {
	t.Helper()
	d.AsOwner(t,
		`CREATE TABLE billing.items (id uuid PRIMARY KEY, tenant_id uuid NOT NULL, label text NOT NULL)`,
		`SELECT platform.enable_tenant_rls('billing.items')`,
		`SELECT platform.create_messaging_tables('billing')`,
	)
}

func insertItem(ctx context.Context, tx db.Tx, tenant sk.TenantID, label string) error {
	_, err := tx.Exec(ctx, `INSERT INTO billing.items (id, tenant_id, label) VALUES (gen_random_uuid(), $1, $2)`, tenant, label)
	return err
}

// AC-PLT-014
func TestRollbackDiscardsBusinessRowAndOutbox(t *testing.T) {
	d := pgtest.New(t)
	withItems(t, d)
	tenant := sk.NewID[sk.TenantTag]()
	ctx := db.WithTenantScope(context.Background(), tenant)
	boom := errors.New("boom")
	err := d.Pool.Module("billing").InTx(ctx, func(ctx context.Context, tx db.Tx) error {
		if err := insertItem(ctx, tx, tenant, "x"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO billing.outbox_events (id, event_type, tenant_id, aggregate_type, aggregate_id, aggregate_version, correlation_id, payload, occurred_at)
			VALUES (gen_random_uuid(), 'billing.test', $1, 'item', gen_random_uuid(), 1, 'c-1', '{}', now())`, tenant); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if n := d.QueryInt(t, `SELECT count(*) FROM billing.items`) + d.QueryInt(t, `SELECT count(*) FROM billing.outbox_events`); n != 0 {
		t.Fatalf("行が残った: %d", n)
	}
}

// AC-PLT-015
func TestTenantIsolation(t *testing.T) {
	d := pgtest.New(t)
	withItems(t, d)
	a, b := sk.NewID[sk.TenantTag](), sk.NewID[sk.TenantTag]()
	m := d.Pool.Module("billing")
	if err := m.InTx(db.WithTenantScope(context.Background(), a), func(ctx context.Context, tx db.Tx) error {
		return insertItem(ctx, tx, a, "a")
	}); err != nil {
		t.Fatal(err)
	}
	ctxB := db.WithTenantScope(context.Background(), b)
	var n int
	if err := m.ReadTx(ctxB, func(ctx context.Context, tx db.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM billing.items`).Scan(&n)
	}); err != nil || n != 0 {
		t.Fatalf("B から A の行が見えた: n=%d err=%v", n, err)
	}
	err := m.InTx(ctxB, func(ctx context.Context, tx db.Tx) error { return insertItem(ctx, tx, a, "forged") })
	if !db.IsInsufficientPrivilege(err) {
		t.Fatalf("A の tenant_id で INSERT できた: %v", err)
	}
	var upd int64
	_ = m.InTx(ctxB, func(ctx context.Context, tx db.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE billing.items SET label = 'hacked'`)
		upd = tag.RowsAffected()
		return err
	})
	if upd != 0 {
		t.Fatalf("B が A の行を更新できた: %d", upd)
	}
}

// AC-PLT-016
func TestNoScopeIsRejected(t *testing.T) {
	d := pgtest.New(t)
	called := false
	err := d.Pool.Module("billing").InTx(context.Background(), func(context.Context, db.Tx) error {
		called = true
		return nil
	})
	if !errors.Is(err, db.ErrNoScope) || called {
		t.Fatalf("err = %v called = %v", err, called)
	}
	ctx := db.WithSystemScope(db.WithTenantScope(context.Background(), sk.NewID[sk.TenantTag]()), "test")
	if err := d.Pool.Module("billing").InTx(ctx, func(context.Context, db.Tx) error { return nil }); !errors.Is(err, db.ErrConflictingScope) {
		t.Fatalf("system と tenant の併用 err = %v", err)
	}
}

// AC-PLT-017
func TestAssertAllTenantTablesHaveRLSDetectsMissing(t *testing.T) {
	d := pgtest.New(t)
	withItems(t, d)
	pgtest.AssertAllTenantTablesHaveRLS(t, d) // outbox_events は除外され、items は RLS がある
	d.AsOwner(t, `CREATE TABLE billing.forgotten (id uuid PRIMARY KEY, tenant_id uuid NOT NULL)`)
	rec := &recordingTB{TB: t}
	pgtest.AssertAllTenantTablesHaveRLS(rec, d)
	if !rec.failed || !strings.Contains(rec.msg, "billing.forgotten") {
		t.Fatalf("RLS の付け忘れを検出しなかった: %q", rec.msg)
	}
}

type recordingTB struct {
	testing.TB
	failed bool
	msg    string
}

func (r *recordingTB) Errorf(format string, args ...any) {
	r.failed = true
	r.msg = strings.TrimSpace(strings.ReplaceAll(format, "%s", "")) + " " + strings.TrimSpace(sprint(args...))
}

func sprint(args ...any) string {
	var b strings.Builder
	for _, a := range args {
		if s, ok := a.(string); ok {
			b.WriteString(s)
		}
	}
	return b.String()
}

// AC-PLT-018
func TestModuleCannotReadOtherSchema(t *testing.T) {
	d := pgtest.New(t)
	d.AsOwner(t, `CREATE TABLE subscription.subscriptions (id uuid PRIMARY KEY, tenant_id uuid NOT NULL)`,
		`SELECT platform.enable_tenant_rls('subscription.subscriptions')`)
	err := d.Pool.Module("billing").ReadTx(db.WithSystemScope(context.Background(), "test"), func(ctx context.Context, tx db.Tx) error {
		_, err := tx.Exec(ctx, `SELECT * FROM subscription.subscriptions`)
		return err
	})
	if !db.IsInsufficientPrivilege(err) {
		t.Fatalf("err = %v", err)
	}
}

// AC-PLT-059
func TestAfterCommitHooks(t *testing.T) {
	d := pgtest.New(t)
	ctx := db.WithTenantScope(context.Background(), sk.NewID[sk.TenantTag]())
	var calls []int
	_ = d.Pool.Module("billing").InTx(ctx, func(ctx context.Context, _ db.Tx) error {
		db.AfterCommit(ctx, func(context.Context) { calls = append(calls, 1) })
		db.AfterCommit(ctx, func(context.Context) { calls = append(calls, 2) })
		return nil
	})
	if len(calls) != 2 || calls[0] != 1 || calls[1] != 2 {
		t.Fatalf("calls = %v", calls)
	}
	calls = nil
	_ = d.Pool.Module("billing").InTx(ctx, func(ctx context.Context, _ db.Tx) error {
		db.AfterCommit(ctx, func(context.Context) { calls = append(calls, 1) })
		return errors.New("rollback")
	})
	if len(calls) != 0 {
		t.Fatalf("ロールバックでフックが呼ばれた: %v", calls)
	}
}

type sqlRecorder struct {
	mu   sync.Mutex
	sqls []string
}

func (r *sqlRecorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	r.mu.Lock()
	r.sqls = append(r.sqls, d.SQL)
	r.mu.Unlock()
	return ctx
}
func (r *sqlRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// AC-PLT-060
func TestTxSetupIsSingleStatement(t *testing.T) {
	rec := &sqlRecorder{}
	d := pgtest.New(t, func(c *db.Config) { c.Tracer = rec })
	tenant := sk.NewID[sk.TenantTag]()
	var user, tid string
	err := d.Pool.Module("billing").InTx(db.WithTenantScope(context.Background(), tenant), func(ctx context.Context, tx db.Tx) error {
		return tx.QueryRow(ctx, `SELECT current_user, current_setting('app.tenant_id')`).Scan(&user, &tid)
	})
	if err != nil {
		t.Fatal(err)
	}
	if user != "sp_billing" || tid != tenant.String() {
		t.Fatalf("current_user = %s, app.tenant_id = %s", user, tid)
	}
	var inTx []string
	for _, s := range rec.sqls {
		if s != "begin" && s != "commit" && !strings.HasPrefix(s, "begin ") {
			inTx = append(inTx, s)
		}
	}
	if len(inTx) != 2 || inTx[0] != db.SetupSQL || strings.Count(inTx[0], "set_config") != 5 {
		t.Fatalf("SQL = %q", rec.sqls)
	}
	for _, s := range rec.sqls {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(s)), "SET ") {
			t.Fatalf("SET 文が使われた: %q", s)
		}
	}
	// system スコープは BYPASSRLS のロール
	_ = d.Pool.Module("billing").ReadTx(db.WithSystemScope(context.Background(), "test"), func(ctx context.Context, tx db.Tx) error {
		return tx.QueryRow(ctx, `SELECT current_user`).Scan(&user)
	})
	if user != "sp_billing_sys" {
		t.Fatalf("system の current_user = %s", user)
	}
}

// AC-PLT-066（DB の部分）: sp_e2e_reader は書き込めず、RLS を越えて読める
func TestE2EReader(t *testing.T) {
	d := pgtest.New(t)
	withItems(t, d)
	tenant := sk.NewID[sk.TenantTag]()
	if err := d.Pool.Module("billing").InTx(db.WithTenantScope(context.Background(), tenant), func(ctx context.Context, tx db.Tx) error {
		return insertItem(ctx, tx, tenant, "x")
	}); err != nil {
		t.Fatal(err)
	}
	if n := d.QueryInt(t, `SELECT count(*) FROM pg_roles WHERE rolname = 'sp_e2e_reader'`); n != 0 {
		t.Fatal("本番のマイグレーションの後に sp_e2e_reader がある")
	}
	ctx := context.Background()
	if err := migrate.CreateE2EReader(ctx, d.AdminURL, pgtest.ReaderPass, append([]string{"platform"}, migrate.Modules...)); err != nil {
		t.Fatal(err)
	}
	readerURL := strings.Replace(d.AppURL, "sp_app:sp_app", "sp_e2e_reader:"+pgtest.ReaderPass, 1)
	conn, err := pgx.Connect(ctx, readerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM billing.items`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("RLS を越えて読めない: n=%d err=%v", n, err)
	}
	if _, err := conn.Exec(ctx, `UPDATE billing.items SET label = 'y'`); err == nil {
		t.Fatal("sp_e2e_reader で書き込めた")
	}
}

// AC-PLT-079
func TestSlowQueryLog(t *testing.T) {
	var buf bytes.Buffer
	if _, err := observability.Setup(observability.LogConfig{Service: "api", Env: "test", Stdout: &buf}); err != nil {
		t.Fatal(err)
	}
	d := pgtest.New(t, func(c *db.Config) { c.SlowQuery = 200 * time.Millisecond })
	err := d.Pool.Module("billing").ReadTx(db.WithSystemScope(context.Background(), "test"), func(ctx context.Context, tx db.Tx) error {
		_, err := tx.Exec(ctx, "-- name: SleepForTest :exec\nSELECT pg_sleep(0.3), $1::text", "secret-arg-value")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Count(out, `"msg":"db.slow_query"`) != 1 || !strings.Contains(out, `"query":"SleepForTest"`) || !strings.Contains(out, `"duration_ms"`) {
		t.Fatalf("log = %s", out)
	}
	if strings.Contains(out, "secret-arg-value") {
		t.Fatal("引数がログに出た")
	}
	_ = slog.Default()
}
