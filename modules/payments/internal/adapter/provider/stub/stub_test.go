package stub_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"subscription-platform/modules/payments/internal/adapter/provider"
	"subscription-platform/modules/payments/internal/adapter/provider/stub"
	"subscription-platform/modules/payments/internal/domain"
)

// AC-PAY-010: 結果の決定規則（§4.5 の表の全行と優先順位）
func TestDecide(t *testing.T) {
	rows := []struct {
		scenario  string
		amount    int64
		collect   provider.OutcomeStatus
		code      domain.FailureCode
		retryable bool
	}{
		{"ok", 0, provider.OutcomeSucceeded, "", false},
		{"decline", 999401, provider.OutcomeFailed, domain.FailCardDeclined, true},
		{"insufficient", 999402, provider.OutcomeFailed, domain.FailInsufficientFunds, true},
		{"expired", 999403, provider.OutcomeFailed, domain.FailCardExpired, false},
		{"invalid", 999422, provider.OutcomeFailed, domain.FailInvalidCard, false},
		{"timeout", 999408, provider.OutcomeUnknown, "", false},
		{"timeout_fail", 999409, provider.OutcomeUnknown, domain.FailCardDeclined, true},
		{"timeout_lost", 999410, provider.OutcomeUnknown, "", false},
		{"async_ok", 999202, provider.OutcomePendingAsync, "", false},
		{"async_fail", 999203, provider.OutcomePendingAsync, "", false},
	}
	for _, r := range rows {
		// トークンのシナリオで決まる
		if got := stub.Decide("", 980, r.scenario); got != r.scenario {
			t.Errorf("token %s → %s", r.scenario, got)
		}
		// 特別な金額はトークンより優先
		if r.amount != 0 {
			if got := stub.Decide("", r.amount, "ok"); got != r.scenario {
				t.Errorf("amount %d → %s", r.amount, got)
			}
		}
		collect, code, retryable, ok := stub.RuleFor(r.scenario)
		if !ok || collect != r.collect || code != r.code || retryable != r.retryable {
			t.Errorf("%s: %s %s %v", r.scenario, collect, code, retryable)
		}
	}
	// 上書きは金額・トークンより優先
	if got := stub.Decide("decline", 999408, "ok"); got != "decline" {
		t.Errorf("上書きの優先: %s", got)
	}
	if got := stub.Decide("", 980, "unknown_scenario"); got != "ok" {
		t.Errorf("未知のシナリオは ok: %s", got)
	}
}

func TestParseToken(t *testing.T) {
	tok, err := stub.ParseToken("tok_stub_ok_4242_1228_ab12cd34")
	if err != nil || tok.Scenario != "ok" || tok.Last4 != "4242" || tok.ExpMonth != 12 || tok.ExpYear != 2028 {
		t.Errorf("%+v %v", tok, err)
	}
	for _, bad := range []string{"tok_stub_ok_4242_1328_ab12cd34", "tok_stub_ok_424_1228_ab12cd34", "4242424242424242", "tok_stub_OK_4242_1228_ab12cd34"} {
		if _, err := stub.ParseToken(bad); err == nil {
			t.Errorf("形式不正を受け付けた: %s", bad)
		}
	}
}

// スタブの Webhook 署名
func TestWebhookSignature(t *testing.T) {
	a := stub.New("stub", nil, nil, "secret")
	body := []byte(`{"event_id":"evt_1","order_ref":"stub_x","status":"succeeded","occurred_at":"2026-10-25T00:00:00Z"}`)
	h := http.Header{}
	h.Set("X-Stub-Signature", a.Sign(body, time.Now()))
	evs, err := a.ParseWebhook(context.Background(), h, body)
	if err != nil || len(evs) != 1 || evs[0].ProviderOrderRef != "stub_x" || evs[0].Outcome.Status != provider.OutcomeSucceeded {
		t.Fatalf("%+v %v", evs, err)
	}
	other := stub.New("stub", nil, nil, "other")
	h.Set("X-Stub-Signature", other.Sign(body, time.Now()))
	if _, err := a.ParseWebhook(context.Background(), h, body); !errors.Is(err, provider.ErrInvalidSignature) {
		t.Errorf("別の鍵: %v", err)
	}
	h.Set("X-Stub-Signature", a.Sign(body, time.Now().Add(-6*time.Minute)))
	if _, err := a.ParseWebhook(context.Background(), h, body); !errors.Is(err, provider.ErrInvalidSignature) {
		t.Errorf("古い署名: %v", err)
	}
}
