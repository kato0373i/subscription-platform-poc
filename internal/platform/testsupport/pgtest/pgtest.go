//go:build integration

// Package pgtest は結合テスト用に本物の PostgreSQL 16 を testcontainers で起動する（platform.md §10.2）。
// パッケージごとに 1 コンテナを起動し、テストごとにテンプレート DB から複製する。
package pgtest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/db/migrate"
)

// Image は MiniStack の RDS が使うものと同じ PostgreSQL 16 のイメージ（compose.yaml の profile db と揃える）。
const Image = "postgres:16-alpine@sha256:f4610795587492cd4fb887c2dc3de301b813278bb6f616992061141b5f15ac36"

const (
	adminUser    = "sp_admin"
	adminPass    = "sp_admin"
	appPass      = "sp_app"
	ReaderPass   = "sp_e2e_reader" //nolint:gosec // テスト用のコンテナの中だけで使う
	templateName = "sp_template"
)

var (
	superURL string // postgres（スーパーユーザー）
	counter  atomic.Int64
	atExit   []func()
)

// AtExit は Main の終了時（コンテナを止める前）に呼ぶ関数を登録する。
func AtExit(f func()) { atExit = append(atExit, f) }

// Main はテストパッケージの TestMain から呼ぶ。コンテナを起動し、テンプレート DB に bootstrap と sources のマイグレーションを流す。
func Main(m *testing.M, sources ...migrate.Source) {
	ctx := context.Background()
	c, err := tcpostgres.Run(ctx, Image,
		tcpostgres.WithDatabase("postgres"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pgtest: start postgres:", err)
		os.Exit(1)
	}
	superURL, err = c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintln(os.Stderr, "pgtest:", err)
		os.Exit(1)
	}
	if err := prepareTemplate(ctx, sources); err != nil {
		fmt.Fprintln(os.Stderr, "pgtest: prepare template:", err)
		_ = c.Terminate(ctx)
		os.Exit(1)
	}
	code := m.Run()
	for _, f := range atExit {
		f()
	}
	_ = c.Terminate(ctx)
	os.Exit(code)
}

// withUser は superURL の接続先のユーザーと DB を差し替える。
func withUser(base, user, pass, dbname string) string {
	u, _ := url.Parse(base)
	u.User = url.UserPassword(user, pass)
	u.Path = "/" + dbname
	return u.String()
}

func execSuper(ctx context.Context, dbname string, stmts ...string) error {
	conn, err := pgx.Connect(ctx, withUser(superURL, "postgres", "postgres", dbname))
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	return nil
}

func prepareTemplate(ctx context.Context, sources []migrate.Source) error {
	// rds_superuser 相当: スーパーユーザーではなく、CREATEROLE・BYPASSRLS・DB の CREATE だけを持つ（§8.4）
	if err := execSuper(ctx, "postgres",
		fmt.Sprintf("CREATE ROLE %s LOGIN CREATEROLE BYPASSRLS PASSWORD '%s'", adminUser, adminPass),
		"CREATE DATABASE "+templateName,
		fmt.Sprintf("GRANT CREATE, CONNECT ON DATABASE %s TO %s", templateName, adminUser),
	); err != nil {
		return err
	}
	adminURL := withUser(superURL, adminUser, adminPass, templateName)
	if err := migrate.Bootstrap(ctx, adminURL, migrate.Options{AppPassword: appPass}); err != nil {
		return err
	}
	all := append([]migrate.Source{{Schema: "platform", FS: migrate.SubFS(db.PlatformMigrations, "migrations")}}, sources...)
	if err := migrate.Up(ctx, adminURL, all, discardLogger()); err != nil {
		return err
	}
	return nil
}

// DB はテスト 1 つ分の DB。
type DB struct {
	Name     string
	AdminURL string // sp_admin（bootstrap・マイグレーションを流すユーザー）
	AppURL   string // sp_app（実行時の接続ユーザー）
	SuperURL string
	Pool     *db.Pool // sp_app のプール
}

// New はテンプレートから DB を複製し、sp_app のプールを開く。テスト終了時に閉じる。
func New(t testing.TB, opts ...func(*db.Config)) *DB {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("t_%d_%d", os.Getpid(), counter.Add(1))
	if err := execSuper(ctx, "postgres",
		fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, templateName),
		fmt.Sprintf("GRANT CREATE, CONNECT ON DATABASE %s TO %s", name, adminUser),
	); err != nil {
		t.Fatal(err)
	}
	d := &DB{
		Name:     name,
		AdminURL: withUser(superURL, adminUser, adminPass, name),
		AppURL:   withUser(superURL, "sp_app", appPass, name),
		SuperURL: withUser(superURL, "postgres", "postgres", name),
	}
	cfg := db.Config{URL: d.AppURL, MaxConns: 20}
	for _, o := range opts {
		o(&cfg)
	}
	p, err := db.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	d.Pool = p
	t.Cleanup(p.Close)
	return d
}

// AsOwner は sp_owner としてテスト用の DDL を流す（テスト用の表を作るため）。
func (d *DB) AsOwner(t testing.TB, stmts ...string) {
	t.Helper()
	d.exec(t, d.AdminURL, append([]string{"SET ROLE sp_owner"}, stmts...)...)
}

// AsSuper はスーパーユーザーで SQL を流す（権限を剥がすなど、テストの準備だけに使う）。
func (d *DB) AsSuper(t testing.TB, stmts ...string) {
	t.Helper()
	d.exec(t, d.SuperURL, stmts...)
}

func (d *DB) exec(t testing.TB, u string, stmts ...string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// SuperPool はスーパーユーザーのプール（検証の読み取りだけに使う）。
func (d *DB) SuperPool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.New(context.Background(), d.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

// QueryInt はスーパーユーザーで数値を 1 つ読む。
func (d *DB) QueryInt(t testing.TB, sql string, args ...any) int64 {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, d.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var n int64
	if err := conn.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// AssertAllTenantTablesHaveRLS は tenant_id 列を持つ全テーブルに、RLS の ENABLE・FORCE と tenant_isolation ポリシーがあるかを確かめる。
// outbox_events は設計上 RLS をかけない（sp_relay と Writer だけが触る）ので除く。
func AssertAllTenantTablesHaveRLS(t testing.TB, d *DB) {
	t.Helper()
	rows, err := d.SuperPool(t).Query(context.Background(), `
SELECT n.nspname || '.' || c.relname, c.relrowsecurity, c.relforcerowsecurity,
       EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid = c.oid AND p.polname = 'tenant_isolation')
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_attribute a ON a.attrelid = c.oid AND a.attname = 'tenant_id' AND NOT a.attisdropped
WHERE c.relkind IN ('r', 'p') AND n.nspname = ANY($1) AND c.relname <> 'outbox_events'
ORDER BY 1`, migrate.Modules)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var bad []string
	for rows.Next() {
		var name string
		var enabled, forced, policy bool
		if err := rows.Scan(&name, &enabled, &forced, &policy); err != nil {
			t.Fatal(err)
		}
		if !enabled || !forced || !policy {
			bad = append(bad, fmt.Sprintf("%s(enable=%v force=%v policy=%v)", name, enabled, forced, policy))
		}
	}
	if len(bad) > 0 {
		t.Errorf("RLS がかかっていないテナントの表: %s", strings.Join(bad, ", "))
	}
}
