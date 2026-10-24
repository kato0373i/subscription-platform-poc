package observability

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// NewHTTPClient は外部への HTTP の共通ラッパー。呼び出しごとに http.outbound.completed を出す。
// target は fincode / webhook / ses。モジュールは http.DefaultClient や素の http.Client を使わない（forbidigo）。
func NewHTTPClient(target string, base *http.Client) *http.Client {
	if base == nil {
		base = &http.Client{Timeout: 30 * time.Second}
	}
	c := *base
	rt := c.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	c.Transport = &outboundTransport{target: target, next: rt}
	return &c
}

type outboundTransport struct {
	target string
	next   http.RoundTripper
}

func (t *outboundTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	start := time.Now()
	body := Current().HTTPBodyEnabled()
	var reqBody []byte
	if body && req.Body != nil && req.GetBody != nil {
		if rc, err := req.GetBody(); err == nil {
			reqBody, _ = io.ReadAll(io.LimitReader(rc, 64<<10))
			_ = rc.Close()
		}
	}
	resp, err := t.next.RoundTrip(req)
	attrs := []slog.Attr{
		slog.String("target", t.target),
		slog.String("method", req.Method),
		slog.String("host", req.URL.Host),
		slog.String("path", req.URL.Path),
		slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		slog.Int("attempt", attemptFrom(ctx)),
	}
	if k := req.Header.Get("Idempotency-Key"); k != "" {
		attrs = append(attrs, slog.String("idempotency_key", k))
	}
	level := slog.LevelInfo
	if err != nil {
		level = slog.LevelWarn
		attrs = append(attrs, slog.Group("error", slog.String("kind", classifyNetErr(err)), slog.String("message", err.Error())))
	} else {
		attrs = append(attrs, slog.Int("status", resp.StatusCode))
		if resp.StatusCode >= 400 {
			level = slog.LevelWarn
			attrs = append(attrs, slog.Group("error", slog.String("kind", "http_status")))
		}
		if body && resp.Body != nil {
			b, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(b))
			if rerr == nil {
				attrs = append(attrs, slog.Any("response_body", RedactJSONBody(b)))
			}
		}
	}
	if body && len(reqBody) > 0 {
		attrs = append(attrs, slog.Any("request_body", RedactJSONBody(reqBody)))
	}
	logger("platform.provider").LogAttrs(ctx, level, "http.outbound.completed", attrs...)
	return resp, err //nolint:wrapcheck // RoundTripper はエラーをそのまま返す
}

func classifyNetErr(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case isTLS(err):
		return "tls"
	default:
		return "connection"
	}
}

func isTLS(err error) bool {
	var rec tls.RecordHeaderError
	var cv *tls.CertificateVerificationError
	return errors.As(err, &rec) || errors.As(err, &cv)
}
