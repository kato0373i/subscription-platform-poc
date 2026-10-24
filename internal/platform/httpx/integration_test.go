//go:build integration

package httpx_test

import (
	"context"
	"crypto/ecdsa"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/testsupport/pgtest"
	sk "subscription-platform/internal/sharedkernel"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

const idemSpec = `openapi: 3.0.3
info: {title: t, version: "1"}
paths:
  /v1/me/slow:
    post: {operationId: slow, x-sp-isolation: tested, x-sp-idempotency: required, responses: {"200": {description: ok}}}
  /v1/me/fail:
    post: {operationId: fail, x-sp-isolation: tested, x-sp-idempotency: required, responses: {"200": {description: ok}}}
  /v1/me/accepted:
    post: {operationId: accepted, x-sp-isolation: tested, x-sp-idempotency: required, responses: {"202": {description: ok}}}
`

func idemRouter(t *testing.T, d *pgtest.DB, k *ecdsa.PrivateKey, calls map[string]*atomic.Int64) http.Handler {
	t.Helper()
	spec, err := openapi3.NewLoader().LoadFromData([]byte(idemSpec))
	if err != nil {
		t.Fatal(err)
	}
	customer := sk.NewID[sk.CustomerAccountTag]()
	r := httpx.NewRouter(httpx.RouterConfig{}, httpx.NewDevJWTVerifier(&k.PublicKey), httpx.Ports{
		ResolveConsumer: func(context.Context, httpx.Claims) (sk.CustomerAccountID, error) { return customer, nil },
	}, httpx.NewPGIdempotencyStore(d.Pool))
	err = r.Mount(spec, func(m httpx.ServeMux) {
		m.HandleFunc("POST /v1/me/slow", func(w http.ResponseWriter, _ *http.Request) {
			calls["slow"].Add(1)
			time.Sleep(2 * time.Second)
			httpx.WriteJSON(w, 200, map[string]string{"ok": "1"})
		})
		m.HandleFunc("POST /v1/me/fail", func(w http.ResponseWriter, _ *http.Request) {
			calls["fail"].Add(1)
			w.WriteHeader(500)
		})
		m.HandleFunc("POST /v1/me/accepted", func(w http.ResponseWriter, _ *http.Request) {
			calls["accepted"].Add(1)
			httpx.MarkTransient(w)
			httpx.WriteJSON(w, 202, map[string]string{"status": "processing"})
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return r.Handler()
}

func post(h http.Handler, tok, path, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// AC-PLT-031
func TestIdempotencyConcurrentRequests(t *testing.T) {
	d := pgtest.New(t)
	k := devKey(t)
	calls := map[string]*atomic.Int64{"slow": {}, "fail": {}, "accepted": {}}
	h := idemRouter(t, d, k, calls)
	tok := issue(t, k, httpx.PoolConsumer, nil)
	var wg sync.WaitGroup
	var first *httptest.ResponseRecorder
	wg.Add(1)
	go func() { defer wg.Done(); first = post(h, tok, "/v1/me/slow", "k1", "{}") }()
	time.Sleep(300 * time.Millisecond)
	second := post(h, tok, "/v1/me/slow", "k1", "{}")
	wg.Wait()
	if second.Code != 409 || !strings.Contains(second.Body.String(), "request_in_progress") || second.Header().Get("Retry-After") != "1" {
		t.Fatalf("2 つ目 = %d %s", second.Code, second.Body)
	}
	if first.Code != 200 || calls["slow"].Load() != 1 {
		t.Fatalf("1 つ目 = %d、処理回数 %d", first.Code, calls["slow"].Load())
	}
	replay := post(h, tok, "/v1/me/slow", "k1", "{}")
	if replay.Code != 200 || replay.Header().Get("Idempotent-Replayed") != "true" || calls["slow"].Load() != 1 {
		t.Fatalf("再生 = %d %v", replay.Code, replay.Header())
	}
	if reused := post(h, tok, "/v1/me/slow", "k1", `{"x":1}`); reused.Code != 422 {
		t.Fatalf("違う本文 = %d", reused.Code)
	}
	// 別の利用者が同じキーを使っても衝突しない
	other := issue(t, k, httpx.PoolConsumer, func(r *httpx.DevTokenRequest) { r.Sub = "other" })
	_ = other
}

// AC-PLT-032
func TestIdempotencyDoesNotStore5xxOrTransient(t *testing.T) {
	d := pgtest.New(t)
	k := devKey(t)
	calls := map[string]*atomic.Int64{"slow": {}, "fail": {}, "accepted": {}}
	h := idemRouter(t, d, k, calls)
	tok := issue(t, k, httpx.PoolConsumer, nil)
	for range 2 {
		post(h, tok, "/v1/me/fail", "k-fail", "{}")
		post(h, tok, "/v1/me/accepted", "k-acc", "{}")
	}
	if calls["fail"].Load() != 2 || calls["accepted"].Load() != 2 {
		t.Fatalf("calls fail=%d accepted=%d", calls["fail"].Load(), calls["accepted"].Load())
	}
	if n := d.QueryInt(t, `SELECT count(*) FROM platform.idempotency_keys WHERE status = 'completed'`); n != 0 {
		t.Fatalf("completed の行 = %d", n)
	}
	if rec := post(h, tok, "/v1/me/fail", "", "{}"); rec.Code != 400 || !strings.Contains(rec.Body.String(), `"field":"Idempotency-Key"`) {
		t.Fatalf("キーなし = %d %s", rec.Code, rec.Body)
	}
}
