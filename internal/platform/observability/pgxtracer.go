package observability

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// PgxTracer はしきい値を超えたクエリについて db.slow_query を出す。引数は出さない（個人情報を避ける）。
func PgxTracer(threshold time.Duration) pgx.QueryTracer {
	return &slowQueryTracer{threshold: threshold}
}

type slowQueryTracer struct{ threshold time.Duration }

type traceKey struct{}

type traceData struct {
	start time.Time
	sql   string
}

func (t *slowQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, traceKey{}, traceData{start: time.Now(), sql: d.SQL})
}

func (t *slowQueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	td, ok := ctx.Value(traceKey{}).(traceData)
	if !ok {
		return
	}
	elapsed := time.Since(td.start)
	if elapsed < t.threshold {
		return
	}
	logger("platform.db").LogAttrs(ctx, slog.LevelWarn, "db.slow_query",
		slog.String("query", QueryName(td.sql)),
		slog.Int64("duration_ms", elapsed.Milliseconds()),
		slog.Int64("rows", d.CommandTag.RowsAffected()),
	)
}

// QueryName は sqlc の "-- name: X :one" からクエリ名を取り出す。なければ先頭の 1 語と長さ。
func QueryName(sql string) string {
	for _, line := range strings.Split(sql, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "-- name:"); ok {
			f := strings.Fields(rest)
			if len(f) > 0 {
				return f[0]
			}
		}
	}
	f := strings.Fields(sql)
	if len(f) == 0 {
		return "unknown"
	}
	return "unnamed:" + strings.ToLower(f[0])
}
