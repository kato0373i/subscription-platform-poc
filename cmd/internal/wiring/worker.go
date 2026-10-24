package wiring

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/outbox"
	"subscription-platform/internal/platform/scheduler"
)

// RunWorker は worker-billing・worker-integration：自分のキューの受信、ジョブ、常駐ループ、/healthz・/readyz。
func RunWorker(process string) func(ctx context.Context, b *Base) error {
	return func(ctx context.Context, b *Base) error {
		mods, err := modules(ctx, b, process)
		if err != nil {
			return err
		}
		var consumers []messaging.ConsumerSpec
		var jobs []scheduler.Job
		schemas := []string{"platform"}
		for _, m := range mods {
			consumers = append(consumers, m.Consumers...)
			jobs = append(jobs, m.Jobs...)
			if m.HasOutbox {
				schemas = append(schemas, m.Name)
			}
		}
		pc, pj := platformParts(b, process, schemas)
		consumers = append(consumers, pc...)
		jobs = append(jobs, pj...)

		var loops []func(context.Context) error
		for _, q := range b.Topology.QueuesFor(process) {
			p, err := processorFor(b, q, consumers, jobs, process)
			if err != nil {
				return err
			}
			cfg := messaging.ConsumerConfig{
				Client:   b.SQS,
				QueueURL: messaging.QueueURL(b.Cfg.QueueURLPrefix, q.Name),
				DLQURL:   messaging.QueueURL(b.Cfg.QueueURLPrefix, messaging.DLQName(q.Name)),
			}
			loops = append(loops, func(ctx context.Context) error { return messaging.RunConsumer(ctx, p, cfg) })
		}
		if process == "worker-billing" {
			relay := outbox.NewRelay(b.Pool, b.Publisher(), schemas, outbox.RelayConfig{})
			loops = append(loops, relay.Run)
		}
		if b.Cfg.SchedulerMode == "internal" {
			loops = append(loops, func(ctx context.Context) error {
				return scheduler.RunInternal(ctx, b.Schedules, process, b.SendToQueue)
			})
		}
		health := httpx.NewRouter(httpx.RouterConfig{Clock: b.Clock, Pool: b.Pool}, nil, httpx.Ports{}, nil)
		loops = append(loops, func(ctx context.Context) error {
			return serveHTTP(ctx, b.Cfg.HealthAddr, health.Handler(), health.SetShuttingDown)
		})
		b.Log.InfoContext(ctx, "worker.ready", slog.String("process", process), slog.Int("queues", len(b.Topology.QueuesFor(process))), slog.Any("outbox_schemas", schemas))
		return runAll(ctx, loops)
	}
}

func processorFor(b *Base, q messaging.QueueSpec, consumers []messaging.ConsumerSpec, jobs []scheduler.Job, process string) (messaging.Processor, error) {
	if q.Kind == messaging.KindScheduler {
		var mine []scheduler.Job
		for _, j := range jobs {
			if spec, ok := b.Schedules.Job(j.Name()); ok && spec.Process == process {
				mine = append(mine, j)
			}
		}
		return scheduler.NewRunner(q, b.Pool, b.Schedules, b.Clock, mine)
	}
	d, err := messaging.NewDispatcher(b.Topology, q.Name, consumers)
	if err != nil {
		return nil, fmt.Errorf("queue %s: %w", q.Name, err)
	}
	return d, nil
}

// runAll は全ループを並行に動かし、どれかが失敗したら残りも止める。
func runAll(ctx context.Context, loops []func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errs := make([]error, len(loops))
	for i, l := range loops {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l(ctx); err != nil && !errors.Is(err, context.Canceled) {
				errs[i] = err
				cancel()
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}
