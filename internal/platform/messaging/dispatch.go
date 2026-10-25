package messaging

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"subscription-platform/internal/platform/observability"
	sk "subscription-platform/internal/sharedkernel"
)

// Processor は受信したメッセージを処理する（Dispatcher と scheduler の Runner が実装する）。
type Processor interface {
	Dispatch(ctx context.Context, msgs []Message) Outcome
	Queue() QueueSpec
}

// Dispatcher は実行基盤に依存しない受信の共通部分。解析 → 購読者の振り分け → ハンドラ → エラーの分類。
type Dispatcher struct {
	queue    QueueSpec
	consumer ConsumerSpec
}

// NewDispatcher は queue の購読者を consumers から探す。見つからなければエラー（起動を失敗させる）。
func NewDispatcher(t Topology, queue string, consumers []ConsumerSpec) (*Dispatcher, error) {
	q, ok := t.Queue(queue)
	if !ok {
		return nil, fmt.Errorf("messaging: queue %q is not in topology", queue)
	}
	if q.Kind != KindEvents {
		return nil, fmt.Errorf("messaging: queue %q is not an event queue", queue)
	}
	for _, c := range consumers {
		if c.Name == q.Consumer {
			return &Dispatcher{queue: q, consumer: c}, nil
		}
	}
	return nil, fmt.Errorf("messaging: consumer %q for queue %q is not registered", q.Consumer, queue)
}

func (d *Dispatcher) Queue() QueueSpec { return d.queue }

type dispatchInfoKey struct{}

type dispatchInfo struct{ duplicate bool }

// MarkDuplicate は Inbox が重複と判断したことを記録する（event.consumed の duplicate=true）。
func MarkDuplicate(ctx context.Context) {
	if di, ok := ctx.Value(dispatchInfoKey{}).(*dispatchInfo); ok {
		di.duplicate = true
	}
}

// Dispatch はメッセージを 1 件ずつ処理する。FIFO では、失敗したメッセージより後ろの同じグループを処理しない。
func (d *Dispatcher) Dispatch(ctx context.Context, msgs []Message) Outcome {
	var out Outcome
	failedGroups := map[string]bool{}
	for _, m := range msgs {
		if d.queue.FIFO && failedGroups[m.GroupID] {
			out.Retry = append(out.Retry, m)
			continue
		}
		err := d.handle(ctx, m)
		switch {
		case err == nil:
			out.Succeeded = append(out.Succeeded, m)
		case IsPermanent(err):
			out.Permanent = append(out.Permanent, m)
			if d.queue.FIFO {
				failedGroups[m.GroupID] = true
			}
		default:
			out.Retry = append(out.Retry, m)
			if delay := RetryDelay(err); delay > 0 {
				if out.Delay == nil {
					out.Delay = map[string]time.Duration{}
				}
				out.Delay[m.ID] = delay
			}
			if d.queue.FIFO {
				failedGroups[m.GroupID] = true
			}
		}
	}
	return out
}

func (d *Dispatcher) handle(ctx context.Context, m Message) (err error) {
	log := observability.Logger("platform.consumer")
	start := time.Now()
	base := []slog.Attr{slog.String("message_id", m.ID), slog.String("queue", d.queue.Name), slog.String("consumer", d.consumer.Name), slog.Int("receive_count", m.ReceiveCount)}
	env, perr := DecodeBody(m.Body)
	if perr != nil {
		err = Permanent(perr)
		log.LogAttrs(ctx, slog.LevelWarn, "event.failed", append(base, observability.ErrWithCode("unparseable_message", perr))...)
		return err
	}
	ctx = ContextFromEnvelope(ctx, env)
	ctx = observability.WithFields(ctx, slog.String("message_id", m.ID), slog.String("queue", d.queue.Name), slog.String("consumer", d.consumer.Name))
	info := &dispatchInfo{}
	ctx = context.WithValue(ctx, dispatchInfoKey{}, info)
	func() {
		defer observability.Recover(ctx, "consumer", &err)
		err = d.consumer.Handler(ctx, env)
	}()
	attrs := []slog.Attr{slog.Int("receive_count", m.ReceiveCount), slog.Int64("duration_ms", time.Since(start).Milliseconds())}
	if err != nil {
		log.LogAttrs(ctx, slog.LevelWarn, "event.failed", append(attrs, slog.Bool("permanent", IsPermanent(err)), observability.Err(err))...)
		return err
	}
	if info.duplicate {
		attrs = append(attrs, slog.Bool("duplicate", true))
	}
	log.LogAttrs(ctx, slog.LevelInfo, "event.consumed", attrs...)
	return nil
}

// ContextFromEnvelope は購読者の context を作る：相関 ID を引き継ぎ、causation_id に受け取った event_id、actor は system。
func ContextFromEnvelope(ctx context.Context, env sk.EventEnvelope) context.Context {
	cause := env.EventID.String()
	ctx = observability.WithCorrelation(ctx, env.CorrelationID, &cause)
	ctx = observability.WithActor(ctx, sk.Actor{Type: "system", ID: "consumer"})
	fields := []slog.Attr{slog.String("event_id", env.EventID.String()), slog.String("event_type", env.EventType)}
	if env.TenantID != nil {
		fields = append(fields, slog.String("tenant_id", env.TenantID.String()))
	}
	return observability.WithFields(ctx, fields...)
}
