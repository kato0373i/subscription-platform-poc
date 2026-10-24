package messaging_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"subscription-platform/internal/platform/messaging"
	sk "subscription-platform/internal/sharedkernel"
)

const topo = `
bus: sp-events
queues:
  - name: billing-payments
    process: worker-billing
    consumer: billing.payment_outcome
    events: [payments.payment_succeeded, "prefix:payments.payment_f"]
    concurrency: 8
  - name: orc-renewal.fifo
    process: worker-billing
    consumer: orchestration.renewal_requests
    fifo: true
  - name: scheduler-billing
    process: worker-billing
    kind: scheduler
`

func envBody(t *testing.T, eventType string, raw bool) string {
	t.Helper()
	env, err := sk.NewEnvelope(eventType, "x", uuid.New(), 1, nil, time.Now(), map[string]int{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	env.CorrelationID = "c-1"
	if raw {
		b, _ := json.Marshal(env)
		return string(b)
	}
	s, err := messaging.EncodeEventBridgeBody(env)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTopology(t *testing.T) {
	tp, err := messaging.ParseTopology([]byte(topo))
	if err != nil {
		t.Fatal(err)
	}
	if subs := tp.Subscribers("payments.payment_failed"); len(subs) != 1 || subs[0].Name != "billing-payments" {
		t.Fatalf("subs = %v", subs)
	}
	if len(tp.Subscribers("payments.payment_pending")) != 0 {
		t.Fatal("prefix の外にマッチした")
	}
	q, _ := tp.Queue("billing-payments")
	if q.Concurrency != 8 || q.VisibilityTimeoutS != 60 || q.MaxReceive != 5 {
		t.Fatalf("defaults = %+v", q)
	}
	if messaging.DLQName("orc-renewal.fifo") != "orc-renewal-dlq.fifo" || messaging.DLQName("a") != "a-dlq" {
		t.Fatal("DLQName")
	}
	bad := []string{
		"bus: b\nqueues:\n  - {name: x.fifo, process: worker-billing, consumer: a.b, events: [a.b], fifo: true}",
		"bus: b\nqueues:\n  - {name: x, process: worker-billing, consumer: a.b, events: [\"suffix:x\"]}",
		"bus: b\nqueues:\n  - {name: x, process: lambda, consumer: a.b}",
		"bus: b\nqueues:\n  - {name: x, process: api, consumer: a.b}\n  - {name: y, process: api, consumer: a.b}",
		"bus: b\nqueues:\n  - {name: x, process: api, consumer: a.b, unknown: 1}",
	}
	for _, b := range bad {
		if _, err := messaging.ParseTopology([]byte(b)); err == nil {
			t.Errorf("通ってしまった: %s", b)
		}
	}
}

func TestNewDispatcherRequiresConsumer(t *testing.T) {
	tp, _ := messaging.ParseTopology([]byte(topo))
	if _, err := messaging.NewDispatcher(tp, "billing-payments", nil); err == nil {
		t.Fatal("購読者なしで作れた")
	}
}

// AC-PLT-061
func TestDispatchFIFOKeepsGroupOrder(t *testing.T) {
	tp, _ := messaging.ParseTopology([]byte(topo))
	var called []string
	h := func(_ context.Context, env sk.EventEnvelope) error {
		p, _ := sk.DecodePayload[map[string]string](env)
		called = append(called, p["id"])
		if p["id"] == "2" {
			return errors.New("transient")
		}
		return nil
	}
	d, err := messaging.NewDispatcher(tp, "orc-renewal.fifo", []messaging.ConsumerSpec{{Name: "orchestration.renewal_requests", Handler: h}})
	if err != nil {
		t.Fatal(err)
	}
	var msgs []messaging.Message
	for i, g := range []string{"G1", "G1", "G1", "G2", "G2", "G2"} {
		id := string(rune('1' + i))
		env, _ := sk.NewEnvelope("orchestration.renewal_requested", "x", uuid.New(), 1, nil, time.Now(), map[string]string{"id": id})
		env.CorrelationID = "c"
		b, _ := json.Marshal(env)
		msgs = append(msgs, messaging.Message{ID: id, GroupID: g, Body: string(b), ReceiveCount: 1})
	}
	out := d.Dispatch(context.Background(), msgs)
	ids := func(ms []messaging.Message) string {
		var s []string
		for _, m := range ms {
			s = append(s, m.ID)
		}
		return strings.Join(s, ",")
	}
	if ids(out.Succeeded) != "1,4,5,6" || ids(out.Retry) != "2,3" || len(out.Permanent) != 0 {
		t.Fatalf("succeeded=%s retry=%s permanent=%s", ids(out.Succeeded), ids(out.Retry), ids(out.Permanent))
	}
	if strings.Join(called, ",") != "1,2,4,5,6" {
		t.Fatalf("called = %v（3 は呼ばれてはいけない）", called)
	}
}

func TestDispatchClassifiesErrors(t *testing.T) {
	tp, _ := messaging.ParseTopology([]byte(topo))
	var gotCorr, gotCause string
	h := func(ctx context.Context, env sk.EventEnvelope) error {
		switch env.EventType {
		case "payments.payment_failed":
			return messaging.Permanent(errors.New("contract violation"))
		case "payments.payment_fx":
			panic("boom")
		}
		_ = ctx
		gotCorr = env.CorrelationID
		gotCause = env.EventID.String()
		return nil
	}
	d, _ := messaging.NewDispatcher(tp, "billing-payments", []messaging.ConsumerSpec{{Name: "billing.payment_outcome", Handler: h}})
	out := d.Dispatch(context.Background(), []messaging.Message{
		{ID: "ok", Body: envBody(t, "payments.payment_succeeded", false)},
		{ID: "perm", Body: envBody(t, "payments.payment_failed", false)},
		{ID: "panic", Body: envBody(t, "payments.payment_fx", false)},
		{ID: "garbage", Body: "not json"},
		{ID: "missing", Body: `{"detail-type":"x.y","detail":{"event_type":"x.y"}}`},
	})
	if len(out.Succeeded) != 1 || len(out.Permanent) != 3 || len(out.Retry) != 1 || out.Retry[0].ID != "panic" {
		t.Fatalf("out = %+v", out)
	}
	if gotCorr != "c-1" || gotCause == "" {
		t.Fatalf("corr = %q cause = %q", gotCorr, gotCause)
	}
}
