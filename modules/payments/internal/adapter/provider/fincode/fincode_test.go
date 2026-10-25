package fincode_test

// fincode の API（docs.fincode.jp の OpenAPI 仕様 1.4.0）に合わせた要求を送り、応答を正しく解釈することを確かめる。
// 本物の fincode には繋がない（認証情報が必要）。代わりに 127.0.0.1 の HTTP サーバーが仕様の応答例どおりに返す（P-IMP-21）。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/payments/contracts"
	"subscription-platform/modules/payments/internal/adapter/provider"
	"subscription-platform/modules/payments/internal/adapter/provider/fincode"
	"subscription-platform/modules/payments/internal/domain"
)

type call struct {
	Method, Path, Query string
	Header              http.Header
	Body                map[string]any
}

// server は fincode の API の代わり。route が (status, body) を返す。
type server struct {
	t     *testing.T
	srv   *httptest.Server
	mu    sync.Mutex
	calls []call
	route func(c call) (int, string)
}

func newServer(t *testing.T, route func(c call) (int, string)) *server {
	s := &server{t: t, route: route}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		cl := call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone()}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &cl.Body)
		}
		s.mu.Lock()
		s.calls = append(s.calls, cl)
		s.mu.Unlock()
		st, body := s.route(cl)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(st)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *server) adapter(t *testing.T) *fincode.Adapter {
	a, err := fincode.New(fincode.Config{APIBaseURL: s.srv.URL, SecretKey: "m_test_secret", WebhookSignature: "sig-123", SharedCustomer: true,
		HTTPClient: &http.Client{Timeout: 2 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func errBody(code string) string {
	return `{"errors":[{"error_code":"` + code + `","error_message":"x"}]}`
}

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func collectInput(a *fincode.Adapter) provider.CollectInput {
	id := uuid.Must(uuid.NewV7())
	shop := "s_tenant0001"
	return provider.CollectInput{
		AttemptID: id, ProviderOrderRef: a.OrderRefFor(id), IdempotencyKey: id.String(), Amount: sk.MustMoney(980), TenantShopID: &shop,
		Ref: domain.ProviderRef{Provider: "fincode", CustomerRef: "c_abc", MethodRef: "cs_card1"},
	}
}

const registered = `{"id":"%s","access_id":"a_123456789012345678901234","status":"UNPROCESSED","amount":980}`

func TestOrderRefAndIdempotentKey(t *testing.T) {
	s := newServer(t, func(call) (int, string) { return 200, "{}" })
	a := s.adapter(t)
	seen := map[string]bool{}
	for range 1000 {
		ref := a.OrderRefFor(uuid.Must(uuid.NewV7()))
		if len(ref) > 30 || !regexp.MustCompile(`^o[0-9A-Z]{26}$`).MatchString(ref) || seen[ref] {
			t.Fatalf("order id %q（30 文字まで・一意）", ref)
		}
		seen[ref] = true
	}
	id := uuid.Must(uuid.NewV7())
	first := a.OrderRefFor(id)
	if a.OrderRefFor(id) != first {
		t.Fatal("同じ試行で同じオーダー ID にならない")
	}
	if fincode.CustomerID(id) != "c_"+strings.ReplaceAll(id.String(), "-", "") || len(fincode.CustomerID(id)) > 60 {
		t.Fatal(fincode.CustomerID(id))
	}
}

func TestCollectSucceeded(t *testing.T) {
	s := newServer(t, func(cl call) (int, string) {
		switch {
		case cl.Method == http.MethodPost && cl.Path == "/v1/payments":
			return 200, strings.Replace(registered, "%s", cl.Body["id"].(string), 1)
		case cl.Method == http.MethodPut:
			return 200, `{"id":"x","access_id":"a_123456789012345678901234","status":"CAPTURED","amount":980,"approve":"1234567"}`
		}
		return 404, errBody("E0000000000")
	})
	a := s.adapter(t)
	in := collectInput(a)
	out, err := a.Collect(context.Background(), in)
	if err != nil || out.Status != provider.OutcomeSucceeded {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if len(s.calls) != 2 {
		t.Fatalf("calls = %d", len(s.calls))
	}
	reg, exec := s.calls[0], s.calls[1]
	for _, cl := range s.calls {
		if cl.Header.Get("Authorization") != "Bearer m_test_secret" || cl.Header.Get("Tenant-Shop-Id") != "s_tenant0001" || !uuidV4.MatchString(cl.Header.Get("idempotent_key")) {
			t.Errorf("headers = %v", cl.Header)
		}
	}
	if reg.Header.Get("idempotent_key") == exec.Header.Get("idempotent_key") {
		t.Error("登録と実行で冪等キーが同じ")
	}
	if reg.Body["id"] != in.ProviderOrderRef || reg.Body["pay_type"] != "Card" || reg.Body["job_code"] != "CAPTURE" || reg.Body["amount"] != "980" {
		t.Errorf("登録の本文 = %v", reg.Body)
	}
	if exec.Path != "/v1/payments/"+in.ProviderOrderRef || exec.Body["access_id"] != "a_123456789012345678901234" || exec.Body["customer_id"] != "c_abc" ||
		exec.Body["card_id"] != "cs_card1" || exec.Body["method"] != "1" {
		t.Errorf("実行 = %s %v", exec.Path, exec.Body)
	}
	// 再送では同じ冪等キー
	s.calls = nil
	_, _ = a.Collect(context.Background(), in)
	if s.calls[0].Header.Get("idempotent_key") != reg.Header.Get("idempotent_key") {
		t.Error("再送で冪等キーが変わった")
	}
}

func TestCollectFailures(t *testing.T) {
	cases := []struct {
		name         string
		regSt        int
		reg          string
		execSt       int
		exec         string
		wantErr      error
		want         provider.OutcomeStatus
		code         domain.FailureCode
		retryable    bool
		wantExecCall bool
	}{
		{"残高不足", 200, "", 400, errBody("E9993134010"), nil, provider.OutcomeFailed, domain.FailInsufficientFunds, true, true},
		{"有効期限", 200, "", 400, errBody("E9993134032"), nil, provider.OutcomeFailed, domain.FailCardExpired, false, true},
		{"カード番号の誤り", 200, "", 400, errBody("E9993134016"), nil, provider.OutcomeFailed, domain.FailInvalidCard, false, true},
		{"その他の拒否", 200, "", 400, errBody("E9993134012"), nil, provider.OutcomeFailed, domain.FailCardDeclined, true, true},
		{"実行で 500", 200, "", 500, "{}", nil, provider.OutcomeUnknown, "", false, true},
		{"登録で流量制限（403）", 403, "{}", 0, "", provider.ErrUnavailable, "", "", false, false},
		{"登録でメンテナンス（503）", 503, "{}", 0, "", provider.ErrUnavailable, "", "", false, false},
		{"登録で入力エラー", 400, errBody("E0110025008"), 0, "", provider.ErrNotSent, "", "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t, func(cl call) (int, string) {
				if cl.Method == http.MethodPost {
					if tc.reg == "" {
						return 200, strings.Replace(registered, "%s", cl.Body["id"].(string), 1)
					}
					return tc.regSt, tc.reg
				}
				return tc.execSt, tc.exec
			})
			a := s.adapter(t)
			out, err := a.Collect(context.Background(), collectInput(a))
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v", err)
				}
			} else if err != nil || out.Status != tc.want || out.FailureCode != tc.code || (tc.want == provider.OutcomeFailed && out.Retryable != tc.retryable) {
				t.Fatalf("out = %+v err=%v", out, err)
			}
			if (len(s.calls) == 2) != tc.wantExecCall {
				t.Fatalf("calls = %d", len(s.calls))
			}
		})
	}
}

// 実行の応答が届かない（タイムアウト）→ unknown。照会で確定する。
func TestCollectTimeoutIsUnknown(t *testing.T) {
	block := make(chan struct{})
	s := newServer(t, func(cl call) (int, string) {
		if cl.Method == http.MethodPost {
			return 200, strings.Replace(registered, "%s", cl.Body["id"].(string), 1)
		}
		<-block
		return 200, "{}"
	})
	t.Cleanup(func() { close(block) }) // サーバーを閉じる前に、止めているハンドラを放す（Cleanup は後に登録したものから動く）
	a := s.adapter(t)
	out, err := a.Collect(context.Background(), collectInput(a))
	if err != nil || out.Status != provider.OutcomeUnknown {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

// 冪等キーの期限（30 分）を過ぎた再送：オーダー ID の重複 → 照会。決済済みなら実行を送らない。未実行なら実行する。
func TestCollectRetryAfterIdempotencyWindow(t *testing.T) {
	for _, tc := range []struct {
		status   string
		want     provider.OutcomeStatus
		execCall bool
	}{{"CAPTURED", provider.OutcomeSucceeded, false}, {"UNPROCESSED", provider.OutcomeSucceeded, true}} {
		s := newServer(t, func(cl call) (int, string) {
			switch cl.Method {
			case http.MethodPost:
				return 400, errBody("E0110025014")
			case http.MethodGet:
				if cl.Query != "pay_type=Card" {
					return 400, errBody("E0000000001")
				}
				return 200, `{"id":"x","access_id":"a_123456789012345678901234","status":"` + tc.status + `"}`
			}
			return 200, `{"status":"CAPTURED"}`
		})
		a := s.adapter(t)
		out, err := a.Collect(context.Background(), collectInput(a))
		if err != nil || out.Status != tc.want {
			t.Fatalf("%s: out=%+v err=%v", tc.status, out, err)
		}
		var exec bool
		for _, cl := range s.calls {
			exec = exec || cl.Method == http.MethodPut
		}
		if exec != tc.execCall {
			t.Fatalf("%s: 実行を送った = %v", tc.status, exec)
		}
	}
	// 実行が「すでに決済が終了」→ 照会の結果
	s := newServer(t, func(cl call) (int, string) {
		switch cl.Method {
		case http.MethodPost:
			return 200, strings.Replace(registered, "%s", cl.Body["id"].(string), 1)
		case http.MethodPut:
			return 400, errBody("E9993134002")
		}
		return 200, `{"status":"CAPTURED"}`
	})
	a := s.adapter(t)
	if out, _ := a.Collect(context.Background(), collectInput(a)); out.Status != provider.OutcomeSucceeded {
		t.Fatalf("out = %+v", out)
	}
}

func TestGetStatus(t *testing.T) {
	for _, tc := range []struct {
		st   int
		body string
		want provider.OutcomeStatus
	}{
		{200, `{"status":"CAPTURED"}`, provider.OutcomeSucceeded},
		{200, `{"status":"UNPROCESSED"}`, provider.OutcomeNotFound},
		{200, `{"status":"UNPROCESSED","error_code":"E9993134010"}`, provider.OutcomeFailed},
		{200, `{"status":"CANCELED"}`, provider.OutcomeFailed},
		{404, errBody("E0000000404"), provider.OutcomeNotFound},
		{500, "{}", provider.OutcomeUnknown},
	} {
		s := newServer(t, func(call) (int, string) { return tc.st, tc.body })
		out, err := s.adapter(t).GetStatus(context.Background(), provider.StatusInput{ProviderOrderRef: "oABC"})
		if err != nil || out.Status != tc.want {
			t.Errorf("%d %s: out=%+v err=%v", tc.st, tc.body, out, err)
		}
	}
}

func TestRegisterMethod(t *testing.T) {
	account := sk.NewID[sk.CustomerAccountTag]()
	s := newServer(t, func(cl call) (int, string) {
		switch cl.Path {
		case "/v1/customers":
			return 400, errBody("E0001003014") // 登録済みの顧客
		case "/v1/customers/" + fincode.CustomerID(account.UUID()) + "/cards":
			if cl.Body["token"] == "used" {
				return 400, errBody("E0006102A01")
			}
			return 200, `{"id":"cs_card1","customer_id":"c_x","default_flag":"1","card_no":"************4242","expire":"2812","brand":"VISA"}`
		}
		return 404, "{}"
	})
	a := s.adapter(t)
	rm, err := a.RegisterMethod(context.Background(), provider.RegisterInput{CustomerAccountID: account, Token: "tok", IdempotencyKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	want := domain.CardDisplay{Brand: "visa", Last4: "4242", ExpMonth: 12, ExpYear: 2028}
	if rm.Display != want || rm.Ref.CustomerRef != fincode.CustomerID(account.UUID()) || rm.Ref.MethodRef != "cs_card1" || rm.Ref.Provider != "fincode" {
		t.Fatalf("rm = %+v", rm)
	}
	if s.calls[0].Body["id"] != fincode.CustomerID(account.UUID()) || s.calls[1].Body["default_flag"] != "1" {
		t.Fatalf("calls = %+v", s.calls)
	}
	if strings.Contains(s.calls[1].Header.Get("Tenant-Shop-Id"), "s_") {
		t.Fatal("共有の顧客なのにテナントショップを指定した")
	}
	if _, err := a.RegisterMethod(context.Background(), provider.RegisterInput{CustomerAccountID: account, Token: "used", IdempotencyKey: "k2"}); !errors.Is(err, c.ErrInvalidToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseWebhook(t *testing.T) {
	s := newServer(t, func(call) (int, string) { return 200, "{}" })
	a := s.adapter(t)
	body := []byte(`{"shop_id":"s_tenant0001","order_id":"oABC","access_id":"a_1","pay_type":"Card","status":"CAPTURED","amount":"980","event":"payments.card.capture","transaction_date":"2026/10/25 10:00:00.123"}`)
	h := http.Header{}
	if _, err := a.ParseWebhook(context.Background(), h, body); !errors.Is(err, provider.ErrInvalidSignature) {
		t.Fatalf("署名なし: %v", err)
	}
	h.Set("Fincode-Signature", "wrong")
	if _, err := a.ParseWebhook(context.Background(), h, body); !errors.Is(err, provider.ErrInvalidSignature) {
		t.Fatalf("署名違い: %v", err)
	}
	h.Set("Fincode-Signature", "sig-123")
	evs, err := a.ParseWebhook(context.Background(), h, body)
	if err != nil || len(evs) != 1 {
		t.Fatalf("evs=%v err=%v", evs, err)
	}
	e := evs[0]
	if e.ProviderOrderRef != "oABC" || e.Outcome.Status != provider.OutcomeSucceeded || e.ProviderEventID != "payments.card.capture:oABC:CAPTURED:" ||
		!e.OccurredAt.Equal(time.Date(2026, 10, 25, 1, 0, 0, 123e6, time.UTC)) {
		t.Fatalf("event = %+v", e)
	}
	again, _ := a.ParseWebhook(context.Background(), h, body)
	if again[0].ProviderEventID != e.ProviderEventID {
		t.Fatal("再送で同じ ID にならない")
	}
	regist := []byte(`{"order_id":"oABC","status":"UNPROCESSED","event":"payments.card.regist"}`)
	if evs, err := a.ParseWebhook(context.Background(), h, regist); err != nil || len(evs) != 0 {
		t.Fatalf("登録の通知は捨てる: %v %v", evs, err)
	}
	if string(a.WebhookAck()) != `{"receive":"0"}` {
		t.Fatal(string(a.WebhookAck()))
	}
}

func TestPerTenantContractUnsupported(t *testing.T) {
	if _, err := fincode.New(fincode.Config{APIBaseURL: "https://api.test.fincode.jp", SecretKey: "x", SharedCustomer: false}); !errors.Is(err, fincode.ErrPerTenantUnsupported) {
		t.Fatal(err)
	}
}
