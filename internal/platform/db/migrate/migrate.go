// Package migrate はロールの準備（bootstrap）と、モジュールごとのスキーマのマイグレーション（goose）を行う（platform.md §6.1・§6.4）。
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

// Modules はロールとスキーマを作るモジュールの一覧（マイグレーションの順序でもある）。reporting は作らない。
var Modules = []string{"tenant", "customer", "catalog", "subscription", "billing", "payments", "orchestration", "integration", "audit"}

// Source はスキーマ 1 つ分のマイグレーション。
type Source struct {
	Schema string
	FS     fs.FS // ルートに *.sql がある
}

// Options は bootstrap の設定。
type Options struct {
	AppPassword string // sp_app のパスワード（空ならパスワードを設定しない）
}

func quoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Bootstrap はロール・スキーマ・既定の権限を作る。何度実行してもよい。
// 接続するユーザーは rds_superuser 相当（CREATEROLE・BYPASSRLS・DB の CREATE）でよく、スーパーユーザーは要らない。
func Bootstrap(ctx context.Context, url string, opts Options) error {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return fmt.Errorf("bootstrap: connect: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))

	var stmts []string
	role := func(name, attrs string) {
		stmts = append(stmts, fmt.Sprintf(`DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = %s) THEN CREATE ROLE %s %s; END IF;
END $$`, quoteLiteral(name), name, attrs))
	}
	role("sp_owner", "NOLOGIN")
	role("sp_app", "LOGIN NOINHERIT")
	if opts.AppPassword != "" {
		stmts = append(stmts, fmt.Sprintf("ALTER ROLE sp_app PASSWORD %s", quoteLiteral(opts.AppPassword)))
	}
	role("sp_platform", "NOLOGIN")
	role("sp_relay", "NOLOGIN BYPASSRLS")
	for _, m := range Modules {
		role("sp_"+m, "NOLOGIN NOBYPASSRLS")
		role("sp_"+m+"_sys", "NOLOGIN BYPASSRLS")
	}
	// 準備用のユーザーが sp_owner として振る舞えるようにする（PG16: 作成者にも SET を明示する）
	stmts = append(stmts, "GRANT sp_owner TO CURRENT_USER WITH INHERIT FALSE, SET TRUE")
	// sp_app は自分では権限を持たず、トランザクションの先頭で切り替えるだけ
	targets := []string{"sp_platform", "sp_relay"}
	for _, m := range Modules {
		targets = append(targets, "sp_"+m, "sp_"+m+"_sys")
	}
	for _, r := range targets {
		stmts = append(stmts, fmt.Sprintf("GRANT %s TO sp_app WITH INHERIT FALSE, SET TRUE", r))
	}
	stmts = append(stmts, "GRANT CONNECT ON DATABASE "+pgx.Identifier{dbName(ctx, conn)}.Sanitize()+" TO sp_app")

	schemas := append([]string{"platform"}, Modules...)
	for _, s := range schemas {
		stmts = append(stmts, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s AUTHORIZATION sp_owner", s))
	}
	// 以降は sp_owner として権限を設定する
	stmts = append(stmts, "SET ROLE sp_owner")
	allModuleRoles := []string{}
	for _, m := range Modules {
		allModuleRoles = append(allModuleRoles, "sp_"+m, "sp_"+m+"_sys")
	}
	stmts = append(stmts,
		"GRANT USAGE ON SCHEMA platform TO sp_platform, sp_relay, "+strings.Join(allModuleRoles, ", "),
		"ALTER DEFAULT PRIVILEGES IN SCHEMA platform GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO sp_platform",
	)
	for _, m := range Modules {
		own := fmt.Sprintf("sp_%s, sp_%s_sys", m, m)
		stmts = append(stmts,
			fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s, sp_relay", m, own),
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s", m, own),
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT USAGE, SELECT ON SEQUENCES TO %s", m, own),
		)
	}
	stmts = append(stmts, "RESET ROLE")

	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			return fmt.Errorf("bootstrap: %s: %w", firstLine(s), err)
		}
	}
	return nil
}

func dbName(ctx context.Context, conn *pgx.Conn) string {
	var n string
	_ = conn.QueryRow(ctx, "SELECT current_database()").Scan(&n)
	return n
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}

// openOwnerDB は接続ごとに SET ROLE sp_owner する database/sql の DB を開く（goose 用）。
func openOwnerDB(url string) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("migrate: parse url: %w", err)
	}
	return stdlib.OpenDB(*cfg, stdlib.OptionAfterConnect(func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE sp_owner")
		return err //nolint:wrapcheck
	})), nil
}

func provider(db *sql.DB, src Source) (*goose.Provider, error) {
	store, err := database.NewStore(database.DialectPostgres, src.Schema+".goose_db_version")
	if err != nil {
		return nil, fmt.Errorf("migrate: store: %w", err)
	}
	p, err := goose.NewProvider("", db, src.FS, goose.WithStore(store), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return nil, fmt.Errorf("migrate: provider %s: %w", src.Schema, err)
	}
	return p, nil
}

// Up は sources を順に最新まで上げる。
func Up(ctx context.Context, url string, sources []Source, log *slog.Logger) error {
	db, err := openOwnerDB(url)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, src := range sources {
		p, err := provider(db, src)
		if err != nil {
			return err
		}
		res, err := p.Up(ctx)
		if err != nil {
			return fmt.Errorf("migrate up %s: %w", src.Schema, err)
		}
		log.InfoContext(ctx, "migrate.applied", slog.String("schema", src.Schema), slog.Int("count", len(res)))
	}
	return nil
}

// Status は各スキーマの現在のバージョンを返す。
func Status(ctx context.Context, url string, sources []Source) (map[string]int64, error) {
	db, err := openOwnerDB(url)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	out := map[string]int64{}
	for _, src := range sources {
		p, err := provider(db, src)
		if err != nil {
			return nil, err
		}
		v, err := p.GetDBVersion(ctx)
		if err != nil {
			return nil, fmt.Errorf("migrate status %s: %w", src.Schema, err)
		}
		out[src.Schema] = v
	}
	return out, nil
}

// SubFS は embed.FS の "migrations" ディレクトリを Source 用に切り出す。
func SubFS(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
