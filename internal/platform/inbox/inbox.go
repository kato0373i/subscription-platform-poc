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
