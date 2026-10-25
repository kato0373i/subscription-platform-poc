package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	sk "subscription-platform/internal/sharedkernel"
)

// APIVersion は外部イベントのスキーマ版（PoC はこれだけ）。
const APIVersion = "2026-10-01"

// MaxPayloadBytes は外部イベントの本文の上限（§4.2）。
const MaxPayloadBytes = 64 << 10

// ExternalEventTypes は事業者に送る 13 種類（§4.1）。内部の event_type → 外部 type の対応表はここだけに置く（§10）。
var ExternalEventTypes = []string{
	"subscription.started", "subscription.trial_will_end", "subscription.renewed", "subscription.past_due", "subscription.recovered",
	"subscription.plan_change_scheduled", "subscription.plan_change_revoked", "subscription.plan_changed",
	"subscription.cancellation_scheduled", "subscription.cancellation_revoked", "subscription.canceled",
	"invoice.paid", "invoice.payment_failed",
}

var internalToExternal = map[string]string{
	"billing.invoice_paid":           "invoice.paid",
	"billing.invoice_payment_failed": "invoice.payment_failed",
}

func init() {
	for _, t := range ExternalEventTypes[:11] {
		internalToExternal[t] = t // subscription.* は同名
	}
}

// KnownEventType は外部 type として登録できる値か。
func KnownEventType(t string) bool { return slices.Contains(ExternalEventTypes, t) }

// WebhookEventTypes は integration-webhook が購読する内部の event_type（deploy/messaging.yaml と一致させる）。
func WebhookEventTypes() []string {
	out := make([]string, 0, len(internalToExternal))
	for k := range internalToExternal {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// callerManaged は申込の決済失敗（P-BIL-5）。collection_mode がない payload は scheduled とみなす。
func callerManaged(payload json.RawMessage) bool {
	var p struct {
		CollectionMode string `json:"collection_mode"`
	}
	_ = json.Unmarshal(payload, &p)
	return p.CollectionMode == "caller_managed"
}

// ExternalType は内部イベントを外部 type に変換する。ok=false は「出さない」（AC-INT-010）。
func ExternalType(env sk.EventEnvelope) (string, bool) {
	t, ok := internalToExternal[env.EventType]
	if !ok {
		return "", false
	}
	if env.EventType == "billing.invoice_payment_failed" && callerManaged(env.Payload) {
		return "", false
	}
	return t, true
}

// CustomerAccountOf は payload の契約者 ID（subscription のスナップショットにだけある）。なければ nil。
func CustomerAccountOf(env sk.EventEnvelope) (*sk.CustomerAccountID, error) {
	var p struct {
		Subscription *struct {
			CustomerAccountID *sk.CustomerAccountID `json:"customer_account_id"`
		} `json:"subscription"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return nil, fmt.Errorf("integration: payload: %w", err)
	}
	if p.Subscription == nil {
		return nil, nil
	}
	return p.Subscription.CustomerAccountID, nil
}

// WebhookEventTag は外部イベントの ID の型の印。
type WebhookEventTag struct{}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewEventID は UUIDv7 の 128 ビットを Crockford base32 の 26 文字にした "evt_…"（§3.2）。
func NewEventID() string {
	return EventIDFrom([16]byte(sk.NewID[WebhookEventTag]().UUID()))
}

// EventIDFrom は 16 バイトを "evt_" + 26 文字にする（先頭の文字は上位 3 ビット）。
func EventIDFrom(b [16]byte) string {
	out := make([]byte, 26)
	// 130 ビット（先頭 2 ビットは 0）を 5 ビットずつ下位から取り出す
	var hi, lo uint64
	for i := range 8 {
		hi = hi<<8 | uint64(b[i])
		lo = lo<<8 | uint64(b[8+i])
	}
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | (hi&31)<<59
		hi >>= 5
	}
	return "evt_" + string(out)
}

// ErrPayloadTooLarge は外部イベントの本文が 64 KB を超えた。
var ErrPayloadTooLarge = errors.New("integration: webhook payload exceeds 64KB")

// BuildPayload は §4.2 の本文を作る。customerID は ResolveTenantCustomerID の結果（契約者がいないイベントでは空）。
// 返すバイト列をそのまま保存し、再送でも同じものを送る。
func BuildPayload(eventID, extType string, env sk.EventEnvelope, customerID string) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(env.Payload, &raw); err != nil {
		return nil, fmt.Errorf("integration: payload: %w", err)
	}
	data := map[string]any{}
	switch env.AggregateType {
	case "invoice":
		data["object"] = invoiceObject(extType, raw, env.AggregateVersion)
	default:
		obj := map[string]any{}
		if snap, ok := raw["subscription"].(map[string]any); ok {
			for k, v := range snap {
				switch k {
				case "tenant_id":
				case "subscription_id":
					obj["id"] = v
				case "customer_account_id":
					obj["customer_id"] = customerID
				default:
					obj[k] = v
				}
			}
		}
		obj["object"] = "subscription"
		obj["version"] = env.AggregateVersion
		data["object"] = obj
		for k, v := range raw {
			if k != "subscription" && k != "tenant_id" && k != "customer_account_id" && k != "subscription_id" {
				data[k] = v
			}
		}
	}
	var tenant *sk.TenantID
	if env.TenantID != nil {
		tenant = env.TenantID
	}
	body, err := json.Marshal(map[string]any{
		"id": eventID, "type": extType, "api_version": APIVersion, "created_at": env.OccurredAt.UTC().Truncate(time.Second).Format(time.RFC3339),
		"tenant_id": tenant, "data": data,
	})
	if err != nil {
		return nil, fmt.Errorf("integration: marshal payload: %w", err)
	}
	if len(body) > MaxPayloadBytes {
		return nil, ErrPayloadTooLarge
	}
	return body, nil
}

// invoiceObject は invoice の項目（§4.2。customer_id は入れない。P-INT-3）。
func invoiceObject(extType string, raw map[string]any, version int64) map[string]any {
	obj := map[string]any{"object": "invoice", "id": raw["invoice_id"], "subscription_id": raw["subscription_id"], "version": version}
	if a, ok := raw["amount"]; ok {
		obj["amount"] = a
	}
	switch extType {
	case "invoice.paid":
		obj["paid_at"] = raw["paid_at"]
	case "invoice.payment_failed":
		obj["attempt_no"] = raw["attempt_no"]
		obj["next_collection_at"] = raw["next_collection_at"]
		obj["failure_code"] = raw["failure_code"]
	}
	return obj
}
