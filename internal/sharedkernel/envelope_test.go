package sharedkernel_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	sk "subscription-platform/internal/sharedkernel"
)

func TestEnvelopeRoundTripIgnoresUnknownFields(t *testing.T) {
	type v1 struct {
		Amount int64 `json:"amount"`
	}
	tid := sk.NewID[sk.TenantTag]()
	env, err := sk.NewEnvelope("billing.invoice_paid", "invoice", uuid.New(), 3, &tid, time.Now(), v1{Amount: 980})
	if err != nil {
		t.Fatal(err)
	}
	if env.Module() != "billing" {
		t.Fatalf("module = %q", env.Module())
	}
	env.Payload = json.RawMessage(`{"amount":980,"added_later":"x"}`)
	b, _ := json.Marshal(env)
	var back sk.EventEnvelope
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	p, err := sk.DecodePayload[v1](back)
	if err != nil || p.Amount != 980 || *back.TenantID != tid {
		t.Fatalf("decode = %+v %v", p, err)
	}
	if _, err := sk.NewEnvelope("nodot", "x", uuid.New(), 1, nil, time.Now(), v1{}); err == nil {
		t.Fatal("event_type の形式を検査していない")
	}
}
