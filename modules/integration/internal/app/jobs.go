package app

import (
	"context"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/scheduler"
	"subscription-platform/modules/integration/internal/adapter/postgres/sqlcgen"
)

// JobSecretCleanup はジョブ名（deploy/schedules.yaml と同じ。§8.5）。
const JobSecretCleanup = "integration-webhook-secret-cleanup"

type secretCleanupJob struct{ s *Service }

// SecretCleanupJob は期限の過ぎた前の鍵を消す（日次。署名は遅延評価で期限を守るので、遅れても前の鍵は使われない）。
func (s *Service) SecretCleanupJob() scheduler.Job { return secretCleanupJob{s} }

func (secretCleanupJob) Name() string { return JobSecretCleanup }

func (j secretCleanupJob) Run(ctx context.Context, _ scheduler.Run) (scheduler.Stats, error) {
	var n int64
	now := j.s.d.Clock.Now()
	err := j.s.d.DB.InTx(db.WithSystemScope(ctx, "webhook secret cleanup"), func(ctx context.Context, tx db.Tx) error {
		var err error
		n, err = sqlcgen.New(tx).CleanupExpiredSecrets(ctx, &now)
		return err
	})
	return scheduler.Stats{"cleared": n}, err
}
