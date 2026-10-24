package observability

import (
	"context"
	"log/slog"
	"regexp"

	"github.com/google/uuid"
	sk "subscription-platform/internal/sharedkernel"
)

type ctxKey int

const (
	keyFields ctxKey = iota
	keyCorrelation
	keyActor
	keyAttempt
)

type correlation struct {
	corr  string
	cause *string
}

var correlationPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// ValidCorrelationID は相関 ID の形式（^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$）を満たすか。
func ValidCorrelationID(s string) bool { return correlationPattern.MatchString(s) }

// NewCorrelationID は UUIDv7 の文字列を返す。
func NewCorrelationID() string { return uuid.Must(uuid.NewV7()).String() }

// WithCorrelation は相関 ID と直前の原因を context に入れる。以後のログとイベントに付く。
func WithCorrelation(ctx context.Context, corr string, cause *string) context.Context {
	return context.WithValue(ctx, keyCorrelation, correlation{corr: corr, cause: cause})
}

// CorrelationFrom は context の相関 ID と原因を返す。なければ空文字と nil。
func CorrelationFrom(ctx context.Context) (string, *string) {
	c, _ := ctx.Value(keyCorrelation).(correlation)
	return c.corr, c.cause
}

// WithFields は以後のログに付ける項目を足す（subscription_id など）。同じキーは後から足した値が勝つ。
func WithFields(ctx context.Context, attrs ...slog.Attr) context.Context {
	prev, _ := ctx.Value(keyFields).([]slog.Attr)
	next := make([]slog.Attr, 0, len(prev)+len(attrs))
	for _, p := range prev {
		if !hasKey(attrs, p.Key) {
			next = append(next, p)
		}
	}
	next = append(next, attrs...)
	return context.WithValue(ctx, keyFields, next)
}

func hasKey(attrs []slog.Attr, k string) bool {
	for _, a := range attrs {
		if a.Key == k {
			return true
		}
	}
	return false
}

// FieldsFrom は WithFields で足した項目を返す。
func FieldsFrom(ctx context.Context) []slog.Attr {
	f, _ := ctx.Value(keyFields).([]slog.Attr)
	return f
}

// FieldString は WithFields で足した文字列の項目を返す。
func FieldString(ctx context.Context, key string) string {
	for _, a := range FieldsFrom(ctx) {
		if a.Key == key {
			return a.Value.String()
		}
	}
	return ""
}

// WithActor は操作した主体を context に入れる。
func WithActor(ctx context.Context, a sk.Actor) context.Context {
	return context.WithValue(ctx, keyActor, &a)
}

// ActorFrom は context の主体を返す。なければ nil。
func ActorFrom(ctx context.Context) *sk.Actor {
	a, _ := ctx.Value(keyActor).(*sk.Actor)
	return a
}

// WithAttempt は外向きの呼び出しの試行回数を context に入れる（http.outbound.completed の attempt）。
func WithAttempt(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, keyAttempt, n)
}

func attemptFrom(ctx context.Context) int {
	n, _ := ctx.Value(keyAttempt).(int)
	if n == 0 {
		return 1
	}
	return n
}

// contextAttrs はログに付ける context の項目を返す。
func contextAttrs(ctx context.Context) []slog.Attr {
	if ctx == nil {
		return nil
	}
	var out []slog.Attr
	if c, ok := ctx.Value(keyCorrelation).(correlation); ok && c.corr != "" {
		out = append(out, slog.String("correlation_id", c.corr))
		if c.cause != nil {
			out = append(out, slog.String("causation_id", *c.cause))
		}
	}
	if a := ActorFrom(ctx); a != nil {
		out = append(out, slog.Group("actor", slog.String("type", a.Type), slog.String("id", a.ID)))
	}
	out = append(out, FieldsFrom(ctx)...)
	return out
}
