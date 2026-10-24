package observability

import (
	"context"
	"fmt"
	"log/slog"
)

// StateChanged は domain.state_changed を出す（from == to なら出さない）。
// 状態を持つ集約のリポジトリの保存処理が呼ぶ（§8.6.2）。
func StateChanged(ctx context.Context, aggregate, id, from, to, reason string, version ...int64) {
	if from == to {
		return
	}
	attrs := []slog.Attr{
		slog.String("aggregate", aggregate),
		slog.String("id", id),
		slog.String("from", from),
		slog.String("to", to),
	}
	if reason != "" {
		attrs = append(attrs, slog.String("reason", reason))
	}
	if len(version) > 0 {
		attrs = append(attrs, slog.Int64("version", version[0]))
	}
	logger(aggregateModule(aggregate)+".domain").LogAttrs(ctx, slog.LevelInfo, "domain.state_changed", attrs...)
}

// aggregateModule は集約名からモジュール名を推定する（ログの module 項目用）。
func aggregateModule(aggregate string) string {
	switch aggregate {
	case "tenant":
		return "tenant"
	case "subscription":
		return "subscription"
	case "invoice":
		return "billing"
	case "payment_attempt":
		return "payments"
	case "coupon_redemption":
		return "catalog"
	case "billing_cycle_process", "bulk_cancellation_job":
		return "orchestration"
	case "webhook_delivery", "email_notification":
		return "integration"
	default:
		return "domain"
	}
}

// Recover は defer で使う。panic を回収して panic.recovered を出し、errp があれば error に変える。
//
//	defer observability.Recover(ctx, "consumer", &err)
func Recover(ctx context.Context, component string, errp ...*error) {
	r := recover()
	if r == nil {
		return
	}
	err := fmt.Errorf("panic: %v", r)
	logger("platform."+component).LogAttrs(ctx, slog.LevelError, "panic.recovered",
		Err(err), Stack())
	if len(errp) > 0 && errp[0] != nil {
		*errp[0] = err
	}
}
