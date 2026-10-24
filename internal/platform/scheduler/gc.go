package scheduler

import (
	"context"
	"fmt"

	"subscription-platform/internal/platform/db"
)

// Platform のジョブ（§8.2）: 期限切れの Idempotency・送信済みの Outbox・古い Inbox・完了したカーソルを消す。
// 基盤の都合なので、業務時刻ではなく DB の実時刻 now() で判定する。

type gcJob struct {
	name string
	run  func(ctx context.Context) (Stats, error)
}

func (g gcJob) Name() string                                  { return g.name }
func (g gcJob) Run(ctx context.Context, _ Run) (Stats, error) { return g.run(ctx) }

func exec(ctx context.Context, m *db.ModuleDB, sql string) (int64, error) {
	var n int64
	err := m.InTx(db.WithSystemScope(ctx, "gc"), func(ctx context.Context, tx db.Tx) error {
		tag, err := tx.Exec(ctx, sql)
		n = tag.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("gc %s: %w", m.Name(), err)
	}
	return n, nil
}

// PlatformJobs は platform の GC ジョブ。schemas は Outbox・Inbox を持つモジュールのスキーマ。
func PlatformJobs(pool *db.Pool, schemas []string) []Job {
	return []Job{
		gcJob{"platform-idempotency-gc", func(ctx context.Context) (Stats, error) {
			n, err := exec(ctx, pool.Platform(), `DELETE FROM platform.idempotency_keys WHERE expires_at < now()`)
			return Stats{"deleted": n}, err
		}},
		gcJob{"platform-outbox-gc", func(ctx context.Context) (Stats, error) {
			var total int64
			for _, s := range schemas {
				n, err := exec(ctx, pool.Relay(), fmt.Sprintf(`DELETE FROM %s.outbox_events WHERE published_at < now() - interval '7 days'`, s))
				if err != nil {
					return nil, err
				}
				total += n
			}
			return Stats{"deleted": total}, nil
		}},
		gcJob{"platform-inbox-gc", func(ctx context.Context) (Stats, error) {
			var total int64
			for _, s := range schemas {
				n, err := exec(ctx, pool.Module(s), fmt.Sprintf(`DELETE FROM %s.processed_messages WHERE processed_at < now() - interval '30 days'`, s))
				if err != nil {
					return nil, err
				}
				total += n
			}
			return Stats{"deleted": total}, nil
		}},
		gcJob{"platform-job-cursor-gc", func(ctx context.Context) (Stats, error) {
			n, err := exec(ctx, pool.Platform(), `DELETE FROM platform.job_cursors WHERE completed_at < now() - interval '30 days'`)
			return Stats{"deleted": n}, err
		}},
	}
}
