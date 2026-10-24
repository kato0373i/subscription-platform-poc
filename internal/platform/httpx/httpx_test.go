package httpx_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"subscription-platform/internal/platform/auditctx"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/httpx/errcodes"
	sk "subscription-platform/internal/sharedkernel"
)

func devKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func issue(t *testing.T, k *ecdsa.PrivateKey, pool httpx.UserPool, mod func(r *httpx.DevTokenRequest)) string {
	t.Helper()
	r := httpx.DevTokenRequest{Pool: pool, Sub: "user-1", Email: "u@example.com"}
	if mod != nil {
		mod(&r)
	}
	tok, _, err := httpx.IssueDevToken(k, r, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func signRaw(t *testing.T, build func(*jwt.Builder) *jwt.Builder, alg jwa.SignatureAlgorithm, key any) string {
	t.Helper()
	set := httpx.DevPoolSettings(httpx.PoolConsumer)
	b := jwt.NewBuilder().Issuer(set.Issuer).Audience([]string{set.Audience}).Subject("s").Expiration(time.Now().Add(time.Hour))
	tok, err := build(b).Build()
	if err != nil {
		t.Fatal(err)
	}
	s, err := jwt.Sign(tok, jwt.WithKey(alg, key))
	if err != nil {
		t.Fatal(err)
	}
	return string(s)
}

// AC-PLT-026
func TestVerifierRejectsBadTokens(t *testing.T) {
	k := devKey(t)
	v := httpx.NewDevJWTVerifier(&k.PublicKey)
	ctx := context.Background()
	if c, err := v.Verify(ctx, issue(t, k, httpx.PoolConsumer, nil), httpx.PoolConsumer); err != nil || c.Subject != "user-1" || c.EmailVerified == nil || !*c.EmailVerified {
		t.Fatalf("正しいトークン: %+v %v", c, err)
	}
	other := devKey(t)
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	none := func() string {
		h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
		p := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"` + httpx.DevIssuer + `","aud":"sp-dev-consumer","sub":"s","exp":9999999999}`))
		return h + "." + p + "."
	}()
	bad := map[string]string{
		"期限切れ":       signRaw(t, func(b *jwt.Builder) *jwt.Builder { return b.Expiration(time.Now().Add(-2 * time.Minute)) }, jwa.ES256(), k),
		"iss 違い":     signRaw(t, func(b *jwt.Builder) *jwt.Builder { return b.Issuer("https://evil") }, jwa.ES256(), k),
		"aud 違い":     issue(t, k, httpx.PoolStaff, nil),
		"alg none":   none,
		"HS256":      signRaw(t, func(b *jwt.Builder) *jwt.Builder { return b }, jwa.HS256(), []byte("secret-secret-secret-secret-1234")),
		"署名の鍵違い":     issue(t, other, httpx.PoolConsumer, nil),
		"RS256 未登録鍵": signRaw(t, func(b *jwt.Builder) *jwt.Builder { return b }, jwa.RS256(), rsaKey),
		"JWT でない":    "abc",
	}
	for name, tok := range bad {
		if _, err := v.Verify(ctx, tok, httpx.PoolConsumer); !errors.Is(err, httpx.ErrInvalidToken) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// AC-PLT-034
func TestWriteProblemHidesInternalError(t *testing.T) {
	r := httpx.NewRouter(httpx.RouterConfig{}, nil, httpx.Ports{}, nil)
	_ = r
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	httpx.WriteProblem(rec, req, errors.New("pq: password authentication failed for user sp_app"))
	var p map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != 500 || rec.Header().Get("Content-Type") != "application/problem+json" || p["code"] != "internal_error" {
		t.Fatalf("status=%d ct=%s body=%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Fatal("内部のメッセージが漏れた")
	}
	if _, ok := p["request_id"]; !ok {
		t.Fatal("request_id がない")
	}
}

// AC-PLT-036
func TestParsePage(t *testing.T) {
	codec, err := httpx.NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	good := codec.Encode(map[string]string{"id": "x"})
	tampered := good[:len(good)-2] + "AA"
	for _, q := range []string{"limit=101", "limit=0", "limit=abc", "starting_after=" + tampered} {
		_, err := httpx.ParsePage(httptest.NewRequest(http.MethodGet, "/x?"+q, nil), codec)
		var p *httpx.Problem
		if !errors.As(err, &p) || p.Code != errcodes.ValidationFailed {
			t.Errorf("%s: err = %v", q, err)
		}
	}
	pr, err := httpx.ParsePage(httptest.NewRequest(http.MethodGet, "/x?starting_after="+good, nil), codec)
	if err != nil || pr.Limit != 20 || string(pr.After) != `{"id":"x"}` {
		t.Fatalf("pr = %+v err = %v", pr, err)
	}
}

const specTmpl = `openapi: 3.0.3
info: {title: t, version: "1"}
paths:
  /v1/console/plans:
    post:
      operationId: createPlan
      %s
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties: {name: {type: string}}
      responses: {"201": {description: ok}}
  /v1/me/things/{id}:
    get:
      operationId: getThing
      x-sp-isolation: tested
      parameters: [{name: id, in: path, required: true, schema: {type: string, format: uuid}}]
      responses: {"200": {description: ok}}
`

func loadSpec(t *testing.T, ext string) *openapi3.T {
	t.Helper()
	s, err := openapi3.NewLoader().LoadFromData([]byte(strings.Replace(specTmpl, "%s", ext, 1)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// AC-PLT-028
func TestMountRequiresExtensions(t *testing.T) {
	for name, ext := range map[string]string{
		"permission がない": "x-sp-isolation: tested",
		"isolation がない":  "x-sp-permission: catalog.write",
		"isolation が不正":  "x-sp-permission: catalog.write\n      x-sp-isolation: maybe",
	} {
		r := httpx.NewRouter(httpx.RouterConfig{}, nil, httpx.Ports{}, nil)
		if err := r.Mount(loadSpec(t, ext), func(httpx.ServeMux) {}); err == nil {
			t.Errorf("%s: 起動時エラーにならなかった", name)
		}
	}
	r := httpx.NewRouter(httpx.RouterConfig{}, nil, httpx.Ports{}, nil)
	err := r.Mount(loadSpec(t, "x-sp-permission: catalog.write\n      x-sp-isolation: tested"), func(m httpx.ServeMux) {
		m.HandleFunc("GET /v1/me/undeclared", func(http.ResponseWriter, *http.Request) {})
	})
	if err == nil {
		t.Fatal("spec にないルートを登録できた")
	}
}

type memAudit struct{ recs []auditctx.Record }

func (m *memAudit) Write(_ context.Context, r []auditctx.Record) error {
	m.recs = append(m.recs, r...)
	return nil
}

// ルーターの認証・認可・監査・入力検証の流れ（テスト用のルートで確かめる）
func TestRouterAuthFlow(t *testing.T) {
	k := devKey(t)
	tenant := sk.NewID[sk.TenantTag]()
	customer := sk.NewID[sk.CustomerAccountTag]()
	audit := &memAudit{}
	ports := httpx.Ports{
		ResolveConsumer: func(context.Context, httpx.Claims) (sk.CustomerAccountID, error) { return customer, nil },
		GetStaffRole: func(_ context.Context, tid sk.TenantID, sub string) (string, error) {
			if tid == tenant && sub == "admin" {
				return "admin", nil
			}
			if tid == tenant && sub == "viewer" {
				return "viewer", nil
			}
			return "", nil
		},
		Allows:            func(role, perm string) bool { return role == "admin" || perm == "console.read" },
		IsWritePermission: func(perm string) bool { return perm != "console.read" },
		TenantWritable:    func(context.Context, sk.TenantID) (bool, error) { return true, nil },
		AuditWriter:       audit,
	}
	r := httpx.NewRouter(httpx.RouterConfig{}, httpx.NewDevJWTVerifier(&k.PublicKey), ports, nil)
	err := r.Mount(loadSpec(t, "x-sp-permission: catalog.write\n      x-sp-isolation: tested"), func(m httpx.ServeMux) {
		m.HandleFunc("POST /v1/console/plans", func(w http.ResponseWriter, req *http.Request) {
			auditctx.Annotate(req.Context(), "plan.create", auditctx.Target{Type: "plan", ID: "p1"}, nil, map[string]any{"name": "x", "amount": 1000})
			httpx.WriteJSON(w, 201, map[string]string{"id": "p1"})
		})
		m.HandleFunc("GET /v1/me/things/{id}", func(w http.ResponseWriter, req *http.Request) {
			httpx.WriteJSON(w, 200, map[string]string{"customer": httpx.ConsumerID(req.Context()).String()})
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	h := r.Handler()
	do := func(method, path, tok, tenantHdr, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		if tenantHdr != "" {
			req.Header.Set("X-Tenant-Id", tenantHdr)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Correlation-Id", "e2e-x-abc123")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	consumerTok := issue(t, k, httpx.PoolConsumer, nil)
	staff := func(sub string) string {
		return issue(t, k, httpx.PoolStaff, func(r *httpx.DevTokenRequest) { r.Sub = sub })
	}
	thing := "/v1/me/things/" + sk.NewID[sk.TenantTag]().String()
	cases := []struct {
		name              string
		method, path, tok string
		tenant, body      string
		status            int
		code              string
	}{
		{"consumer 正しい主体", "GET", thing, consumerTok, "", "", 200, ""},
		{"consumer トークンなし", "GET", thing, "", "", "", 401, "unauthenticated"},
		{"consumer に staff のトークン", "GET", thing, staff("admin"), "", "", 403, "forbidden"},
		{"不正な path パラメータ", "GET", "/v1/me/things/not-uuid", consumerTok, "", "", 400, "validation_failed"},
		{"console admin", "POST", "/v1/console/plans", staff("admin"), tenant.String(), `{"name":"x"}`, 201, ""},
		{"console X-Tenant-Id なし", "POST", "/v1/console/plans", staff("admin"), "", `{"name":"x"}`, 400, "validation_failed"},
		{"console 所属していない", "POST", "/v1/console/plans", staff("stranger"), tenant.String(), `{"name":"x"}`, 403, "forbidden"},
		{"console viewer の書き込み", "POST", "/v1/console/plans", staff("viewer"), tenant.String(), `{"name":"x"}`, 403, "forbidden"},
		{"console に consumer のトークン", "POST", "/v1/console/plans", consumerTok, tenant.String(), `{"name":"x"}`, 403, "forbidden"},
		{"console 本文が spec に合わない", "POST", "/v1/console/plans", staff("admin"), tenant.String(), `{}`, 400, "validation_failed"},
		{"未定義のルート", "GET", "/v1/me/nothing", consumerTok, "", "", 404, "not_found"},
		{"405", "DELETE", thing, consumerTok, "", "", 405, "method_not_allowed"},
	}
	for _, c := range cases {
		rec := do(c.method, c.path, c.tok, c.tenant, c.body)
		if rec.Code != c.status {
			t.Errorf("%s: status = %d body = %s", c.name, rec.Code, rec.Body)
			continue
		}
		if rec.Header().Get("X-Correlation-Id") != "e2e-x-abc123" || rec.Header().Get("X-Request-Id") == "" {
			t.Errorf("%s: 応答ヘッダー = %v", c.name, rec.Header())
		}
		if c.code != "" {
			var p map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &p)
			if p["code"] != c.code || p["correlation_id"] != "e2e-x-abc123" || p["request_id"] != rec.Header().Get("X-Request-Id") {
				t.Errorf("%s: problem = %v", c.name, p)
			}
		}
	}
	// 監査: 201 の注釈 1 件・viewer の 403 が denied で 1 件・stranger の 403 も denied。400・401 は記録しない
	var succeeded, denied int
	for _, rec := range audit.recs {
		switch rec.Outcome {
		case "succeeded":
			succeeded++
			if rec.Action != "plan.create" || rec.ActorRole != "admin" || rec.Route != "POST /v1/console/plans" || rec.After["name"] != "***" {
				t.Errorf("succeeded record = %+v", rec)
			}
		case "denied":
			denied++
		}
	}
	if succeeded != 1 || denied != 2 {
		t.Fatalf("audit = succeeded %d, denied %d: %+v", succeeded, denied, audit.recs)
	}
}

// AC-PLT-081（長すぎる相関 ID は捨てられる）
func TestLongCorrelationIDIsReplaced(t *testing.T) {
	r := httpx.NewRouter(httpx.RouterConfig{}, nil, httpx.Ports{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/nothing", nil)
	req.Header.Set("X-Correlation-Id", strings.Repeat("a", 200))
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, req)
	got := rec.Header().Get("X-Correlation-Id")
	if len(got) != 36 || rec.Code != 404 {
		t.Fatalf("correlation = %q status = %d", got, rec.Code)
	}
}
