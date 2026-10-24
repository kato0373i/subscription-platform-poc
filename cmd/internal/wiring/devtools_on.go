//go:build devtools

package wiring

import (
	"context"
	"crypto/ecdsa"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/devapi"
	"subscription-platform/internal/platform/devclock"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/inbox"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/outbox"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
)

// newClock は devtools ビルドでは dev clock（実時刻 + offset、1 秒キャッシュ）。
func newClock(pool *db.Pool) sk.Clock { return devclock.New(pool) }

func registerDev(r *httpx.Router, b *Base, key *ecdsa.PrivateKey) error {
	return devapi.Register(r, devapi.Deps{Pool: b.Pool, Clock: b.Clock.(*devclock.Clock), Scheduler: b.SchedulerService(), DevKey: key})
}

// platformParts は platform 自身の購読者とジョブ。
func platformParts(b *Base, process string, schemas []string) ([]messaging.ConsumerSpec, []scheduler.Job) {
	var cs []messaging.ConsumerSpec
	var js []scheduler.Job
	if process == "worker-billing" {
		cs = append(cs, messaging.ConsumerSpec{
			Name: devapi.SmokeConsumer,
			Handler: inbox.Wrap(b.Pool.Platform(), devapi.SmokeConsumer, func(context.Context, db.Tx, sk.EventEnvelope) error { return nil },
				inbox.AllowSystemScope("smoke")),
		})
		js = append(js, scheduler.PlatformJobs(b.Pool, schemas)...)
		js = append(js, noopJob{pool: b.Pool, writer: outbox.NewWriter("platform", b.Clock), clock: b.Clock})
	}
	return cs, js
}

// noopJob は何もしないジョブ。実行したことをイベントに残す（ジョブの相関 ID の確認。AC-PLT-082）。
type noopJob struct {
	pool   *db.Pool
	writer *outbox.Writer
	clock  sk.Clock
}

func (noopJob) Name() string { return "platform-noop" }

func (j noopJob) Run(ctx context.Context, run scheduler.Run) (scheduler.Stats, error) {
	env, err := sk.NewEnvelope("platform.job_noop_ran", "job_run", run.ID, 1, nil, j.clock.Now(), map[string]string{"as_of": run.AsOf.String()})
	if err != nil {
		return nil, err
	}
	err = j.pool.Platform().InTx(ctx, func(ctx context.Context, tx db.Tx) error { return j.writer.Append(ctx, tx, env) })
	return scheduler.Stats{"events": 1}, err
}
