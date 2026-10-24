// Package db は pgxpool・トランザクション・スコープ（ロールの切り替えと RLS）を受け持つ（platform.md §3.2・§4）。
package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"subscription-platform/internal/platform/observability"
)

// Tx はトランザクション。
type Tx = pgx.Tx

// Config は接続の設定。
type Config struct {
	URL       string
	MaxConns  int32 // api 20 / worker 10
	MinConns  int32
	SlowQuery time.Duration
	Tracer    pgx.QueryTracer // 追加のトレーサー（テストで実行した SQL を記録する）
}

// Pool は 1 プロセスに 1 つの接続プール。
type Pool struct{ pgx *pgxpool.Pool }

// Open はプールを作り、接続を確かめる。
func Open(ctx context.Context, cfg Config) (*Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("db: parse url: %w", err)
	}
	if cfg.MaxConns > 0 {
		pc.MaxConns = cfg.MaxConns
	}
	pc.MinConns = cfg.MinConns
	var tracers multiTracer
	if cfg.SlowQuery > 0 {
		tracers = append(tracers, observability.PgxTracer(cfg.SlowQuery))
	}
	if cfg.Tracer != nil {
		tracers = append(tracers, cfg.Tracer)
	}
	if len(tracers) > 0 {
		pc.ConnConfig.Tracer = tracers
	}
	p, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	return &Pool{pgx: p}, nil
}

// Ping は DB に届くかを確かめる（/readyz）。
func (p *Pool) Ping(ctx context.Context) error {
	if err := p.pgx.Ping(ctx); err != nil {
		return fmt.Errorf("db: ping: %w", err)
	}
	return nil
}

// Close はプールを閉じる。
func (p *Pool) Close() { p.pgx.Close() }

// PGX はマイグレーションとテストの補助だけが使う生のプール。モジュールは使わない。
func (p *Pool) PGX() *pgxpool.Pool { return p.pgx }

// Module は name（スキーマ名 = ロール名の接尾辞）の ModuleDB を返す。
func (p *Pool) Module(name string) *ModuleDB {
	return &ModuleDB{pool: p, name: name, role: "sp_" + name, sysRole: "sp_" + name + "_sys"}
}

// Platform は platform スキーマ用（sp_platform。RLS はない）。
func (p *Pool) Platform() *ModuleDB {
	return &ModuleDB{pool: p, name: "platform", role: "sp_platform", sysRole: "sp_platform", scopeOptional: true}
}

// Relay は Outbox の中継用（sp_relay。全スキーマの outbox_events を読み書きする）。
func (p *Pool) Relay() *ModuleDB {
	return &ModuleDB{pool: p, name: "relay", role: "sp_relay", sysRole: "sp_relay", scopeOptional: true}
}

// ModuleDB はモジュールのロールでトランザクションを張る。
type ModuleDB struct {
	pool          *Pool
	name          string
	role, sysRole string
	scopeOptional bool
}

// Name はモジュール名（スキーマ名）。
func (m *ModuleDB) Name() string { return m.name }

type txOptions struct {
	iso         pgx.TxIsoLevel
	retries     int
	stmtTimeout time.Duration
	lockTimeout time.Duration
	readOnly    bool
}

// TxOption はトランザクションの設定。
type TxOption func(*txOptions)

func WithIsolation(l pgx.TxIsoLevel) TxOption { return func(o *txOptions) { o.iso = l } }

// WithRetryOnSerialization は 40001/40P01 だけを max 回まで再試行する（既定 0）。
func WithRetryOnSerialization(max int) TxOption { return func(o *txOptions) { o.retries = max } }

func WithStatementTimeout(d time.Duration) TxOption { return func(o *txOptions) { o.stmtTimeout = d } }

func WithLockTimeout(d time.Duration) TxOption { return func(o *txOptions) { o.lockTimeout = d } }

// InTx はスコープに応じたロールでトランザクションを張り、fn を呼ぶ。コミット後に AfterCommit のフックを順に呼ぶ。
func (m *ModuleDB) InTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error, opts ...TxOption) error {
	return m.run(ctx, fn, false, opts)
}

// ReadTx は READ ONLY のトランザクション。将来はリーダーのプールに向ける。
func (m *ModuleDB) ReadTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error, opts ...TxOption) error {
	return m.run(ctx, fn, true, opts)
}

// SetupSQL はトランザクションの先頭で実行する 1 文（AC-PLT-060）。
const SetupSQL = `SELECT set_config('role', $1, true), set_config('statement_timeout', $2, true), set_config('lock_timeout', $3, true), set_config('app.tenant_id', $4, true), set_config('app.customer_account_id', $5, true)`

func (m *ModuleDB) run(ctx context.Context, fn func(context.Context, Tx) error, readOnly bool, opts []TxOption) error {
	o := txOptions{iso: pgx.ReadCommitted, stmtTimeout: 15 * time.Second, lockTimeout: 5 * time.Second, readOnly: readOnly}
	for _, f := range opts {
		f(&o)
	}
	scope := scopeOf(ctx)
	if m.scopeOptional {
		scope = Scope{System: true, SystemReason: m.name} // 固定ロール（RLS のない表だけを触る）は呼び出し元のスコープに関係なく動く
	} else {
		if err := scope.validate(); err != nil {
			observability.Logger("platform.db").LogAttrs(ctx, slog.LevelError, "db.scope_missing",
				slog.String("module", m.name), observability.Err(err))
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		err := m.once(ctx, scope, fn, o)
		if err == nil || attempt >= o.retries || !isSerializationFailure(err) {
			return err
		}
	}
}

func (m *ModuleDB) once(ctx context.Context, scope Scope, fn func(context.Context, Tx) error, o txOptions) (err error) {
	access := pgx.ReadWrite
	if o.readOnly {
		access = pgx.ReadOnly
	}
	tx, err := m.pool.pgx.BeginTx(ctx, pgx.TxOptions{IsoLevel: o.iso, AccessMode: access})
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	role := m.role
	if scope.System {
		role = m.sysRole
	}
	var tenant, customer string
	if scope.Tenant != nil {
		tenant = scope.Tenant.String()
	}
	if scope.Customer != nil {
		customer = scope.Customer.String()
	}
	if _, err = tx.Exec(ctx, SetupSQL, role, ms(o.stmtTimeout), ms(o.lockTimeout), tenant, customer); err != nil {
		return fmt.Errorf("db: setup tx (role %s): %w", role, err)
	}
	hooks := &afterCommitHooks{}
	if err = fn(context.WithValue(ctx, hooksKey{}, hooks), tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit: %w", err)
	}
	for _, h := range hooks.fns {
		h(context.WithoutCancel(ctx))
	}
	return nil
}

func ms(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }

func isSerializationFailure(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && (pg.Code == "40001" || pg.Code == "40P01")
}

// IsUniqueViolation は 23505 か。
func IsUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// IsInsufficientPrivilege は 42501 か（他スキーマへのアクセス・RLS の WITH CHECK 違反）。
func IsInsufficientPrivilege(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "42501"
}

type hooksKey struct{}

type afterCommitHooks struct{ fns []func(context.Context) }

// AfterCommit は InTx の中で登録し、コミット成功後に登録順に実行する（ロールバック時は捨てる）。
// トランザクションの外で呼ばれたら、すぐに実行する。
func AfterCommit(ctx context.Context, f func(ctx context.Context)) {
	if h, ok := ctx.Value(hooksKey{}).(*afterCommitHooks); ok {
		h.fns = append(h.fns, f)
		return
	}
	f(ctx)
}

type multiTracer []pgx.QueryTracer

func (m multiTracer) TraceQueryStart(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	for _, t := range m {
		ctx = t.TraceQueryStart(ctx, c, d)
	}
	return ctx
}

func (m multiTracer) TraceQueryEnd(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryEndData) {
	for _, t := range m {
		t.TraceQueryEnd(ctx, c, d)
	}
}
