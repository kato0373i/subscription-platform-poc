package auditctx_test

import (
	"context"
	"testing"

	"subscription-platform/internal/platform/auditctx"
)

// AC-PLT-055
func TestMaskSecretsAndPII(t *testing.T) {
	ctx := auditctx.NewBag(context.Background())
	auditctx.Annotate(ctx, "webhook_endpoint.rotate_secret", auditctx.Target{Type: "webhook_endpoint", ID: "1"},
		map[string]any{"email": "a@example.com", "secret": "whsec_x", "url": "https://x"},
		map[string]any{"signing_secret": "whsec_y", "nested": map[string]any{"display_name": "Taro"}, "amount": 1200})
	got := auditctx.Drain(ctx)
	if len(got) != 1 {
		t.Fatalf("annotations = %v", got)
	}
	a := got[0]
	if a.Before["email"] != "***" || a.Before["secret"] != "***" || a.Before["url"] != "https://x" {
		t.Errorf("before = %v", a.Before)
	}
	if a.After["signing_secret"] != "***" || a.After["nested"].(map[string]any)["display_name"] != "***" || a.After["amount"] != 1200 {
		t.Errorf("after = %v", a.After)
	}
	if len(auditctx.Drain(ctx)) != 0 {
		t.Error("Drain の後に注釈が残った")
	}
}

// AC-PLT-056
func TestAnnotateWithoutBagIsNoop(t *testing.T) {
	auditctx.Annotate(context.Background(), "plan.create", auditctx.Target{Type: "plan", ID: "1"}, nil, map[string]any{"a": 1})
	if got := auditctx.Drain(context.Background()); got != nil {
		t.Fatalf("got = %v", got)
	}
}
