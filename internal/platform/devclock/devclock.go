//go:build devtools

// Package devclock はローカルの業務時刻（実時刻 + offset）を持つ。devtools ビルドだけに含める（憲章 §5、P-ARC-8）。
// offset は platform.dev_clock に 1 行。プロセスごとに裏のループで 250ms ごとに読み直す（LISTEN と ack はしない）。
package devclock

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/observability"
	sk "subscription-platform/internal/sharedkernel"
)

// CacheTTL はプロセスごとのキャッシュの長さ。書き込みから最大この時間だけ古い時刻が使われる（E14）。
const CacheTTL = time.Second

// ErrBackward は時刻を戻そうとした（E13）。
var ErrBackward = errors.New("devclock: clock backward not allowed")

// Clock は sharedkernel.Clock を実装する。
type Clock struct {
	platform *db.ModuleDB
	real     func() time.Time

	once     sync.Once
	mu       sync.Mutex
	offset   time.Duration
	version  int64
	loadedAt time.Time
}

func New(pool *db.Pool) *Clock { return &Clock{platform: pool.Platform(), real: time.Now} }

// State は現在の状態。
type State struct {
	Now            time.Time  `json:"now"`
	Today          string     `json:"today"`
	OffsetSeconds  int64      `json:"offset_seconds"`
	Version        int64      `json:"version"`
	EffectiveAfter *time.Time `json:"effective_after,omitempty"`
}

// refreshInterval は裏のループが offset を読み直す間隔（CacheTTL より短くして、書き込みから 1 秒以内に反映する）。
const refreshInterval = 250 * time.Millisecond

// load は platform.dev_clock を読む。呼び出し元を待たせないよう、接続の取得を含めて短い期限を付ける。
func (c *Clock) load(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var offMs, ver int64
	err := c.platform.ReadTx(db.WithSystemScope(ctx, "devclock"), func(ctx context.Context, tx db.Tx) error {
		err := tx.QueryRow(ctx, `SELECT offset_ms, version FROM platform.dev_clock WHERE id = 1`).Scan(&offMs, &ver)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil {
		observability.Logger("platform.devclock").LogAttrs(ctx, slog.LevelWarn, "devclock.read_failed", observability.Err(err))
		return // 前の値を使い続ける
	}
	c.mu.Lock()
	c.offset, c.version, c.loadedAt = time.Duration(offMs)*time.Millisecond, ver, c.real()
	c.mu.Unlock()
}

// refresh は初回だけ同期で読み、以後は裏のループに任せる。Now() は DB の接続を待たない
// （トランザクションの中から Now() を呼ぶので、接続を待つとプールを使い切ったときにデッドロックする）。
func (c *Clock) refresh(ctx context.Context) {
	c.once.Do(func() {
		c.load(ctx)
		go func() {
			t := time.NewTicker(refreshInterval)
			defer t.Stop()
			for range t.C {
				c.load(context.Background())
			}
		}()
	})
}

// Now は業務時刻（UTC）。
func (c *Clock) Now() time.Time {
	c.refresh(context.Background())
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.real().Add(c.offset).UTC()
}

func (c *Clock) Today() sk.Date { return sk.DateOf(c.Now()) }

// Get は現在の状態を返す（GET /v1/dev/clock）。
func (c *Clock) Get(ctx context.Context) State {
	c.refresh(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.real().Add(c.offset).UTC()
	return State{Now: now, Today: sk.DateOf(now).String(), OffsetSeconds: int64(c.offset / time.Second), Version: c.version}
}

// Set は advance（進める長さ）か to（その時刻にする）のどちらか 1 つで時刻を進める（POST /v1/dev/clock）。
// 書き込んだらすぐ返す。他のプロセスは EffectiveAfter（応答時刻 + 1 秒）以降に新しい時刻を使う。
func (c *Clock) Set(ctx context.Context, advance *time.Duration, to *time.Time) (State, error) {
	var st State
	var from time.Time
	err := c.platform.InTx(db.WithSystemScope(ctx, "devclock"), func(ctx context.Context, tx db.Tx) error {
		var offMs, ver int64
		err := tx.QueryRow(ctx, `SELECT offset_ms, version FROM platform.dev_clock WHERE id = 1 FOR UPDATE`).Scan(&offMs, &ver)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("devclock: read: %w", err)
		}
		real := c.real()
		from = real.Add(time.Duration(offMs) * time.Millisecond)
		var target time.Time
		switch {
		case advance != nil:
			if *advance < 0 {
				return ErrBackward
			}
			target = from.Add(*advance)
		case to != nil:
			target = *to
		}
		if target.Before(from) {
			return ErrBackward
		}
		newOff := target.Sub(real)
		if newOff < 0 {
			newOff = 0
		}
		ver++
		if _, err := tx.Exec(ctx, `INSERT INTO platform.dev_clock (id, offset_ms, version, updated_at) VALUES (1, $1, $2, now())
			ON CONFLICT (id) DO UPDATE SET offset_ms = EXCLUDED.offset_ms, version = EXCLUDED.version, updated_at = now()`, newOff.Milliseconds(), ver); err != nil {
			return fmt.Errorf("devclock: write: %w", err)
		}
		c.mu.Lock()
		c.offset, c.version, c.loadedAt = time.Duration(newOff.Milliseconds())*time.Millisecond, ver, real
		c.mu.Unlock()
		now := real.Add(c.offset).UTC()
		eff := real.Add(CacheTTL).UTC()
		st = State{Now: now, Today: sk.DateOf(now).String(), OffsetSeconds: int64(c.offset / time.Second), Version: ver, EffectiveAfter: &eff}
		return nil
	})
	if err != nil {
		return State{}, err
	}
	observability.Logger("platform.devclock").LogAttrs(ctx, slog.LevelInfo, "dev.clock_changed",
		slog.Time("from", from.UTC()), slog.Time("to", st.Now), slog.Int64("offset_seconds", st.OffsetSeconds), slog.Int64("version", st.Version))
	return st, nil
}
