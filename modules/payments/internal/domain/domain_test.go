package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/payments/contracts"
	"subscription-platform/modules/payments/internal/domain"
)

var now = time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)

func attempt(st c.AttemptStatus) *domain.Attempt {
	a := domain.NewAttempt(domain.AttemptState{ID: sk.NewID[sk.PaymentAttemptTag](), RequestedAt: now})
	switch st {
	case c.AttemptUnknown:
		_ = a.MarkUnknown()
	case c.AttemptPendingAsync:
		_ = a.AcceptAsync()
	case c.AttemptSucceeded:
		_ = a.Succeed(now, domain.ViaSync)
	case c.AttemptFailed:
		_ = a.Fail(domain.FailCardDeclined, true, "", now, domain.ViaSync)
	case c.AttemptProcessing:
	}
	a.Events()
	return a
}

// §3.2 の状態遷移表
func TestAttemptTransitions(t *testing.T) {
	ops := map[string]func(a *domain.Attempt) error{
		"succeed": func(a *domain.Attempt) error { return a.Succeed(now, domain.ViaWebhook) },
		"fail": func(a *domain.Attempt) error {
			return a.Fail(domain.FailCardDeclined, true, "", now, domain.ViaWebhook)
		},
		"async":   func(a *domain.Attempt) error { return a.AcceptAsync() },
		"unknown": func(a *domain.Attempt) error { return a.MarkUnknown() },
	}
	cases := []struct {
		from  c.AttemptStatus
		op    string
		to    c.AttemptStatus
		event string
		err   error
	}{
		{c.AttemptProcessing, "succeed", c.AttemptSucceeded, c.EventPaymentSucceeded, nil},
		{c.AttemptProcessing, "fail", c.AttemptFailed, c.EventPaymentFailed, nil},
		{c.AttemptProcessing, "async", c.AttemptPendingAsync, c.EventPaymentPending, nil},
		{c.AttemptProcessing, "unknown", c.AttemptUnknown, c.EventPaymentOutcomeUnknown, nil},
		{c.AttemptUnknown, "succeed", c.AttemptSucceeded, c.EventPaymentSucceeded, nil},
		{c.AttemptUnknown, "async", c.AttemptPendingAsync, c.EventPaymentPending, nil},
		{c.AttemptPendingAsync, "fail", c.AttemptFailed, c.EventPaymentFailed, nil},
		{c.AttemptSucceeded, "succeed", c.AttemptSucceeded, "", domain.ErrSameOutcome},
		{c.AttemptSucceeded, "fail", c.AttemptSucceeded, "", c.ErrAttemptFinalized},
		{c.AttemptFailed, "succeed", c.AttemptFailed, "", c.ErrAttemptFinalized},
		{c.AttemptFailed, "async", c.AttemptFailed, "", c.ErrAttemptFinalized},
	}
	for _, tc := range cases {
		a := attempt(tc.from)
		err := ops[tc.op](a)
		if !errors.Is(err, tc.err) {
			t.Errorf("%s→%s: err = %v, want %v", tc.from, tc.op, err, tc.err)
		}
		if a.State().Status != tc.to {
			t.Errorf("%s→%s: status = %s", tc.from, tc.op, a.State().Status)
		}
		evs := a.Events()
		if (tc.event == "") != (len(evs) == 0) || (len(evs) == 1 && evs[0].Type != tc.event) {
			t.Errorf("%s→%s: events = %v", tc.from, tc.op, evs)
		}
	}
}

// 手動確定（§7.6）
func TestManualResolve(t *testing.T) {
	young := attempt(c.AttemptProcessing)
	if err := young.ResolveManually(false, domain.FailManual, true, "op", "x", now.Add(10*time.Second)); !errors.Is(err, c.ErrAttemptNotResolvable) {
		t.Errorf("30 秒以内の processing: %v", err)
	}
	old := attempt(c.AttemptProcessing)
	if err := old.ResolveManually(false, domain.FailManual, true, "op", "理由", now.Add(31*time.Second)); err != nil {
		t.Fatal(err)
	}
	st := old.State()
	if st.Status != c.AttemptFailed || *st.Resolution != domain.ViaManual || *st.ResolvedBy != "op" {
		t.Errorf("%+v", st)
	}
	done := attempt(c.AttemptSucceeded)
	if err := done.ResolveManually(true, "", false, "op", "x", now); !errors.Is(err, domain.ErrSameOutcome) {
		t.Errorf("同じ結果: %v", err)
	}
	if err := done.ResolveManually(false, domain.FailManual, false, "op", "x", now); !errors.Is(err, c.ErrAttemptFinalized) {
		t.Errorf("違う結果: %v", err)
	}
}

// AC-PAY-034: 冪等キーの検証
func TestIdempotencyKey(t *testing.T) {
	inv := sk.NewID[sk.InvoiceTag]()
	payable := c.PayableRef{Type: c.PayableInvoice, ID: inv.String()}
	if seq, err := domain.ParseIdempotencyKey(c.IdempotencyKeyFor(inv, 3), payable); err != nil || seq != 3 {
		t.Errorf("正しい形式: %d %v", seq, err)
	}
	for name, key := range map[string]string{
		"invoice_id 不一致": c.IdempotencyKeyFor(sk.NewID[sk.InvoiceTag](), 1),
		"連番 0":           "invoice:" + inv.String() + ":0",
		"長すぎる連番":         "invoice:" + inv.String() + ":123456",
		"接頭辞違い":          "order:" + inv.String() + ":1",
		"空":              "",
	} {
		if _, err := domain.ParseIdempotencyKey(key, payable); !errors.Is(err, c.ErrInvalidIdempotencyKey) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if !domain.ValidAmount(sk.MustMoney(1)) || !domain.ValidAmount(sk.MustMoney(1_000_000)) || domain.ValidAmount(sk.MustMoney(0)) || domain.ValidAmount(sk.MustMoney(1_000_001)) {
		t.Error("金額の範囲（E-09）")
	}
}

// §3.4 の FailureCode と retryable・契約者向け分類の対応
func TestFailureCodes(t *testing.T) {
	for code, want := range map[domain.FailureCode]struct {
		retry bool
		cat   string
	}{
		domain.FailCardDeclined: {true, "generic"}, domain.FailInsufficientFunds: {true, "insufficient_funds"}, domain.FailCardExpired: {false, "card_expired"},
		domain.FailInvalidCard: {false, "generic"}, domain.FailPaymentMethodUnavailable: {true, "generic"}, domain.FailMethodReentryRequired: {false, "generic"},
		domain.FailNotProcessed: {true, "generic"}, domain.FailProcessingError: {true, "generic"}, domain.FailAsyncExpired: {false, "generic"},
	} {
		if code.DefaultRetryable() != want.retry || code.ConsumerCategory() != want.cat {
			t.Errorf("%s: retryable=%v category=%s", code, code.DefaultRetryable(), code.ConsumerCategory())
		}
	}
}

// 支払方法：参照の選び方・退会のマスキング・有効期限
func TestPaymentMethod(t *testing.T) {
	shop := "shop_a"
	m := domain.NewPaymentMethod(sk.NewID[sk.PaymentMethodTag](), sk.NewID[sk.CustomerAccountTag](), c.MethodCard,
		domain.CardDisplay{Brand: "visa", Last4: "4242", ExpMonth: 12, ExpYear: 2028}, domain.ProviderRef{Provider: "stub", CustomerRef: "c", MethodRef: "shared"}, now)
	if r, ok := m.RefFor(&shop); !ok || r.MethodRef != "shared" {
		t.Error("事業者向けがなければ共有参照")
	}
	m.AddProviderRef(domain.ProviderRef{Provider: "stub", TenantShopID: &shop, MethodRef: "tenant"})
	if r, _ := m.RefFor(&shop); r.MethodRef != "tenant" {
		t.Error("事業者向けを優先する")
	}
	if m.AddProviderRef(domain.ProviderRef{Provider: "stub", TenantShopID: &shop}) {
		t.Error("同じ (provider, shop) は重複しない")
	}
	if m.Expired(sk.MustDate(2028, 12, 31)) || !m.Expired(sk.MustDate(2029, 1, 1)) {
		t.Error("有効期限の判定")
	}
	if !m.DeleteOnWithdrawal(now) || m.DeleteOnWithdrawal(now) {
		t.Error("退会の削除は 1 回だけ")
	}
	if d := m.State().Display; d.Last4 != "****" || d.Brand != "" || strings.Contains(m.View(sk.MustDate(2026, 1, 1)).Display["last4"].(string), "4242") {
		t.Errorf("マスキング: %+v", d)
	}
}
