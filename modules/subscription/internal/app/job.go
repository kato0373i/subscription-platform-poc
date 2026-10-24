package app

import (
	"context"
	"log/slog"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/observability"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/subscription/internal/domain"
)

// TrialWillEndJobName はトライアル終了の予告ジョブ（§8、P-SUB-6）。名前は Platform の規約 `<module>-<name>` に合わせる（P-IMP-14）。
const TrialWillEndJobName = "subscription-trial-will-end"

// TrialWillEndJob は trial_end = as_of + 3 の trialing に、1 回だけ `subscription.trial_will_end` を出す。
type TrialWillEndJob struct{ s *Service }

func (s *Service) TrialWillEndJob() scheduler.Job { return TrialWillEndJob{s: s} }

func (TrialWillEndJob) Name() string { return TrialWillEndJobName }

func (j TrialWillEndJob) Run(ctx context.Context, run scheduler.Run) (scheduler.Stats, error) {
	ctx = observability.WithActor(ctx, sk.Actor{Type: "system", ID: TrialWillEndJobName})
	date := run.AsOf
	if date.IsZero() {
		date = j.s.d.Clock.Today()
	}
	var notified, skipped int64
	cursor := ""
	for {
		page, err := j.s.ListTrialEndingOn(ctx, date.AddDays(3), cursor, 500)
		if err != nil {
			return scheduler.Stats{"notified": notified, "skipped": skipped}, err
		}
		for _, ref := range page.Items {
			done := false
			_, err := j.s.mutate(db.WithTenantScope(ctx, ref.TenantID), ref.SubscriptionID, func(x *domain.Subscription) error {
				done = x.NotifyTrialWillEnd(date)
				return nil
			})
			if err != nil {
				return scheduler.Stats{"notified": notified, "skipped": skipped}, err
			}
			if done {
				notified++
			} else {
				skipped++
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	observability.Logger("subscription.trial_will_end").LogAttrs(ctx, slog.LevelInfo, "subscription.trial_will_end_notified",
		slog.String("as_of", date.String()), slog.Int64("notified", notified), slog.Int64("skipped", skipped))
	return scheduler.Stats{"notified": notified, "skipped": skipped}, nil
}
