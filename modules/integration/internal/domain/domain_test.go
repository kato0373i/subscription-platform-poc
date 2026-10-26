package domain_test

import (
	"encoding/json"
	"errors"
	"net/netip"
	"regexp"
	"strings"
	"testing"
	"time"

	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/integration/internal/domain"
)

var local = domain.InsecureHosts{"localhost", "127.0.0.1", "webhook-sink"}

func urlCode(err error) string {
	var ue *domain.URLError
	if errors.As(err, &ue) {
		return ue.Code
	}
	return ""
}

// AC-INT-001〜003・E19：URL の検証（U1〜U5）。
func TestValidateURL(t *testing.T) {
	resolve := func(host string) ([]netip.Addr, error) {
		switch host {
		case "internal.example":
			return []netip.Addr{netip.MustParseAddr("10.1.2.3")}, nil
		case "mixed.example":
			return []netip.Addr{netip.MustParseAddr("10.1.2.3"), netip.MustParseAddr("93.184.216.34")}, nil
		case "example.com":
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}
		return nil, errors.New("nxdomain")
	}
	long := "https://example.com/" + strings.Repeat("a", domain.MaxURLLength-len("https://example.com/"))
	cases := []struct{ url, want string }{
		{"https://example.com/hook", ""},
		{"http://example.com/hook", domain.CodeURLNotHTTPS},
		{"ftp://example.com/", domain.CodeURLNotHTTPS},
		{"https://10.0.0.5/", domain.CodeURLForbiddenHost},
		{"https://169.254.169.254/", domain.CodeURLForbiddenHost},
		{"https://[::1]/", domain.CodeURLForbiddenHost},
		{"https://[::ffff:192.168.0.1]/", domain.CodeURLForbiddenHost},
		{"https://100.64.0.1/", domain.CodeURLForbiddenHost},
		{"https://93.184.216.34/", ""},
		{"https://internal.example/", domain.CodeURLForbiddenHost},
		{"https://mixed.example/", ""},
		{"https://unresolvable.example/", ""},
		{"http://webhook-sink:8080/hooks/x", ""},
		{"http://localhost:18080/hooks/x", ""},
		{"http://other-host/hooks/x", domain.CodeURLNotHTTPS},
		{"https://user:pass@example.com/", domain.CodeURLInvalid},
		{"https://example.com/#frag", domain.CodeURLInvalid},
		{"/relative", domain.CodeURLInvalid},
		{"", domain.CodeURLInvalid},
		{long, ""},
		{long + "a", domain.CodeURLInvalid},
	}
	for _, c := range cases {
		if got := urlCode(domain.ValidateURL(c.url, local, resolve)); got != c.want {
			t.Errorf("%.60s: got %q, want %q", c.url, got, c.want)
		}
	}
}

// AC-INT-020：Dialer.Control が使う判定。
func TestDialAllowed(t *testing.T) {
	cases := []struct {
		host, ip string
		want     bool
	}{
		{"example.com", "93.184.216.34", true},
		{"example.com", "10.0.0.1", false},
		{"example.com", "172.16.5.4", false},
		{"example.com", "192.168.1.1", false},
		{"example.com", "127.0.0.1", false},
		{"example.com", "169.254.169.254", false},
		{"example.com", "100.64.0.1", false},
		{"example.com", "0.0.0.0", false},
		{"example.com", "224.0.0.1", false},
		{"example.com", "240.0.0.1", false},
		{"example.com", "::1", false},
		{"example.com", "::", false},
		{"example.com", "fc00::1", false},
		{"example.com", "fe80::1", false},
		{"example.com", "::ffff:10.0.0.1", false},
		{"example.com", "2606:2800:220:1:248:1893:25c8:1946", true},
		{"webhook-sink", "172.18.0.5", true},
		{"localhost", "127.0.0.1", true},
	}
	for _, c := range cases {
		if got := domain.DialAllowed(c.host, netip.MustParseAddr(c.ip), local); got != c.want {
			t.Errorf("%s %s: got %v", c.host, c.ip, got)
		}
	}
}

// AC-INT-010：内部イベント → 外部 type。
func TestExternalType(t *testing.T) {
	env := func(typ, payload string) sk.EventEnvelope {
		return sk.EventEnvelope{EventType: typ, Payload: json.RawMessage(payload)}
	}
	cases := []struct {
		env  sk.EventEnvelope
		want string
	}{
		{env("subscription.signup_failed", `{}`), ""},
		{env("subscription.payment_method_changed", `{}`), ""},
		{env("billing.invoice_payment_failed", `{"collection_mode":"caller_managed"}`), ""},
		{env("billing.invoice_payment_failed", `{"collection_mode":"scheduled"}`), "invoice.payment_failed"},
		{env("billing.invoice_payment_failed", `{}`), "invoice.payment_failed"},
		{env("billing.invoice_paid", `{}`), "invoice.paid"},
		{env("billing.invoice_issued", `{}`), ""},
		{env("customer.withdrawn", `{}`), ""},
	}
	for _, c := range cases {
		got, ok := domain.ExternalType(c.env)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s: got %q %v", c.env.EventType, got, ok)
		}
	}
	n := 0
	for _, typ := range domain.ExternalEventTypes {
		internal := typ
		switch typ {
		case "invoice.paid":
			internal = "billing.invoice_paid"
		case "invoice.payment_failed":
			internal = "billing.invoice_payment_failed"
		}
		if got, ok := domain.ExternalType(env(internal, `{}`)); ok && got == typ {
			n++
		}
	}
	if n != 13 || len(domain.WebhookEventTypes()) != 13 {
		t.Fatalf("13 種類にならない: %d", n)
	}
}

func TestEventID(t *testing.T) {
	re := regexp.MustCompile(`^evt_[0-9A-HJKMNP-TV-Z]{26}$`)
	seen := map[string]bool{}
	for range 1000 {
		id := domain.NewEventID()
		if !re.MatchString(id) || seen[id] {
			t.Fatalf("bad or duplicate id %s", id)
		}
		seen[id] = true
	}
	var max [16]byte
	for i := range max {
		max[i] = 0xff
	}
	if got := domain.EventIDFrom(max); got != "evt_7ZZZZZZZZZZZZZZZZZZZZZZZZZ" {
		t.Fatalf("max = %s", got)
	}
	if got := domain.EventIDFrom([16]byte{15: 1}); got != "evt_00000000000000000000000001" {
		t.Fatalf("one = %s", got)
	}
}

// AC-INT-008 の Go 側：本文に customer_id = cus_…・version があり、customer_account_id・tenant_id（object の中）・内部 event_id がない。
func TestBuildPayloadSubscription(t *testing.T) {
	tenant := sk.NewID[sk.TenantTag]()
	env := sk.EventEnvelope{
		EventID: sk.NewID[sk.EventTag](), EventType: "subscription.renewed", TenantID: &tenant, AggregateType: "subscription",
		AggregateVersion: 7, OccurredAt: time.Date(2026, 10, 15, 1, 0, 3, 500, time.UTC), CorrelationID: "corr-1",
		Payload: json.RawMessage(`{"subscription":{"subscription_id":"s1","tenant_id":"t1","customer_account_id":"internal-account-c1","plan_id":"p1","status":"active",
			"current_period":{"start":"2026-10-15","end":"2026-11-14"},"version":7},"invoice_id":"i1"}`),
	}
	body, err := domain.BuildPayload("evt_X", "subscription.renewed", env, "cus_abc")
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, bad := range []string{"customer_account_id", "internal-account-c1", env.EventID.String(), "corr-1", `"t1"`} {
		if strings.Contains(s, bad) {
			t.Errorf("本文に %s が含まれる: %s", bad, s)
		}
	}
	var got struct {
		ID        string `json:"id"`
		CreatedAt string `json:"created_at"`
		Data      struct {
			Object    map[string]any `json:"object"`
			InvoiceID string         `json:"invoice_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	o := got.Data.Object
	if got.ID != "evt_X" || got.CreatedAt != "2026-10-15T01:00:03Z" || o["object"] != "subscription" || o["id"] != "s1" ||
		o["customer_id"] != "cus_abc" || o["version"] != float64(7) || got.Data.InvoiceID != "i1" {
		t.Fatalf("payload = %s", s)
	}
	again, _ := domain.BuildPayload("evt_X", "subscription.renewed", env, "cus_abc")
	if string(again) != s {
		t.Fatal("同じ入力で同じバイト列にならない")
	}
}

func TestBuildPayloadInvoice(t *testing.T) {
	env := sk.EventEnvelope{
		EventType: "billing.invoice_payment_failed", AggregateType: "invoice", AggregateVersion: 3, OccurredAt: time.Now(),
		Payload: json.RawMessage(`{"invoice_id":"i1","subscription_id":"s1","amount":980,"attempt_no":2,"next_collection_at":null,"failure_code":"card_declined","customer_account_id":"internal-account-c1"}`),
	}
	body, err := domain.BuildPayload("evt_Y", "invoice.payment_failed", env, "")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Data struct{ Object map[string]any }
	}
	_ = json.Unmarshal(body, &got)
	o := got.Data.Object
	if o["object"] != "invoice" || o["id"] != "i1" || o["amount"] != float64(980) || o["attempt_no"] != float64(2) || o["failure_code"] != "card_declined" {
		t.Fatalf("payload = %s", body)
	}
	if _, ok := o["customer_id"]; ok || strings.Contains(string(body), "internal-account-c1") {
		t.Fatalf("invoice に customer が入っている: %s", body)
	}
}

// バックオフ（§8.2）と状態遷移（§3.3）。AC-INT-012・013 の時刻を本番の値で確かめる。
func TestDeliveryBackoff(t *testing.T) {
	s := domain.DefaultSchedule()
	s.Jitter = 0
	t0 := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	ep := &domain.Endpoint{ID: sk.NewID[domain.EndpointTag]()}
	fail := func(at time.Time) domain.AttemptResult {
		c := 500
		return domain.AttemptResult{RequestedAt: at, StatusCode: &c, ErrorKind: domain.ErrHTTPStatus}
	}

	// 最初の 4 回は 500 → 5 回目（2 時間 36 分）で成功
	d := domain.NewAutoDelivery(ep, "evt_X", t0)
	var times []time.Duration
	for range 4 {
		at := *d.NextAttemptAt
		times = append(times, at.Sub(t0))
		if err := d.Record(fail(at), at, s); err != nil {
			t.Fatal(err)
		}
	}
	at := *d.NextAttemptAt
	times = append(times, at.Sub(t0))
	ok := 200
	if err := d.Record(domain.AttemptResult{RequestedAt: at, StatusCode: &ok}, at, s); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{0, time.Minute, 6 * time.Minute, 36 * time.Minute, 2*time.Hour + 36*time.Minute}
	for i := range want {
		if times[i] != want[i] {
			t.Fatalf("試行の時刻 %v, want %v", times, want)
		}
	}
	if d.Status != domain.DeliverySucceeded || d.AttemptCount != 5 || d.NextAttemptAt != nil {
		t.Fatalf("delivery = %+v", d)
	}
	if err := d.Record(fail(at), at, s); !errors.Is(err, domain.ErrNotPending) {
		t.Fatal("終端の配信が変わった")
	}

	// 常に 500 → 11 回目の後に failed（次の予定 77h36m が 72h を超える）
	d = domain.NewAutoDelivery(ep, "evt_X", t0)
	for d.Status == domain.DeliveryPending {
		at := *d.NextAttemptAt
		_ = d.Record(fail(at), at, s)
	}
	if d.Status != domain.DeliveryFailed || d.AttemptCount != 11 || d.LastAttemptedAt.Sub(t0) != 65*time.Hour+36*time.Minute {
		t.Fatalf("failed: count=%d last=%v", d.AttemptCount, d.LastAttemptedAt.Sub(t0))
	}
}

// E9：予定時刻 = deadline ちょうどなら試行する。
func TestDeliveryDeadlineBoundary(t *testing.T) {
	s := domain.Schedule{Waits: []time.Duration{time.Hour}, GiveUp: 2 * time.Hour}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := domain.NewAutoDelivery(&domain.Endpoint{}, "evt_X", t0)
	c := 500
	for i := range 2 {
		at := *d.NextAttemptAt
		_ = d.Record(domain.AttemptResult{RequestedAt: at, StatusCode: &c, ErrorKind: domain.ErrHTTPStatus}, at, s)
		if d.Status != domain.DeliveryPending {
			t.Fatalf("%d 回目で %s", i+1, d.Status)
		}
	}
	if d.NextAttemptAt.Sub(t0) != 2*time.Hour {
		t.Fatal(d.NextAttemptAt)
	}
	at := *d.NextAttemptAt
	_ = d.Record(domain.AttemptResult{RequestedAt: at, StatusCode: &c, ErrorKind: domain.ErrHTTPStatus}, at, s)
	if d.Status != domain.DeliveryFailed {
		t.Fatal(d.Status)
	}
}

func TestPostponeAndAbandon(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := domain.NewAutoDelivery(&domain.Endpoint{}, "evt_X", t0)
	_ = d.Postpone(t0.Add(time.Minute), t0)
	if d.AttemptCount != 0 || !d.NextAttemptAt.Equal(t0.Add(time.Minute)) {
		t.Fatal("postpone で attempt_count が変わった")
	}
	_ = d.Abandon(t0)
	if d.Status != domain.DeliveryAbandoned || d.NextAttemptAt != nil {
		t.Fatal(d.Status)
	}
	m := domain.NewManualDelivery(d, t0)
	if m.Trigger != domain.TriggerManual || *m.RedeliveryOf != d.ID || m.EventID != "evt_X" || m.Status != domain.DeliveryPending {
		t.Fatalf("manual = %+v", m)
	}
}

func TestJitterRange(t *testing.T) {
	s := domain.DefaultSchedule()
	for range 200 {
		w := s.Wait(1)
		if w < 54*time.Second || w > 66*time.Second {
			t.Fatalf("jitter out of range: %v", w)
		}
	}
	if _, err := domain.ParseSchedule("1s,2s,4s,8s", 30*time.Second, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := domain.ParseSchedule("1s,x", time.Minute, 0); err == nil {
		t.Fatal("不正な値を受け付けた")
	}
}

// E7・E8：ローテーション。
func TestRotateSecret(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ep, err := domain.NewEndpoint(sk.NewID[sk.TenantTag](), "https://example.com", []string{"invoice.paid", "invoice.paid"}, "", true, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ep.EventTypes) != 1 || !strings.HasPrefix(string(ep.Current), "whsec_") {
		t.Fatalf("endpoint = %+v", ep)
	}
	k1 := ep.Current
	_ = ep.RotateSecret(t0, 24)
	k2 := ep.Current
	if got := ep.ActiveSecrets(t0.Add(time.Hour)); len(got) != 2 || got[0] != string(k2) || got[1] != string(k1) {
		t.Fatalf("移行期間中: %v", got)
	}
	if got := ep.ActiveSecrets(t0.Add(24 * time.Hour)); len(got) != 1 || got[0] != string(k2) {
		t.Fatalf("期限後: %v", got)
	}
	_ = ep.RotateSecret(t0.Add(time.Hour), 24)
	if *ep.Previous != k2 || ep.Current == k2 || ep.Current == k1 {
		t.Fatal("2 回目のローテーションで K1 が残った")
	}
	_ = ep.RotateSecret(t0, 0)
	if ep.Previous != nil || len(ep.ActiveSecrets(t0)) != 1 {
		t.Fatal("grace 0 で previous が残った")
	}
	if err := ep.RotateSecret(t0, 169); !errors.Is(err, domain.ErrGracePeriod) {
		t.Fatal(err)
	}
	if _, err := domain.NewEndpoint(ep.TenantID, "https://x", []string{"unknown"}, "", true, t0); !errors.Is(err, domain.ErrEventTypeUnknown) {
		t.Fatal(err)
	}
	if _, err := domain.NewEndpoint(ep.TenantID, "https://x", nil, "", true, t0); !errors.Is(err, domain.ErrEventTypesEmpty) {
		t.Fatal(err)
	}
}

func TestBreaker(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ep := &domain.Endpoint{}
	for i := range 4 {
		ep.RecordDeliveryResult(false, t0, domain.DefaultBreaker)
		if !ep.BreakerAllows(t0) {
			t.Fatalf("%d 回で開いた", i+1)
		}
	}
	ep.RecordDeliveryResult(false, t0, domain.DefaultBreaker)
	if ep.BreakerAllows(t0.Add(59*time.Second)) || !ep.BreakerAllows(t0.Add(60*time.Second)) {
		t.Fatal("5 回で 60 秒開かない")
	}
	ep.RecordDeliveryResult(false, t0.Add(60*time.Second), domain.DefaultBreaker) // 半開の試しの失敗
	if ep.BreakerAllows(t0.Add(61 * time.Second)) {
		t.Fatal("半開の失敗で再び開かない")
	}
	ep.RecordDeliveryResult(true, t0.Add(2*time.Minute), domain.DefaultBreaker)
	if ep.BreakerFailures != 0 || ep.BreakerOpenUntil != nil {
		t.Fatal("成功で閉じない")
	}
}

// AC-INT-025：6 種類のきっかけ → template_key・dedupe_key・変数。変数に氏名・メールアドレスがない。
func TestPlanEmail(t *testing.T) {
	tenant, sub, cust, inv := sk.NewID[sk.TenantTag](), sk.NewID[sk.SubscriptionTag](), sk.NewID[sk.CustomerAccountTag](), sk.NewID[sk.InvoiceTag]()
	snap := func(extra string) string {
		return `{"subscription":{"subscription_id":"` + sub.String() + `","tenant_id":"` + tenant.String() + `","customer_account_id":"` + cust.String() +
			`","status":"trialing","trial_end":"2026-10-21","next_renewal_date":"2026-11-15","canceled_at":"2026-10-20T03:00:00Z",
			"current_period":{"start":"2026-10-15","end":"2026-11-14"},"cancellation":{"effective_date":"2026-11-14"}}` + extra + `}`
	}
	cases := []struct {
		typ, payload, tmpl, dedupe string
		vars                       map[string]any
	}{
		{"subscription.started", snap(""), domain.TmplSignupCompleted, "signup_completed:" + sub.String(),
			map[string]any{"trial_end": "2026-10-21", "first_charge_date": "2026-10-22", "status": "trialing"}},
		{"subscription.trial_will_end", snap(`,"trial_end":"2026-10-21"`), domain.TmplTrialWillEnd, "trial_will_end:" + sub.String() + ":2026-10-21",
			map[string]any{"first_charge_date": "2026-10-22"}},
		{"subscription.renewed", snap(`,"invoice_id":"` + inv.String() + `"`), domain.TmplRenewalSucceeded, "renewal_succeeded:" + inv.String(),
			map[string]any{"next_renewal_date": "2026-11-15"}},
		{"subscription.past_due", snap(`,"invoice_id":"` + inv.String() + `"`), domain.TmplPaymentFailed, "payment_failed:" + inv.String(), map[string]any{}},
		{"subscription.cancellation_scheduled", snap(`,"reason":"tenant_suspended","effective_date":"2026-11-14"`), domain.TmplCancellationScheduled,
			"cancellation_scheduled:" + sub.String() + ":9", map[string]any{"reason": "tenant_suspended", "effective_date": "2026-11-14"}},
		{"subscription.cancellation_scheduled", snap(""), domain.TmplCancellationScheduled, "cancellation_scheduled:" + sub.String() + ":9",
			map[string]any{"reason": "customer", "effective_date": "2026-11-14"}},
		{"subscription.canceled", snap(`,"reason":"dunning"`), domain.TmplSubscriptionCanceled, "subscription_canceled:" + sub.String(),
			map[string]any{"reason": "dunning", "canceled_on": "2026-10-20"}},
	}
	for _, c := range cases {
		p, err := domain.PlanEmail(sk.EventEnvelope{EventType: c.typ, AggregateVersion: 9, Payload: json.RawMessage(c.payload)})
		if err != nil || p == nil {
			t.Fatalf("%s: %v", c.typ, err)
		}
		if p.TemplateKey != c.tmpl || p.DedupeKey != c.dedupe || p.Recipient != domain.RecipientCustomer || *p.CustomerID != cust || p.TenantID != tenant {
			t.Errorf("%s: %+v", c.typ, p)
		}
		for k, v := range c.vars {
			if p.Variables[k] != v {
				t.Errorf("%s: %s = %v, want %v", c.typ, k, p.Variables[k], v)
			}
		}
		b, _ := json.Marshal(p.Variables)
		for _, bad := range []string{"email", "name", "@"} {
			if strings.Contains(string(b), bad) {
				t.Errorf("%s: 変数に %s: %s", c.typ, bad, b)
			}
		}
	}
	for _, typ := range []string{"subscription.recovered", "billing.invoice_issued", "customer.withdrawn"} {
		if p, _ := domain.PlanEmail(sk.EventEnvelope{EventType: typ, Payload: json.RawMessage(snap(""))}); p != nil {
			t.Errorf("%s でメールを作った", typ)
		}
	}
}

func TestEmailNextAttempt(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	next, ok := domain.EmailNextAttempt(t0, t0, 1)
	if !ok || next.Sub(t0) != time.Minute {
		t.Fatal(next)
	}
	if _, ok := domain.EmailNextAttempt(t0, t0.Add(23*time.Hour), 6); ok {
		t.Fatal("24 時間を超えても打ち切らない")
	}
	p := domain.WebhookFailedPlan(sk.NewID[sk.TenantTag](), sk.NewID[domain.EndpointTag](), time.Date(2026, 10, 14, 16, 0, 0, 0, time.UTC))
	if !strings.HasSuffix(p.DedupeKey, ":2026-10-15") || p.Recipient != domain.RecipientTenantOwner {
		t.Fatalf("JST の日付にならない: %s", p.DedupeKey)
	}
}
