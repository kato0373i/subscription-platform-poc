package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"subscription-platform/internal/platform/db"
	sk "subscription-platform/internal/sharedkernel"
)

// BreakerConfig はサーキットブレーカーとトークンバケットの設定（§4.4）。
type BreakerConfig struct {
	FailureThreshold int           // 連続失敗（unknown・接続エラー）がこの回数で開く。既定 5
	OpenFor          time.Duration // 開いている時間。既定 30 秒
	RatePerSecond    float64       // タスクごとのトークンバケット。0 なら制限しない
	Burst            int
}

// Guard はブレーカーの状態を DB（provider_circuit_breakers）に持ち、全タスクで共有する（P-ARC-11 ④）。
type Guard struct {
	db    *db.ModuleDB
	clock sk.Clock
	cfg   BreakerConfig

	mu      sync.Mutex
	buckets map[ProviderID]*bucket
}

// NewGuard は横断機能を作る。
func NewGuard(m *db.ModuleDB, clock sk.Clock, cfg BreakerConfig) *Guard {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.OpenFor <= 0 {
		cfg.OpenFor = 30 * time.Second
	}
	return &Guard{db: m, clock: clock, cfg: cfg, buckets: map[ProviderID]*bucket{}}
}

// Wrap はレジストリの WithDecorator に渡す。
func (g *Guard) Wrap(p PaymentProvider) PaymentProvider { return &guarded{PaymentProvider: p, g: g} }

func (g *Guard) scope(ctx context.Context) context.Context {
	return db.WithUnscoped(ctx, "circuit_breaker")
}

// Allow は送信してよいか。ブレーカーが開いている・レート超過なら ErrUnavailable。
func (g *Guard) Allow(ctx context.Context, p ProviderID) error {
	var openUntil *time.Time
	err := g.db.ReadTx(g.scope(ctx), func(ctx context.Context, tx db.Tx) error {
		err := tx.QueryRow(ctx, `SELECT open_until FROM payments.provider_circuit_breakers WHERE provider = $1`, string(p)).Scan(&openUntil)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("payments: read breaker: %w", err)
	}
	if openUntil != nil && g.clock.Now().Before(*openUntil) {
		return fmt.Errorf("%w: circuit open until %s", ErrUnavailable, openUntil.UTC().Format(time.RFC3339))
	}
	if !g.take(p) {
		return fmt.Errorf("%w: rate limited", ErrUnavailable)
	}
	return nil
}

// Record は結果を記録する。unknown・接続エラーは失敗、それ以外（成功・拒否）はリセット。
func (g *Guard) Record(ctx context.Context, p ProviderID, failed bool) {
	now := g.clock.Now()
	_ = g.db.InTx(g.scope(ctx), func(ctx context.Context, tx db.Tx) error {
		if !failed {
			_, err := tx.Exec(ctx, `INSERT INTO payments.provider_circuit_breakers (provider, consecutive_failures, open_until, updated_at) VALUES ($1, 0, NULL, $2)
				ON CONFLICT (provider) DO UPDATE SET consecutive_failures = 0, open_until = NULL, updated_at = EXCLUDED.updated_at`, string(p), now)
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `INSERT INTO payments.provider_circuit_breakers (provider, consecutive_failures, updated_at) VALUES ($1, 1, $2)
			ON CONFLICT (provider) DO UPDATE SET consecutive_failures = payments.provider_circuit_breakers.consecutive_failures + 1, updated_at = EXCLUDED.updated_at
			RETURNING consecutive_failures`, string(p), now).Scan(&n); err != nil {
			return err
		}
		if n >= g.cfg.FailureThreshold {
			_, err := tx.Exec(ctx, `UPDATE payments.provider_circuit_breakers SET open_until = $2, consecutive_failures = 0 WHERE provider = $1`, string(p), now.Add(g.cfg.OpenFor))
			return err
		}
		return nil
	})
}

// bucket はタスクごとのトークンバケット（メモリ上の安全網）。
type bucket struct {
	tokens float64
	last   time.Time
}

func (g *Guard) take(p ProviderID) bool {
	if g.cfg.RatePerSecond <= 0 {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now() //nolint:forbidigo // レート制限は実時間で数える（業務時刻ではない）
	b, ok := g.buckets[p]
	if !ok {
		b = &bucket{tokens: float64(max(g.cfg.Burst, 1)), last: now}
		g.buckets[p] = b
	}
	b.tokens = min(float64(max(g.cfg.Burst, 1)), b.tokens+now.Sub(b.last).Seconds()*g.cfg.RatePerSecond)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// guarded はブレーカーとレート制限で包んだアダプター。任意の能力は Unwrap で元のアダプターから取る。
type guarded struct {
	PaymentProvider
	g *Guard
}

// Allow は送信してよいか（登録の前の確認に使う）。
func (w *guarded) Allow(ctx context.Context) error { return w.g.Allow(ctx, w.Key().Provider) }

// Unwrap は元のアダプター（任意の能力の型アサーション用）。
func (w *guarded) Unwrap() PaymentProvider { return w.PaymentProvider }

func (w *guarded) Collect(ctx context.Context, in CollectInput) (Outcome, error) {
	p := w.Key().Provider
	if err := w.g.Allow(ctx, p); err != nil {
		return Outcome{}, err
	}
	out, err := w.PaymentProvider.Collect(ctx, in)
	switch {
	case errors.Is(err, ErrUnavailable):
		w.g.Record(ctx, p, true)
	case err != nil && !errors.Is(err, ErrNotSent):
		w.g.Record(ctx, p, true)
	case err == nil:
		w.g.Record(ctx, p, out.Status == OutcomeUnknown)
	}
	return out, err
}

func (w *guarded) GetStatus(ctx context.Context, in StatusInput) (Outcome, error) {
	if err := w.g.Allow(ctx, w.Key().Provider); err != nil {
		return Outcome{}, err
	}
	return w.PaymentProvider.GetStatus(ctx, in)
}

// Capability は任意の能力を取り出す（包まれていれば元のアダプターから）。
func Capability[T any](p PaymentProvider) (T, bool) {
	if w, ok := p.(interface{ Unwrap() PaymentProvider }); ok {
		p = w.Unwrap()
	}
	t, ok := p.(T)
	return t, ok
}
