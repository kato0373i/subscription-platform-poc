package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"subscription-platform/internal/platform/observability"
)

func setup(t *testing.T, level string) (*observability.Logging, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	l, err := observability.Setup(observability.LogConfig{
		Service: "api", Version: "test", Env: "test", Sinks: []string{"stdout"}, Level: level, Stdout: &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	return l, &buf
}

func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if ln == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("JSON でない行: %s", ln)
		}
		out = append(out, m)
	}
	return out
}

// AC-PLT-044
func TestSecretKeysAreRedacted(t *testing.T) {
	l, buf := setup(t, "info")
	l.ForModule("payments").InfoContext(context.Background(), "x",
		slog.String("authorization", "Bearer abc"),
		slog.String("cookie", "sid=1"),
		slog.String("x-api-key", "sk_live_123"),
		slog.String("card_token", "anything"),
		slog.Any("headers", map[string]string{"Authorization": "Basic zzz"}),
	)
	m := lines(t, buf)[0]
	for _, k := range []string{"authorization", "cookie", "x-api-key", "card_token"} {
		if m[k] != "[REDACTED]" {
			t.Errorf("%s = %v", k, m[k])
		}
	}
	if h := m["headers"].(map[string]any); h["Authorization"] != "[REDACTED]" {
		t.Errorf("headers.Authorization = %v", h["Authorization"])
	}
}

// AC-PLT-075
func TestRedactValues(t *testing.T) {
	tests := []struct{ in, want string }{
		{"4242 4242 4242 4242", "[REDACTED_PAN]"},
		{"card 4242424242424242 used", "card [REDACTED_PAN] used"},
		{"4242424242424241", "4242424242424241"},
		{"Bearer abc", "[REDACTED]"},
		{"sk_live_x", "[REDACTED]"},
		{"whsec_x", "[REDACTED]"},
		{"tok_stub_success_4242_1229_ab12cd34", "[REDACTED]"},
		{"taro@example.com", "t***@example.com"},
		{"0190a0b2-7c1e-7d3a-9f10-1234567890ab", "0190a0b2-7c1e-7d3a-9f10-1234567890ab"},
	}
	for _, tt := range tests {
		if got := observability.RedactString(tt.in); got != tt.want {
			t.Errorf("RedactString(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	got := observability.Redact(map[string]any{"name": "山田太郎", "tenant_name": "ACME", "nested": []any{map[string]any{"holder_name": "TARO"}}}).(map[string]any)
	if got["name"] != "山***" || got["tenant_name"] != "ACME" {
		t.Errorf("names = %v", got)
	}
	if got["nested"].([]any)[0].(map[string]any)["holder_name"] != "T***" {
		t.Errorf("nested = %v", got["nested"])
	}
	// ReplaceAttr でも同じ規則が効く
	l, buf := setup(t, "info")
	l.ForModule("customer").InfoContext(context.Background(), "x", slog.String("email", "taro@example.com"), slog.String("name", "山田太郎"), slog.String("tenant_name", "ACME"))
	m := lines(t, buf)[0]
	if m["email"] != "t***@example.com" || m["name"] != "山***" || m["tenant_name"] != "ACME" {
		t.Errorf("record = %v", m)
	}
}

// AC-PLT-080
func TestModuleLevels(t *testing.T) {
	l, buf := setup(t, "info,billing=debug")
	ctx := context.Background()
	l.ForModule("billing").DebugContext(ctx, "billing-debug")
	l.ForModule("catalog").DebugContext(ctx, "catalog-debug")
	l.ForModule("billing.consumer").DebugContext(ctx, "billing-consumer-debug")
	got := lines(t, buf)
	if len(got) != 2 || got[0]["msg"] != "billing-debug" || got[1]["component"] != "consumer" {
		t.Fatalf("lines = %v", got)
	}
	if _, err := observability.Setup(observability.LogConfig{Service: "api", Env: "test", Level: "info,billing=loud"}); err == nil {
		t.Fatal("不正なレベルで起動できた")
	}
}

// AC-PLT-084
func TestProdRejectsFileAndBody(t *testing.T) {
	for _, c := range []observability.LogConfig{
		{Service: "api", Env: "prod", HTTPBody: "redacted"},
		{Service: "api", Env: "prod", Sinks: []string{"stdout", "file"}},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("%+v が通った", c)
		}
	}
}

func TestCommonFieldsAndCorrelation(t *testing.T) {
	l, buf := setup(t, "info")
	cause := "req-1"
	ctx := observability.WithCorrelation(context.Background(), "e2e-x-abc123", &cause)
	ctx = observability.WithFields(ctx, slog.String("tenant_id", "t1"))
	l.ForModule("platform.http").InfoContext(ctx, "http.request.completed")
	m := lines(t, buf)[0]
	want := map[string]any{"service": "api", "module": "platform", "component": "http", "correlation_id": "e2e-x-abc123", "causation_id": "req-1", "tenant_id": "t1", "env": "test"}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	if ts, _ := m["ts"].(string); !strings.HasSuffix(ts, "Z") || len(ts) != 24 {
		t.Errorf("ts = %v", m["ts"])
	}
	if !observability.ValidCorrelationID("job:orchestration-renewal-dispatch:0190") || observability.ValidCorrelationID(strings.Repeat("a", 200)) || observability.ValidCorrelationID("-x") {
		t.Error("ValidCorrelationID の判定が違う")
	}
}

func TestStateChangedSkipsSameState(t *testing.T) {
	_, buf := setup(t, "info")
	observability.StateChanged(context.Background(), "subscription", "s1", "active", "active", "")
	observability.StateChanged(context.Background(), "subscription", "s1", "pending_initial", "active", "payment_succeeded")
	got := lines(t, buf)
	if len(got) != 1 || got[0]["from"] != "pending_initial" || got[0]["module"] != "subscription" {
		t.Fatalf("lines = %v", got)
	}
}

func TestFileSinkWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	l, err := observability.Setup(observability.LogConfig{Service: "worker-billing", Env: "local", Sinks: []string{"file"}, Dir: dir, Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	l.ForModule("billing").InfoContext(context.Background(), "hello")
	b, err := os.ReadFile(filepath.Join(dir, "worker-billing.jsonl"))
	if err != nil || !strings.Contains(string(b), `"msg":"hello"`) {
		t.Fatalf("file = %s %v", b, err)
	}
	_ = l.Close()
}

func TestRecoverConvertsPanic(t *testing.T) {
	_, buf := setup(t, "info")
	f := func() (err error) {
		defer observability.Recover(context.Background(), "consumer", &err)
		panic("boom")
	}
	if err := f(); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if m := lines(t, buf)[0]; m["msg"] != "panic.recovered" || m["stack"] == nil {
		t.Fatalf("record = %v", m)
	}
}
