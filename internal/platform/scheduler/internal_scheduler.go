package scheduler

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"subscription-platform/internal/platform/observability"
)

// RunInternal は SCHEDULER_MODE=internal のとき、ジョブを持つプロセスが schedules.yaml の cron で
// 自分の scheduler キューに {job, trigger:"schedule"} を送る（以降の経路は EventBridge Scheduler と同じ）。
// 複数タスクが同時に送っても、job_runs の重複排除で 1 回だけ実行される。
func RunInternal(ctx context.Context, s Schedules, process string, send Sender) error {
	type entry struct {
		spec JobSpec
		cron Cron
	}
	var entries []entry
	for _, j := range s.For(process) {
		if j.Schedule == "" {
			continue
		}
		c, err := ParseCron(j.Schedule)
		if err != nil {
			return err
		}
		entries = append(entries, entry{j, c})
	}
	log := observability.Logger("platform.job")
	last := time.Now().Truncate(time.Minute)
	for {
		next := last.Add(time.Minute)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Until(next)):
		}
		last = next
		for _, e := range entries {
			if !e.cron.Matches(next) {
				continue
			}
			body, _ := json.Marshal(JobMessage{Job: e.spec.Name, Trigger: TriggerSchedule})
			if err := send(ctx, SchedulerQueue(process), string(body)); err != nil {
				log.LogAttrs(ctx, slog.LevelWarn, "job.schedule_send_failed", slog.String("job", e.spec.Name), observability.Err(err))
			}
		}
	}
}
