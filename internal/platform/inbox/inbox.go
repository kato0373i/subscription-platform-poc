// Package inbox は受け取ったメッセージの重複を (consumer, event_id) で除く（platform.md §3.2・§5）。
package inbox

import (
	"context"
	"errors"
	"fmt"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/messaging"
	sk "subscription-platform/internal/sharedkernel"
)

// Handler は購読者の業務処理。Inbox の行と同じトランザクションで呼ばれる。
type Handler func(ctx context.Context, tx db.Tx, env sk.EventEnvelope) error

type options struct{ systemReason string }

// Option は Wrap の設定。
type Option func(*options)

// AllowSystemScope は tenant_id が null のイベントを受ける購読者だけが指定する。
func AllowSystemScope(reason string) Option { return func(o *options) { o.systemReason = reason } }

// ErrTenantlessEvent は tenant_id が null のイベントを、AllowSystemScope のない購読者が受け取った。
var ErrTenantlessEvent = errors.New("inbox: event has no tenant_id")

// Wrap は h を Inbox で包む。重複なら h を呼ばずに成功扱いにする。
func Wrap(m *db.ModuleDB, consumer string, h Handler, opts ...Option) messaging.Handler {
	var o options
	for _, f := range opts {
		f(&o)
	}
	return func(ctx context.Context, env sk.EventEnvelope) error {
		switch {
		case env.TenantID != nil:
			ctx = db.WithTenantScope(ctx, *env.TenantID)
		case o.systemReason != "":
			ctx = db.WithSystemScope(ctx, o.systemReason)
		default:
			return messaging.Permanent(fmt.Errorf("%w: consumer %s, event %s", ErrTenantlessEvent, consumer, env.EventType))
		}
		return m.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
			tag, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s.processed_messages (consumer, message_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, m.Name()),
				consumer, env.EventID.UUID())
			if err != nil {
				return fmt.Errorf("inbox: record: %w", err)
			}
			if tag.RowsAffected() == 0 {
				messaging.MarkDuplicate(ctx)
				return nil
			}
			return h(ctx, tx, env)
		})
	}
}

// PlainHandler はトランザクションを持たない購読者の処理（他モジュールの contracts を順に呼ぶ Orchestration）。
type PlainHandler func(ctx context.Context, env sk.EventEnvelope) error

// Guard は「処理済みなら何もしない → h → 処理済みを記録」の順で包む（orchestration.md §5 の共通の手順）。
// h の途中の変更は各モジュールのトランザクションでコミットされるので、Inbox は最適化で、正しさは下流の冪等性で守る。
// 記録の前に落ちたら再配信で h をやり直す。
func Guard(m *db.ModuleDB, consumer string, h PlainHandler, opts ...Option) messaging.Handler {
	var o options
	for _, f := range opts {
		f(&o)
	}
	return func(ctx context.Context, env sk.EventEnvelope) error {
		switch {
		case env.TenantID != nil:
			ctx = db.WithTenantScope(ctx, *env.TenantID)
		case o.systemReason != "":
			ctx = db.WithSystemScope(ctx, o.systemReason)
		default:
			return messaging.Permanent(fmt.Errorf("%w: consumer %s, event %s", ErrTenantlessEvent, consumer, env.EventType))
		}
		var seen bool
		if err := m.ReadTx(ctx, func(ctx context.Context, tx db.Tx) error {
			return tx.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s.processed_messages WHERE consumer = $1 AND message_id = $2)`, m.Name()),
				consumer, env.EventID.UUID()).Scan(&seen)
		}); err != nil {
			return fmt.Errorf("inbox: check: %w", err)
		}
		if seen {
			messaging.MarkDuplicate(ctx)
			return nil
		}
		if err := h(ctx, env); err != nil {
			return err
		}
		return m.InTx(ctx, func(ctx context.Context, tx db.Tx) error {
			_, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s.processed_messages (consumer, message_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, m.Name()),
				consumer, env.EventID.UUID())
			if err != nil {
				return fmt.Errorf("inbox: record: %w", err)
			}
			return nil
		})
	}
}
