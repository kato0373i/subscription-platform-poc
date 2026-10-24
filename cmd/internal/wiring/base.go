// Package wiring はプロセスごとに必要なモジュールを組み立てる（platform.md §8.2）。
// Go の internal の規則で cmd/** からしか import できない。modules/X を import できるのはここだけ。
package wiring

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"subscription-platform/deploy"
	"subscription-platform/internal/platform/awsx"
	"subscription-platform/internal/platform/config"
	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/messaging"
	"subscription-platform/internal/platform/observability"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
)

// Base は全プロセスが共有する部品。
type Base struct {
	Cfg       config.Config
	Logging   *observability.Logging
	Log       *slog.Logger
	Pool      *db.Pool
	AWS       aws.Config
	SQS       *sqs.Client
	Topology  messaging.Topology
	Schedules scheduler.Schedules
	Clock     sk.Clock
	Services  Services // modules() が組み立てた contracts（devtools の一括シードが使う）
}

// NewBase は設定・ログ・DB・AWS・配線を用意する。
func NewBase(ctx context.Context, service string) (*Base, error) {
	cfg := config.MustLoad(service)
	lg, err := observability.Setup(cfg.Log)
	if err != nil {
		return nil, fmt.Errorf("logging: %w", err)
	}
	b := &Base{Cfg: cfg, Logging: lg, Log: lg.ForModule("platform.process")}
	b.Topology, err = messaging.LoadTopology(deploy.FS)
	if err != nil {
		return nil, err
	}
	b.Schedules, err = scheduler.LoadSchedules(deploy.FS)
	if err != nil {
		return nil, err
	}
	b.Pool, err = db.Open(ctx, db.Config{URL: cfg.DatabaseURL, MaxConns: cfg.DBMaxConns, SlowQuery: cfg.SlowQuery})
	if err != nil {
		return nil, err
	}
	b.AWS, err = awsx.Load(ctx, cfg.AWSRegion)
	if err != nil {
		return nil, err
	}
	b.SQS = sqs.NewFromConfig(b.AWS)
	b.Clock = newClock(b.Pool)
	return b, nil
}

// Close は DB とログを閉じる。
func (b *Base) Close() {
	if b.Pool != nil {
		b.Pool.Close()
	}
	if b.Logging != nil {
		_ = b.Logging.Close()
	}
}

// Publisher は MESSAGING_MODE に応じた送信の実装。
func (b *Base) Publisher() messaging.Publisher {
	if b.Cfg.MessagingMode == "direct" {
		return messaging.NewFanoutPublisher(b.SQS, b.Topology, b.Cfg.QueueURLPrefix)
	}
	return messaging.NewEventBridgePublisher(eventbridge.NewFromConfig(b.AWS), b.Cfg.EventBusName)
}

// SendToQueue は scheduler キューなどへ本文を送る。
func (b *Base) SendToQueue(ctx context.Context, queue, body string) error {
	return messaging.SendRaw(ctx, b.SQS, messaging.QueueURL(b.Cfg.QueueURLPrefix, queue), body)
}

// SchedulerService はジョブの投入と照会。
func (b *Base) SchedulerService() *scheduler.Service {
	return scheduler.NewService(b.Pool, b.Schedules, b.Clock, b.SendToQueue)
}

// Main はプロセスの入口。SIGTERM・SIGINT で ctx を取り消し、run の終了を待って終了コードを返す。
func Main(service string, run func(ctx context.Context, b *Base) error) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	b, err := NewBase(ctx, service)
	if err != nil {
		fmt.Fprintln(os.Stderr, service+":", err) //nolint:forbidigo // ログの仕組みがない段階のエラー
		return 1
	}
	defer b.Close()
	b.Log.InfoContext(ctx, "process.started", slog.String("app_env", b.Cfg.AppEnv), slog.Bool("devtools", config.DevtoolsBuild),
		slog.String("messaging_mode", b.Cfg.MessagingMode), slog.String("scheduler_mode", b.Cfg.SchedulerMode), slog.String("auth_mode", b.Cfg.AuthMode))
	if err := run(ctx, b); err != nil && !errors.Is(err, context.Canceled) {
		b.Log.ErrorContext(ctx, "process.failed", observability.Err(err))
		return 1
	}
	b.Log.InfoContext(context.WithoutCancel(ctx), "process.stopped")
	return 0
}

// serveHTTP は ctx が取り消されるまで HTTP を提供し、取り消されたら onStop を呼んでから最大 30 秒で閉じる。
func serveHTTP(ctx context.Context, addr string, h http.Handler, onStop func()) error {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return fmt.Errorf("http %s: %w", addr, err)
	case <-ctx.Done():
	}
	if onStop != nil {
		onStop()
	}
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	return nil
}
