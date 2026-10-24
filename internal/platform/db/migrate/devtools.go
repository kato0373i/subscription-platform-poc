package migrate

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// CreateE2EReader は読み取り専用の確認用ロール sp_e2e_reader を作る（P-TST-3）。
// devtools ビルドの cmd/migrate だけが呼ぶので、本番 DB には存在しない。schemas は付与の対象。
func CreateE2EReader(ctx context.Context, url, password string, schemas []string) error {
	if password == "" {
		return fmt.Errorf("e2e reader: password is required")
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return fmt.Errorf("e2e reader: connect: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))
	stmts := []string{
		`DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sp_e2e_reader') THEN CREATE ROLE sp_e2e_reader LOGIN BYPASSRLS; END IF;
END $$`,
		"ALTER ROLE sp_e2e_reader PASSWORD " + quoteLiteral(password),
		"ALTER ROLE sp_e2e_reader SET default_transaction_read_only = on",
		"GRANT CONNECT ON DATABASE " + pgx.Identifier{dbName(ctx, conn)}.Sanitize() + " TO sp_e2e_reader",
		"SET ROLE sp_owner",
	}
	for _, s := range schemas {
		stmts = append(stmts,
			fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO sp_e2e_reader", s),
			fmt.Sprintf("GRANT SELECT ON ALL TABLES IN SCHEMA %s TO sp_e2e_reader", s),
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT SELECT ON TABLES TO sp_e2e_reader", s),
		)
	}
	stmts = append(stmts, "RESET ROLE")
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			return fmt.Errorf("e2e reader: %s: %w", strings.SplitN(s, "\n", 2)[0], err)
		}
	}
	return nil
}
