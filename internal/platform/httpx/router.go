package httpx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"

	"subscription-platform/internal/platform/auditctx"
	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/httpx/errcodes"
	"subscription-platform/internal/platform/observability"
	sk "subscription-platform/internal/sharedkernel"
)

// AuditWriter は監査の書き込み。実装は modules/audit で、cmd/internal/wiring が注入する。
type AuditWriter interface {
	Write(ctx context.Context, recs []auditctx.Record) error
}

// Ports は cmd/internal/wiring が modules の contracts を包んで注入する関数（platform は modules を import しない）。
type Ports struct {
	ResolveConsumer    func(ctx context.Context, c Claims) (sk.CustomerAccountID, error)
	AuthenticateAPIKey func(ctx context.Context, raw string) (sk.TenantID, error)
	// GetStaffRole は所属していなければ ("", nil) を返す。error は基盤の障害として 500 にする。
	GetStaffRole      func(ctx context.Context, t sk.TenantID, sub string) (string, error)
	Allows            func(role, permission string) bool
	IsWritePermission func(permission string) bool
	TenantWritable    func(ctx context.Context, t sk.TenantID) (bool, error)
	MapError          func(err error) *Problem
	AuditWriter       AuditWriter
}

// RouterConfig はルーターの設定。
type RouterConfig struct {
	Clock       sk.Clock
	Pool        *db.Pool                          // /readyz の DB の確認
	Middlewares []func(http.Handler) http.Handler // レート制限などの差し込み口（§10）
	BodyLimit   int64                             // 既定 1MB
}

// Audience は入口（パスの先頭）ごとの主体。
type Audience string

const (
	AudienceMe       Audience = "me"
	AudienceConsole  Audience = "console"
	AudienceTenant   Audience = "tenant"
	AudienceOps      Audience = "ops"
	AudienceCheckout Audience = "checkout"
	AudienceWebhooks Audience = "webhooks"
	AudienceDev      Audience = "dev"
	AudienceSystem   Audience = "system" // /healthz・/readyz
)

// AudienceOf はパスから入口を決める。
func AudienceOf(path string) (Audience, bool) {
	switch {
	case strings.HasPrefix(path, "/v1/me"):
		return AudienceMe, true
	case strings.HasPrefix(path, "/v1/console/"):
		return AudienceConsole, true
	case strings.HasPrefix(path, "/v1/tenant/"):
		return AudienceTenant, true
	case strings.HasPrefix(path, "/v1/ops/"):
		return AudienceOps, true
	case strings.HasPrefix(path, "/v1/checkout/"):
		return AudienceCheckout, true
	case strings.HasPrefix(path, "/webhooks/"):
		return AudienceWebhooks, true
	case strings.HasPrefix(path, "/v1/dev/"):
		return AudienceDev, true
	}
	return "", false
}

func (a Audience) principalKind() (PrincipalKind, bool) {
	switch a {
	case AudienceMe:
		return KindConsumer, true
	case AudienceConsole:
		return KindStaff, true
	case AudienceTenant:
		return KindTenantAPI, true
	case AudienceOps:
		return KindOperator, true
	default:
		return "", false
	}
}

func (a Audience) audited() bool {
	return a == AudienceConsole || a == AudienceOps || a == AudienceTenant
}

// routePolicy は OpenAPI の拡張属性から読んだルートの方針。
type routePolicy struct {
	pattern     string
	audience    Audience
	operationID string
	permission  string
	idempotency bool
	isolation   string
	validator   routers.Router
}

// Router はルーティングと共通の処理。
type Router struct {
	cfg      RouterConfig
	verifier TokenVerifier
	ports    Ports
	idem     IdempotencyStore
	mux      *http.ServeMux
	policies map[string]*routePolicy
	stopping atomic.Bool
}

// NewRouter はルーターを作る。/healthz と /readyz を登録する。
func NewRouter(cfg RouterConfig, v TokenVerifier, p Ports, idem IdempotencyStore) *Router {
	if cfg.BodyLimit <= 0 {
		cfg.BodyLimit = 1 << 20
	}
	if cfg.Clock == nil {
		cfg.Clock = sk.SystemClock{}
	}
	r := &Router{cfg: cfg, verifier: v, ports: p, idem: idem, mux: http.NewServeMux(), policies: map[string]*routePolicy{}}
	r.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.mux.HandleFunc("GET /readyz", r.readyz)
	return r
}

func (r *Router) readyz(w http.ResponseWriter, req *http.Request) {
	if r.stopping.Load() {
		WriteProblem(w, req, Error(errcodes.ServiceUnavailable, "shutting down"))
		return
	}
	if r.cfg.Pool != nil {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		if err := r.cfg.Pool.Ping(ctx); err != nil {
			WriteProblem(w, req, Error(errcodes.ServiceUnavailable, "database unreachable"))
			return
		}
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// SetShuttingDown は /readyz を 503 にする（SIGTERM を受けたとき）。
func (r *Router) SetShuttingDown() { r.stopping.Store(true) }

// ServeMux は oapi-codegen の HandlerFromMux に渡す登録口。
type ServeMux interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

type mountMux struct {
	r    *Router
	errs []error
}

func (m *mountMux) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	pol, ok := m.r.policies[normalizePattern(pattern)]
	if !ok {
		m.errs = append(m.errs, fmt.Errorf("httpx: route %q is not declared in the OpenAPI spec", pattern))
		return
	}
	m.r.mux.Handle(pattern, m.r.wrap(pol, http.HandlerFunc(h)))
}

func normalizePattern(p string) string {
	method, path, ok := strings.Cut(strings.TrimSpace(p), " ")
	if !ok {
		return p
	}
	return strings.ToUpper(method) + " " + strings.TrimSpace(path)
}

// ParsePolicies は spec の全操作の拡張属性を読み、規則を検査する（AC-PLT-028）。
func ParsePolicies(spec *openapi3.T) ([]*routePolicy, error) {
	var out []*routePolicy
	var errs []error
	for path, item := range spec.Paths.Map() {
		aud, ok := AudienceOf(path)
		if !ok {
			errs = append(errs, fmt.Errorf("%s: unknown entry point", path))
			continue
		}
		for method, op := range item.Operations() {
			pol := &routePolicy{pattern: method + " " + path, audience: aud, operationID: op.OperationID}
			pol.permission = extString(op, "x-sp-permission")
			pol.idempotency = extString(op, "x-sp-idempotency") == "required"
			pol.isolation = extString(op, "x-sp-isolation")
			where := fmt.Sprintf("%s %s (%s)", method, path, op.OperationID)
			if aud == AudienceConsole && pol.permission == "" {
				errs = append(errs, fmt.Errorf("%s: x-sp-permission is required for console", where))
			}
			if aud == AudienceMe || aud == AudienceConsole || aud == AudienceTenant {
				if pol.isolation != "tested" && !strings.HasPrefix(pol.isolation, "not_applicable:") {
					errs = append(errs, fmt.Errorf("%s: x-sp-isolation must be tested or not_applicable:<reason>", where))
				}
			}
			if v := extString(op, "x-sp-idempotency"); v != "" && v != "required" {
				errs = append(errs, fmt.Errorf("%s: x-sp-idempotency must be required", where))
			}
			out = append(out, pol)
		}
	}
	return out, errors.Join(errs...)
}

func extString(op *openapi3.Operation, key string) string {
	v, ok := op.Extensions[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// Mount は spec の方針を読み込み、register にハンドラを登録させる。拡張属性の検査もここで行い、欠けていれば起動時エラー。
func (r *Router) Mount(spec *openapi3.T, register func(mux ServeMux)) error {
	pols, err := ParsePolicies(spec)
	if err != nil {
		return err
	}
	spec.Servers = nil
	validator, err := legacy.NewRouter(spec)
	if err != nil {
		return fmt.Errorf("httpx: openapi router: %w", err)
	}
	for _, p := range pols {
		p.validator = validator
		r.policies[p.pattern] = p
	}
	m := &mountMux{r: r}
	register(m)
	return errors.Join(m.errs...)
}

// HandleUnauthenticated は spec を持たない基盤のルート（/webhooks の署名検証はモジュール）を登録する。
func (r *Router) HandleUnauthenticated(pattern string, h http.Handler) {
	path := strings.TrimSpace(pattern[strings.Index(pattern, " ")+1:])
	aud, _ := AudienceOf(path)
	r.mux.Handle(pattern, r.wrap(&routePolicy{pattern: pattern, audience: aud}, h))
}

// Handler は外側の共通処理（request_id・相関 ID・ログ・panic の回収・本文の上限）を含めた http.Handler。
func (r *Router) Handler() http.Handler {
	var h http.Handler = http.HandlerFunc(r.serve)
	for i := len(r.cfg.Middlewares) - 1; i >= 0; i-- {
		h = r.cfg.Middlewares[i](h)
	}
	return r.outer(h)
}

// serve は ServeMux に渡す。未定義のルート・405 は Problem に変換する。
func (r *Router) serve(w http.ResponseWriter, req *http.Request) {
	h, pattern := r.mux.Handler(req)
	if pattern != "" {
		if ri := reqInfoFrom(req.Context()); ri != nil && ri.route == "" {
			ri.route = pattern
		}
		r.mux.ServeHTTP(w, req) // パスパラメータ（r.PathValue）は ServeHTTP を通したときだけ設定される
		return
	}
	probe := newBufferWriter()
	h.ServeHTTP(probe, req)
	if probe.code() == http.StatusMethodNotAllowed {
		w.Header().Set("Allow", probe.header.Get("Allow"))
		WriteProblem(w, req, Error(errcodes.MethodNotAllowed, ""))
		return
	}
	WriteProblem(w, req, Error(errcodes.NotFound, ""))
}

type reqInfo struct {
	route    string
	tenantID string
	actor    *sk.Actor
	errCode  string
}

type reqInfoKey struct{}

func reqInfoFrom(ctx context.Context) *reqInfo {
	ri, _ := ctx.Value(reqInfoKey{}).(*reqInfo)
	return ri
}

func (r *Router) outer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		reqID := observability.NewCorrelationID()
		corr := req.Header.Get("X-Correlation-Id")
		if !observability.ValidCorrelationID(corr) {
			corr = observability.NewCorrelationID() // E27: 形式に合わない値は捨てる
		}
		w.Header().Set("X-Request-Id", reqID)
		w.Header().Set("X-Correlation-Id", corr)
		ctx := context.WithValue(req.Context(), requestIDKey{}, reqID)
		ctx = observability.WithCorrelation(ctx, corr, &reqID)
		ctx = observability.WithFields(ctx, slog.String("request_id", reqID))
		ctx = withErrorMapper(ctx, r.ports.MapError)
		ri := &reqInfo{}
		ctx = context.WithValue(ctx, reqInfoKey{}, ri)

		logBody := observability.Current().HTTPBodyEnabled()
		rec := &responseRecorder{ResponseWriter: w}
		if logBody {
			rec.body = &bytes.Buffer{}
		}
		var reqBody []byte
		bytesIn := 0
		if req.Body != nil && req.Body != http.NoBody {
			b, err := io.ReadAll(io.LimitReader(req.Body, r.cfg.BodyLimit+1))
			_ = req.Body.Close()
			if err == nil && int64(len(b)) > r.cfg.BodyLimit {
				req = req.WithContext(ctx)
				WriteProblem(rec, req, Error(errcodes.ValidationFailed, "request body too large", FieldError{Field: "body", Code: "too_large"}))
				r.logRequest(ctx, req, rec, ri, start, len(b), nil)
				return
			}
			reqBody = b
			bytesIn = len(b)
			req.Body = io.NopCloser(bytes.NewReader(b))
		}
		req = req.WithContext(ctx)
		func() {
			var perr error
			defer func() {
				if perr != nil {
					if rec.status == 0 {
						WriteProblem(rec, req, perr)
					}
				}
			}()
			defer observability.Recover(ctx, "http", &perr)
			next.ServeHTTP(rec, req)
		}()
		var body []byte
		if logBody {
			body = reqBody
		}
		r.logRequest(ctx, req, rec, ri, start, bytesIn, body)
	})
}

func (r *Router) logRequest(ctx context.Context, req *http.Request, rec *responseRecorder, ri *reqInfo, start time.Time, bytesIn int, reqBody []byte) {
	status := rec.status
	if status == 0 {
		status = http.StatusOK
	}
	route := ri.route
	if route == "" {
		route = req.Pattern
	}
	if route == "" {
		route = "unmatched"
	}
	attrs := []slog.Attr{
		slog.String("method", req.Method),
		slog.String("route", route),
		slog.Int("status", status),
		slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		slog.Int("bytes_in", bytesIn),
		slog.Int("bytes_out", rec.bytes),
	}
	if ri.tenantID != "" {
		attrs = append(attrs, slog.String("tenant_id", ri.tenantID))
	}
	if ri.actor != nil {
		attrs = append(attrs, slog.Group("actor", slog.String("type", ri.actor.Type), slog.String("id", ri.actor.ID)))
	}
	code := ri.errCode
	if code == "" {
		code = rec.errCode
	}
	if code != "" {
		attrs = append(attrs, slog.Group("error", slog.String("code", code)))
	}
	if reqBody != nil {
		attrs = append(attrs, slog.Any("request_body", observability.RedactJSONBody(reqBody)))
	}
	if rec.body != nil && rec.body.Len() > 0 {
		attrs = append(attrs, slog.Any("response_body", observability.RedactJSONBody(rec.body.Bytes())))
	}
	level := slog.LevelInfo
	switch {
	case status >= 500:
		level = slog.LevelError
	case route == "GET /healthz" || route == "GET /readyz":
		level = slog.LevelDebug // ヘルスチェックは数秒ごとに来るので debug にする
	}
	observability.Logger("platform.http").LogAttrs(ctx, level, "http.request.completed", attrs...)
}

// wrap はルートごとの処理（認証 → 監査 → 認可 → 入力検証 → Idempotency → ハンドラ）を組み立てる。
func (r *Router) wrap(pol *routePolicy, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ri := reqInfoFrom(req.Context())
		if ri != nil {
			ri.route = pol.pattern
		}
		kind, needsAuth := pol.audience.principalKind()
		if !needsAuth {
			r.afterAuth(pol, h, w, req)
			return
		}
		ctx, p, err := r.authenticate(req, kind)
		if err != nil {
			WriteProblem(w, req, err)
			return
		}
		ctx = r.annotateContext(ctx, p)
		req = req.WithContext(ctx)
		if pol.audience.audited() && isMutation(req.Method) {
			r.audited(pol, p, h, w, req)
			return
		}
		if err := r.authorize(req, pol, &p); err != nil {
			WriteProblem(w, req, err)
			return
		}
		r.afterAuth(pol, h, w, req.WithContext(r.scoped(req.Context(), p)))
	})
}

func isMutation(m string) bool {
	return m == http.MethodPost || m == http.MethodPut || m == http.MethodPatch || m == http.MethodDelete
}

// afterAuth は入力検証 → Idempotency → ハンドラ。
func (r *Router) afterAuth(pol *routePolicy, h http.Handler, w http.ResponseWriter, req *http.Request) {
	if pol.validator != nil {
		if err := validateRequest(req, pol.validator); err != nil {
			WriteProblem(w, req, err)
			return
		}
	}
	if pol.idempotency {
		r.idempotent(pol, h, w, req)
		return
	}
	h.ServeHTTP(w, req)
}

func (r *Router) annotateContext(ctx context.Context, p Principal) context.Context {
	actor := sk.Actor{Type: string(p.Kind), ID: p.Subject}
	if p.Kind == KindConsumer && p.CustomerAccountID != nil {
		actor.ID = p.CustomerAccountID.String()
	}
	ctx = withPrincipal(ctx, p)
	ctx = observability.WithActor(ctx, actor)
	if ri := reqInfoFrom(ctx); ri != nil {
		ri.actor = &actor
		if p.TenantID != nil {
			ri.tenantID = p.TenantID.String()
		}
	}
	if p.TenantID != nil {
		ctx = observability.WithFields(ctx, slog.String("tenant_id", p.TenantID.String()))
	}
	return ctx
}

func (r *Router) scoped(ctx context.Context, p Principal) context.Context {
	switch p.Kind {
	case KindConsumer:
		return db.WithCustomerScope(ctx, *p.CustomerAccountID)
	case KindStaff, KindTenantAPI:
		return db.WithTenantScope(ctx, *p.TenantID)
	case KindOperator:
		return db.WithSystemScope(ctx, "operator")
	}
	return ctx
}

func bearer(req *http.Request) string {
	h := req.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func poolOf(k PrincipalKind) UserPool {
	switch k {
	case KindConsumer:
		return PoolConsumer
	case KindStaff:
		return PoolStaff
	default:
		return PoolOperator
	}
}

func kindOf(p UserPool) PrincipalKind {
	switch p {
	case PoolConsumer:
		return KindConsumer
	case PoolStaff:
		return KindStaff
	default:
		return KindOperator
	}
}

// authenticate は資格情報を検証して主体を作る。正しい資格情報でも入口の主体と違えば 403、無効なら 401。
func (r *Router) authenticate(req *http.Request, want PrincipalKind) (context.Context, Principal, error) {
	ctx := req.Context()
	raw := bearer(req)
	log := observability.Logger("platform.http")
	if raw == "" {
		return ctx, Principal{}, Error(errcodes.Unauthenticated, "")
	}
	if strings.HasPrefix(raw, "sk_") {
		if r.ports.AuthenticateAPIKey == nil {
			return ctx, Principal{}, Error(errcodes.Unauthenticated, "")
		}
		t, err := r.ports.AuthenticateAPIKey(ctx, raw)
		if err != nil {
			log.LogAttrs(ctx, slog.LevelInfo, "auth.rejected", slog.String("reason", "api_key"), observability.Err(err))
			return ctx, Principal{}, Error(errcodes.Unauthenticated, "")
		}
		if want != KindTenantAPI {
			return ctx, Principal{}, Error(errcodes.Forbidden, "")
		}
		prefix := raw
		if len(prefix) > 15 {
			prefix = prefix[:15]
		}
		return ctx, Principal{Kind: KindTenantAPI, Subject: prefix, TenantID: &t}, nil
	}
	pools := []UserPool{poolOf(want)}
	for _, p := range Pools {
		if !slices.Contains(pools, p) {
			pools = append(pools, p)
		}
	}
	if want == KindTenantAPI {
		pools = Pools
	}
	var claims Claims
	var got PrincipalKind
	var lastErr error
	for _, p := range pools {
		c, err := r.verifier.Verify(ctx, raw, p)
		if err == nil {
			claims, got = c, kindOf(p)
			break
		}
		lastErr = err
	}
	if got == "" {
		log.LogAttrs(ctx, slog.LevelInfo, "auth.rejected", slog.String("reason", "jwt"), observability.Err(lastErr))
		return ctx, Principal{}, Error(errcodes.Unauthenticated, "")
	}
	if got != want {
		return ctx, Principal{}, Error(errcodes.Forbidden, "")
	}
	p := Principal{Kind: got, Subject: claims.Subject}
	switch got {
	case KindConsumer:
		if r.ports.ResolveConsumer == nil {
			return ctx, Principal{}, Error(errcodes.InternalError, "consumer resolver is not configured")
		}
		id, err := r.ports.ResolveConsumer(ctx, claims)
		if err != nil {
			return ctx, Principal{}, err
		}
		p.CustomerAccountID = &id
	case KindOperator:
		if !slices.Contains(claims.Groups, "operators") {
			return ctx, Principal{}, Error(errcodes.Forbidden, "")
		}
	case KindStaff:
		tid := req.Header.Get("X-Tenant-Id")
		if tid == "" {
			return ctx, Principal{}, Error(errcodes.ValidationFailed, "X-Tenant-Id is required", FieldError{Field: "X-Tenant-Id", Code: "required"})
		}
		t, err := sk.ParseID[sk.TenantTag](tid)
		if err != nil {
			return ctx, Principal{}, Error(errcodes.ValidationFailed, "X-Tenant-Id must be a UUID", FieldError{Field: "X-Tenant-Id", Code: "invalid"})
		}
		p.TenantID = &t
	case KindTenantAPI: // API キーは上で処理済み
	}
	return ctx, p, nil
}

// authorize は staff の所属・権限・書き込みの可否を判定する。
func (r *Router) authorize(req *http.Request, pol *routePolicy, p *Principal) error {
	if p.Kind != KindStaff {
		return nil
	}
	ctx := req.Context()
	if r.ports.GetStaffRole == nil || r.ports.Allows == nil {
		return Error(errcodes.InternalError, "staff authorization is not configured")
	}
	role, err := r.ports.GetStaffRole(ctx, *p.TenantID, p.Subject)
	if err != nil {
		return fmt.Errorf("httpx: get staff role: %w", err)
	}
	if role == "" {
		return Error(errcodes.Forbidden, "")
	}
	p.Role = role
	if !r.ports.Allows(role, pol.permission) {
		return Error(errcodes.Forbidden, "")
	}
	if r.ports.IsWritePermission != nil && r.ports.IsWritePermission(pol.permission) && r.ports.TenantWritable != nil {
		ok, err := r.ports.TenantWritable(ctx, *p.TenantID)
		if err != nil {
			return fmt.Errorf("httpx: tenant writable: %w", err)
		}
		if !ok {
			return Error(errcodes.TenantNotActive, "")
		}
	}
	return nil
}

// audited は監査ミドルウェア：認証の後・認可の前に置き、応答を記録してから AuditWriter に渡し、その後で応答を返す（§7.1）。
func (r *Router) audited(pol *routePolicy, p Principal, h http.Handler, w http.ResponseWriter, req *http.Request) {
	ctx := auditctx.NewBag(req.Context())
	req = req.WithContext(ctx)
	buf := newBufferWriter()
	if err := r.authorize(req, pol, &p); err != nil {
		WriteProblem(buf, req, err)
	} else {
		ctx = withPrincipal(ctx, p)
		r.afterAuth(pol, h, buf, req.WithContext(r.scoped(ctx, p)))
	}
	r.writeAudit(req, pol, p, buf.code())
	buf.flushTo(w)
}

func (r *Router) writeAudit(req *http.Request, pol *routePolicy, p Principal, status int) {
	ctx := req.Context()
	var outcome string
	switch {
	case status >= 200 && status < 300:
		outcome = "succeeded"
	case status == http.StatusForbidden:
		outcome = "denied"
	default:
		return // 401・その他の 4xx・5xx は記録しない（E25）
	}
	corr, _ := observability.CorrelationFrom(ctx)
	base := auditctx.Record{
		OccurredAt: r.cfg.Clock.Now(), TenantID: p.TenantID, ActorType: string(p.Kind), ActorID: p.Subject, ActorRole: p.Role,
		Outcome: outcome, RequestID: requestIDFrom(ctx), CorrelationID: corr, HTTPMethod: req.Method, Route: pol.pattern, SourceIP: clientIP(req),
	}
	defaultAction := "http." + strings.ToLower(req.Method)
	var recs []auditctx.Record
	anns := auditctx.Drain(ctx)
	if outcome == "succeeded" && len(anns) > 0 {
		for _, a := range anns {
			rec := base
			rec.Action, rec.Target, rec.Before, rec.After = a.Action, a.Target, a.Before, a.After
			if a.Tenant != nil {
				rec.TenantID = a.Tenant
			}
			recs = append(recs, rec)
		}
	} else {
		rec := base
		rec.Action = defaultAction
		recs = append(recs, rec)
	}
	if r.ports.AuditWriter == nil {
		return
	}
	if err := r.ports.AuditWriter.Write(context.WithoutCancel(ctx), recs); err != nil {
		// E24: 業務はコミット済み。応答はそのまま返し、監査の内容を error ログに残す
		observability.Logger("platform.audit").LogAttrs(ctx, slog.LevelError, "audit.write_failed", slog.Any("records", recs), observability.Err(err))
	}
}

func clientIP(req *http.Request) string {
	if f := req.Header.Get("X-Forwarded-For"); f != "" {
		ip, _, _ := strings.Cut(f, ",")
		return strings.TrimSpace(ip)
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return req.RemoteAddr
	}
	return host
}

func init() {
	// UUIDv7 を含む RFC 9562 の UUID と、日付を検証する
	openapi3.DefineStringFormatValidator("uuid", openapi3.NewRegexpFormatValidator(openapi3.FormatOfStringForUUIDOfRFC9562))
}

// validateRequest は kin-openapi で要求を検証する。
func validateRequest(req *http.Request, v routers.Router) error {
	route, params, err := v.FindRoute(req)
	if err != nil {
		return nil //nolint:nilerr // spec にないルートは ServeMux 側の責務
	}
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	in := &openapi3filter.RequestValidationInput{
		Request: req, PathParams: params, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}
	verr := openapi3filter.ValidateRequest(req.Context(), in)
	req.Body = io.NopCloser(bytes.NewReader(body))
	if verr == nil {
		return nil
	}
	field := "body"
	var re *openapi3filter.RequestError
	if errors.As(verr, &re) && re.Parameter != nil {
		field = re.Parameter.Name
	}
	return Error(errcodes.ValidationFailed, "request does not match the API specification", FieldError{Field: field, Code: "invalid"})
}

// TestedOperations は x-sp-isolation: tested の operationId（e2elint が @isolation タグと突き合わせる）。
func TestedOperations(pols []*routePolicy) []string {
	var out []string
	for _, p := range pols {
		if p.isolation == "tested" && p.operationID != "" {
			out = append(out, p.operationID)
		}
	}
	return out
}
