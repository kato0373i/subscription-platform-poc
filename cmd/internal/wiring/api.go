package wiring

import (
	"context"
	"fmt"

	"subscription-platform/internal/platform/httpx"
	"subscription-platform/web/checkout"
)

// RunAPI は api プロセス：全モジュールの HTTP、/v1/dev/*（devtools）、監査ミドルウェア。ジョブは投入だけ。
func RunAPI(ctx context.Context, b *Base) error {
	mods, err := modules(ctx, b, "api")
	if err != nil {
		return err
	}
	verifier, devKey, err := newVerifier(ctx, b)
	if err != nil {
		return err
	}
	r := httpx.NewRouter(httpx.RouterConfig{Clock: b.Clock, Pool: b.Pool}, verifier, ports(mods), httpx.NewPGIdempotencyStore(b.Pool))
	for _, m := range mods {
		if m.MountHTTP == nil {
			continue
		}
		if err := m.MountHTTP(r); err != nil {
			return fmt.Errorf("mount %s: %w", m.Name, err)
		}
	}
	for pattern, h := range checkout.Routes() {
		r.HandleUnauthenticated(pattern, h) // 最小の申込ページ（payments.md §7.10）
	}
	if err := registerDev(r, b, devKey); err != nil {
		return fmt.Errorf("mount dev api: %w", err)
	}
	return serveHTTP(ctx, b.Cfg.HTTPAddr, r.Handler(), r.SetShuttingDown)
}
