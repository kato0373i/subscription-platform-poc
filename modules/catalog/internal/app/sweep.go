package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/observability"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/catalog/internal/adapter/postgres"
)

// SweepJobName は放置された予約の掃除ジョブ（§8）。
const SweepJobName = "catalog-coupon-reservation-sweep"

const sweepBatch = 500

// SweepJob は期限を過ぎた予約を released(expired) にして枠を戻す。
type SweepJob struct{ s *Service }

func (s *Service) SweepJob() scheduler.Job { return SweepJob{s: s} }

func (SweepJob) Name() string { return SweepJobName }

// Run は期限切れの予約を 500 件ずつ処理する。状態を確かめてから更新するので、重複起動・再実行でも二重に枠を戻さない。
func (j SweepJob) Run(ctx context.Context, _ scheduler.Run) (scheduler.Stats, error) {
	s := j.s
	ctx = observability.WithActor(ctx, sk.Actor{Type: "system", ID: SweepJobName})
	var released, skipped int64
	for {
		now := s.d.Clock.Now()
		var batch []postgres.ExpiredReservation
		err := s.d.DB.ReadTx(db.WithUnscoped(ctx, SweepJobName), func(ctx context.Context, tx db.Tx) error {
			var err error
			batch, err = postgres.New(tx).ListExpiredReservations(ctx, now, sweepBatch)
			return err
		})
		if err != nil {
			return nil, err
		}
		progressed := 0
		for _, e := range batch {
			ok, err := s.expireOne(ctx, e, now)
			if err != nil {
				return scheduler.Stats{"released": released, "skipped": skipped}, err
			}
			if ok {
				released++
				progressed++
			} else {
				skipped++
			}
		}
		// 全件が他の実行にロックされていた・状態が変わっていたなら、ここで止める（無限ループを避ける）
		if len(batch) < sweepBatch || progressed == 0 {
			break
		}
	}
	level := slog.LevelInfo
	if released > 0 {
		level = slog.LevelWarn // Orchestration の確定・解放漏れの兆候
	}
	observability.Logger("catalog.sweep").LogAttrs(ctx, level, "catalog.coupon_reservations_expired", slog.Int64("released", released), slog.Int64("skipped", skipped))
	return scheduler.Stats{"released": released, "skipped": skipped}, nil
}

// expireOne は 1 件をその事業者の RLS の下で解放する。他の実行がロック中なら false。
func (s *Service) expireOne(ctx context.Context, e postgres.ExpiredReservation, now time.Time) (bool, error) {
	done := false
	err := s.d.DB.InTx(db.WithTenantScope(ctx, e.TenantID), func(ctx context.Context, tx db.Tx) error {
		repo := postgres.New(tx)
		red, err := repo.GetRedemption(ctx, e.RedemptionID, postgres.ForUpdateSkipLocked)
		if errors.Is(err, postgres.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		from := red.State().Status
		if !red.Expire(now) {
			return nil
		}
		done = true
		return s.releaseSlot(ctx, tx, repo, red, from, now)
	})
	return done, err
}
