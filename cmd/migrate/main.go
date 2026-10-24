// Command migrate はロールの準備（bootstrap）とスキーマのマイグレーション（up・status）を行う。ECS のワンショット実行。
//
//	migrate bootstrap   # ロール・スキーマ・既定の権限（rds_superuser 相当のユーザーで実行）
//	migrate up          # platform → tenant → … → audit。devtools ビルドでは sp_e2e_reader も作る
//	migrate status
//
// 接続先は MIGRATE_DATABASE_URL（なければ DATABASE_URL）。sp_app のパスワードは SP_APP_PASSWORD。
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"subscription-platform/cmd/internal/wiring"
	"subscription-platform/internal/platform/config"
	"subscription-platform/internal/platform/db/migrate"
	"subscription-platform/internal/platform/observability"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: migrate bootstrap|up|status") //nolint:forbidigo
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	url := os.Getenv("MIGRATE_DATABASE_URL")
	if url == "" {
		url = os.Getenv("DATABASE_URL")
	}
	if url == "" {
		fmt.Fprintln(os.Stderr, "migrate: MIGRATE_DATABASE_URL or DATABASE_URL is required") //nolint:forbidigo
		os.Exit(2)
	}
	appEnv := os.Getenv("APP_ENV")
	if appEnv == "" {
		appEnv = "local"
	}
	sinks := []string{"stdout"}
	if appEnv == "local" || appEnv == "test" {
		sinks = append(sinks, "file")
	}
	lg, err := observability.Setup(observability.LogConfig{Service: "migrate", Version: config.Version, Env: appEnv, Sinks: sinks, Dir: envOr("LOG_DIR", ".local/logs"), Level: envOr("LOG_LEVEL", "info")})
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err) //nolint:forbidigo
		os.Exit(2)
	}
	defer lg.Close()
	log := lg.ForModule("platform.migrate")
	if err := run(ctx, os.Args[1], url); err != nil {
		log.ErrorContext(ctx, "migrate.failed", observability.Err(err))
		os.Exit(1)
	}
	log.InfoContext(ctx, "migrate.completed", "command", os.Args[1])
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func run(ctx context.Context, cmd, url string) error {
	switch cmd {
	case "bootstrap":
		return migrate.Bootstrap(ctx, url, migrate.Options{AppPassword: os.Getenv("SP_APP_PASSWORD")})
	case "up":
		if err := migrate.Up(ctx, url, wiring.MigrationSources(), observability.Logger("platform.migrate")); err != nil {
			return err
		}
		if config.DevtoolsBuild {
			// devtools のマイグレーション（P-TST-3）: 本番のビルドには含まれないので、本番 DB には存在しない
			return migrate.CreateE2EReader(ctx, url, os.Getenv("E2E_READER_PASSWORD"), append([]string{"platform"}, migrate.Modules...))
		}
		return nil
	case "status":
		st, err := migrate.Status(ctx, url, wiring.MigrationSources())
		if err != nil {
			return err
		}
		for s, v := range st {
			fmt.Printf("%-14s %d\n", s, v) //nolint:forbidigo // CLI の出力
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
}
