// Package http は tenant モジュールの ops・console API（oapi-codegen の strict server を実装する）。
package http

import (
	"context"
	"errors"
	"log/slog"
	nethttp "net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"subscription-platform/api/openapi"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/httpx/errcodes"
	"subscription-platform/internal/platform/observability"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/tenant/contracts"
	"subscription-platform/modules/tenant/internal/adapter/http/gen/console"
	"subscription-platform/modules/tenant/internal/adapter/http/gen/ops"
	"subscription-platform/modules/tenant/internal/adapter/identity"
	"subscription-platform/modules/tenant/internal/adapter/postgres"
	"subscription-platform/modules/tenant/internal/app"
	"subscription-platform/modules/tenant/internal/domain"
)

// Options は HTTP の設定。AllowDirectSubject は ops のスタッフ作成で subject の直接指定を受け付けるか（devtools ビルドだけ true）。
type Options struct {
	AllowDirectSubject bool
}

// Register は ops・console のルートを登録する。
func Register(r *httpx.Router, svc *app.Service, codec *httpx.CursorCodec, opt Options) error {
	opsSpec, err := openapi.Load("ops/tenant.yaml")
	if err != nil {
		return err
	}
	consoleSpec, err := openapi.Load("console/tenant.yaml")
	if err != nil {
		return err
	}
	opsH := ops.NewStrictHandlerWithOptions(&opsHandlers{svc: svc, codec: codec, allowSubject: opt.AllowDirectSubject}, nil,
		ops.StrictHTTPServerOptions{RequestErrorHandlerFunc: httpx.StrictRequestError, ResponseErrorHandlerFunc: httpx.StrictResponseError})
	if err := r.Mount(opsSpec, func(m httpx.ServeMux) {
		ops.HandlerWithOptions(opsH, ops.StdHTTPServerOptions{BaseRouter: m, ErrorHandlerFunc: httpx.StrictRequestError})
	}); err != nil {
		return err
	}
	consoleH := console.NewStrictHandlerWithOptions(&consoleHandlers{svc: svc, codec: codec, errLimit: newLimiter(30, time.Minute)}, nil,
		console.StrictHTTPServerOptions{RequestErrorHandlerFunc: httpx.StrictRequestError, ResponseErrorHandlerFunc: httpx.StrictResponseError})
	return r.Mount(consoleSpec, func(m httpx.ServeMux) {
		console.HandlerWithOptions(consoleH, console.StdHTTPServerOptions{BaseRouter: m, ErrorHandlerFunc: httpx.StrictRequestError})
	})
}

// problem はユースケースのエラーを Problem に変換する。対応のないものはそのまま返し、internal_error になる。
func problem(err error) error {
	var ve *domain.ValidationError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ve):
		return httpx.Error(errcodes.ValidationFailed, "", httpx.FieldError{Field: ve.Field, Code: ve.Code})
	case errors.Is(err, domain.ErrTenantStateConflict):
		return httpx.Error(errcodes.TenantStateConflict, "")
	case errors.Is(err, postgres.ErrShopIDTaken):
		return httpx.Error(errcodes.FincodeShopIdTaken, "")
	case errors.Is(err, domain.ErrTenantNotWritable):
		return httpx.Error(errcodes.TenantNotActive, "")
	case errors.Is(err, postgres.ErrStaffExists):
		return httpx.Error(errcodes.StaffAlreadyExists, "")
	case errors.Is(err, domain.ErrLastOwner):
		return httpx.Error(errcodes.LastOwnerRequired, "")
	case errors.Is(err, domain.ErrAPIKeyLimit):
		return httpx.Error(errcodes.ApiKeyLimitExceeded, "")
	case errors.Is(err, contracts.ErrTenantNotFound), errors.Is(err, app.ErrStaffNotFound), errors.Is(err, app.ErrAPIKeyNotFound):
		return httpx.Error(errcodes.NotFound, "")
	case errors.Is(err, postgres.ErrVersionConflict):
		return httpx.Error(errcodes.Conflict, "")
	case errors.Is(err, app.ErrStaffAlreadyMember):
		return httpx.Error(errcodes.StaffAlreadyMember, "")
	case errors.Is(err, domain.ErrNotInvited):
		return httpx.Error(errcodes.StaffNotInvited, "")
	case errors.Is(err, identity.ErrUnavailable):
		return httpx.Error(errcodes.IdentityProviderUnavailable, "")
	}
	return err
}

// ifMatch は If-Match（version の数値。弱い ETag の W/"…" も受ける）を読む。
func ifMatch(v *string) (*int, error) {
	if v == nil || *v == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(strings.Trim(*v, `W/"`))
	if err != nil {
		return nil, httpx.Error(errcodes.ValidationFailed, "If-Match must be the version", httpx.FieldError{Field: "If-Match", Code: "invalid"})
	}
	return &n, nil
}

func pageLimit(l *int) int {
	if l == nil {
		return 20
	}
	return *l
}

// decodeCursor は starting_after を復号する。不正なら validation_failed。
func decodeCursor[T any](codec *httpx.CursorCodec, s *string) (*T, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	var c T
	if err := codec.Decode(*s, &c); err != nil {
		return nil, httpx.Error(errcodes.ValidationFailed, "starting_after is invalid", httpx.FieldError{Field: "starting_after", Code: "invalid"})
	}
	return &c, nil
}

// ---- ops ----

type opsHandlers struct {
	svc          *app.Service
	codec        *httpx.CursorCodec
	allowSubject bool
}

var _ ops.StrictServerInterface = (*opsHandlers)(nil)

func toOpsTenant(s domain.TenantState) ops.Tenant {
	return ops.Tenant{
		Id: s.ID.UUID(), Name: s.Name, Status: ops.TenantStatus(s.Status), FincodeShopId: s.FincodeShopID,
		ApprovedAt: s.ApprovedAt, SuspendedAt: s.SuspendedAt, SuspendReason: s.SuspendReason, CreatedAt: s.CreatedAt,
	}
}

func (h *opsHandlers) OpsCreateTenant(ctx context.Context, req ops.OpsCreateTenantRequestObject) (ops.OpsCreateTenantResponseObject, error) {
	st, err := h.svc.RegisterTenant(ctx, req.Body.Name)
	if err != nil {
		return nil, problem(err)
	}
	return ops.OpsCreateTenant201JSONResponse(toOpsTenant(st)), nil
}

func (h *opsHandlers) OpsListTenants(ctx context.Context, req ops.OpsListTenantsRequestObject) (ops.OpsListTenantsResponseObject, error) {
	after, err := decodeCursor[postgres.TenantCursor](h.codec, req.Params.StartingAfter)
	if err != nil {
		return nil, err
	}
	var status *string
	if req.Params.Status != nil {
		s := string(*req.Params.Status)
		status = &s
	}
	list, more, err := h.svc.ListTenants(ctx, status, after, pageLimit(req.Params.Limit))
	if err != nil {
		return nil, problem(err)
	}
	resp := ops.OpsListTenants200JSONResponse{Data: make([]ops.Tenant, len(list)), HasMore: more}
	for i, s := range list {
		resp.Data[i] = toOpsTenant(s)
	}
	if more {
		last := list[len(list)-1]
		c := h.codec.Encode(postgres.TenantCursor{CreatedAt: last.CreatedAt, ID: last.ID.UUID()})
		resp.NextCursor = &c
	}
	return resp, nil
}

func (h *opsHandlers) OpsGetTenant(ctx context.Context, req ops.OpsGetTenantRequestObject) (ops.OpsGetTenantResponseObject, error) {
	st, err := h.svc.GetTenantState(ctx, sk.IDFromUUID[sk.TenantTag](req.TenantId))
	if err != nil {
		return nil, problem(err)
	}
	return ops.OpsGetTenant200JSONResponse(toOpsTenant(st)), nil
}

func (h *opsHandlers) OpsApproveTenant(ctx context.Context, req ops.OpsApproveTenantRequestObject) (ops.OpsApproveTenantResponseObject, error) {
	st, err := h.svc.ApproveTenant(ctx, sk.IDFromUUID[sk.TenantTag](req.TenantId), req.Body.FincodeShopId)
	if err != nil {
		return nil, problem(err)
	}
	return ops.OpsApproveTenant200JSONResponse(toOpsTenant(st)), nil
}

func (h *opsHandlers) OpsSuspendTenant(ctx context.Context, req ops.OpsSuspendTenantRequestObject) (ops.OpsSuspendTenantResponseObject, error) {
	st, err := h.svc.SuspendTenant(ctx, sk.IDFromUUID[sk.TenantTag](req.TenantId), req.Body.Reason)
	if err != nil {
		return nil, problem(err)
	}
	return ops.OpsSuspendTenant200JSONResponse(toOpsTenant(st)), nil
}

// OpsCreateStaff は最初のオーナーなどの登録。subject を省略すると招待（Cognito でアカウントを作る）。subject の指定は devtools ビルドだけ。
func (h *opsHandlers) OpsCreateStaff(ctx context.Context, req ops.OpsCreateStaffRequestObject) (ops.OpsCreateStaffResponseObject, error) {
	b := req.Body
	tid := sk.IDFromUUID[sk.TenantTag](req.TenantId)
	var st domain.StaffState
	var err error
	switch {
	case b.Subject != nil && h.allowSubject:
		st, err = h.svc.CreateStaff(ctx, tid, *b.Subject, b.Email, b.DisplayName, contracts.Role(b.Role))
	case b.Subject != nil:
		return nil, httpx.Error(errcodes.ValidationFailed, "subject is not accepted", httpx.FieldError{Field: "subject", Code: "not_allowed"})
	default:
		st, err = h.svc.InviteStaff(ctx, tid, b.Email, b.DisplayName, contracts.Role(b.Role))
	}
	if err != nil {
		return nil, problem(err)
	}
	s := toStaff(st, h.svc.Now())
	return ops.OpsCreateStaff201JSONResponse{
		Id: s.Id, Subject: s.Subject, Email: s.Email, DisplayName: s.DisplayName, Role: ops.ConsoleTenantRole(s.Role), Status: ops.ConsoleTenantStaffStatus(s.Status),
		InvitationExpired: s.InvitationExpired, InvitedAt: s.InvitedAt, LastSeenAt: s.LastSeenAt, Version: s.Version, CreatedAt: s.CreatedAt,
	}, nil
}

// ---- console ----

type consoleHandlers struct {
	svc      *app.Service
	codec    *httpx.CursorCodec
	errLimit *limiter
}

var _ console.StrictServerInterface = (*consoleHandlers)(nil)

func toStaff(s domain.StaffState, now time.Time) console.Staff {
	return console.Staff{
		Id: s.ID.UUID(), Subject: s.Subject, Email: s.Email, DisplayName: s.DisplayName, Role: console.Role(s.Role), Status: console.StaffStatus(s.Status),
		InvitationExpired: s.InvitationExpired(now), InvitedAt: s.InvitedAt, LastSeenAt: s.LastSeenAt, Version: s.Version, CreatedAt: s.CreatedAt,
	}
}

func (h *consoleHandlers) ConsoleListMemberships(ctx context.Context, _ console.ConsoleListMembershipsRequestObject) (console.ConsoleListMembershipsResponseObject, error) {
	ms, err := h.svc.ListMemberships(ctx, subjectOf(ctx))
	if err != nil {
		return nil, problem(err)
	}
	resp := console.ConsoleListMemberships200JSONResponse{Data: make([]console.Membership, len(ms))}
	for i, m := range ms {
		perms := make([]string, len(m.Permissions))
		for j, p := range m.Permissions {
			perms[j] = string(p)
		}
		resp.Data[i] = console.Membership{
			TenantId: m.TenantID.UUID(), TenantName: m.TenantName, TenantStatus: console.MembershipTenantStatus(m.TenantStatus), Role: console.Role(m.Role),
			StaffStatus: console.MembershipStaffStatus(m.StaffStatus), Permissions: perms,
		}
	}
	return resp, nil
}

// ConsoleReportClientError は画面の JavaScript のエラーをログに書く（NFR-009）。スタッフごとに 1 分 30 件まで。
func (h *consoleHandlers) ConsoleReportClientError(ctx context.Context, req console.ConsoleReportClientErrorRequestObject) (console.ConsoleReportClientErrorResponseObject, error) {
	sub := subjectOf(ctx)
	if !h.errLimit.allow(sub, h.svc.Now()) {
		return console.ConsoleReportClientError204Response{}, nil // 上限を超えた分は捨てる（画面の動作は止めない）
	}
	b := req.Body
	attrs := []slog.Attr{slog.String("staff_subject", sub), slog.String("message", b.Message)}
	for k, v := range map[string]*string{"stack": b.Stack, "url": b.Url, "tenant_id": b.TenantId, "client_correlation_id": b.CorrelationId} {
		if v != nil {
			attrs = append(attrs, slog.String(k, *v))
		}
	}
	observability.Logger("console.client").LogAttrs(ctx, slog.LevelWarn, "console.client_error", attrs...)
	return console.ConsoleReportClientError204Response{}, nil
}

func (h *consoleHandlers) ConsoleInviteStaff(ctx context.Context, req console.ConsoleInviteStaffRequestObject) (console.ConsoleInviteStaffResponseObject, error) {
	b := req.Body
	st, err := h.svc.InviteStaff(ctx, httpx.TenantID(ctx), b.Email, b.DisplayName, contracts.Role(b.Role))
	if err != nil {
		return nil, problem(err)
	}
	return console.ConsoleInviteStaff201JSONResponse(toStaff(st, h.svc.Now())), nil
}

func (h *consoleHandlers) ConsoleResendStaffInvitation(ctx context.Context, req console.ConsoleResendStaffInvitationRequestObject) (console.ConsoleResendStaffInvitationResponseObject, error) {
	st, err := h.svc.ResendInvitation(ctx, httpx.TenantID(ctx), sk.IDFromUUID[domain.StaffTag](req.StaffId))
	if err != nil {
		return nil, problem(err)
	}
	return console.ConsoleResendStaffInvitation202JSONResponse(toStaff(st, h.svc.Now())), nil
}

func subjectOf(ctx context.Context) string {
	p, _ := httpx.PrincipalFrom(ctx)
	return p.Subject
}

func (h *consoleHandlers) ConsoleListStaff(ctx context.Context, req console.ConsoleListStaffRequestObject) (console.ConsoleListStaffResponseObject, error) {
	after, err := decodeCursor[postgres.StaffCursor](h.codec, req.Params.StartingAfter)
	if err != nil {
		return nil, err
	}
	list, more, err := h.svc.ListStaff(ctx, httpx.TenantID(ctx), after, pageLimit(req.Params.Limit))
	if err != nil {
		return nil, problem(err)
	}
	resp := console.ConsoleListStaff200JSONResponse{Data: make([]console.Staff, len(list)), HasMore: more}
	for i, s := range list {
		resp.Data[i] = toStaff(s, h.svc.Now())
	}
	if more {
		last := list[len(list)-1]
		c := h.codec.Encode(postgres.StaffCursor{CreatedAt: last.CreatedAt, ID: last.ID.UUID()})
		resp.NextCursor = &c
	}
	return resp, nil
}

func (h *consoleHandlers) ConsoleUpdateStaff(ctx context.Context, req console.ConsoleUpdateStaffRequestObject) (console.ConsoleUpdateStaffResponseObject, error) {
	v, err := ifMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	st, err := h.svc.ChangeStaffRole(ctx, httpx.TenantID(ctx), sk.IDFromUUID[domain.StaffTag](req.StaffId), contracts.Role(req.Body.Role), v)
	if err != nil {
		return nil, problem(err)
	}
	return console.ConsoleUpdateStaff200JSONResponse(toStaff(st, h.svc.Now())), nil
}

func (h *consoleHandlers) ConsoleDeleteStaff(ctx context.Context, req console.ConsoleDeleteStaffRequestObject) (console.ConsoleDeleteStaffResponseObject, error) {
	if err := h.svc.RemoveStaff(ctx, httpx.TenantID(ctx), sk.IDFromUUID[domain.StaffTag](req.StaffId)); err != nil {
		return nil, problem(err)
	}
	return console.ConsoleDeleteStaff204Response{}, nil
}

func (h *consoleHandlers) ConsoleCreateApiKey(ctx context.Context, req console.ConsoleCreateApiKeyRequestObject) (console.ConsoleCreateApiKeyResponseObject, error) {
	k, err := h.svc.IssueAPIKey(ctx, httpx.TenantID(ctx), req.Body.Name, subjectOf(ctx))
	if err != nil {
		return nil, problem(err)
	}
	return noStore{console.ConsoleCreateApiKey201JSONResponse{
		Id: k.State.ID.UUID(), Name: k.State.Name, KeyPrefix: k.State.Prefix, ApiKey: k.Raw, CreatedAt: k.State.CreatedAt,
	}}, nil
}

// noStore は平文のキーを含む応答をキャッシュさせない。
type noStore struct {
	console.ConsoleCreateApiKey201JSONResponse
}

func (n noStore) VisitConsoleCreateApiKeyResponse(w nethttp.ResponseWriter) error {
	w.Header().Set("Cache-Control", "no-store")
	return n.ConsoleCreateApiKey201JSONResponse.VisitConsoleCreateApiKeyResponse(w)
}

func toAPIKey(s domain.APIKeyState) console.ApiKey {
	status := console.ApiKeyStatusActive
	if s.RevokedAt != nil {
		status = console.ApiKeyStatusRevoked
	}
	return console.ApiKey{Id: s.ID.UUID(), Name: s.Name, KeyPrefix: s.Prefix, Status: status, CreatedAt: s.CreatedAt, RevokedAt: s.RevokedAt}
}

func (h *consoleHandlers) ConsoleListApiKeys(ctx context.Context, req console.ConsoleListApiKeysRequestObject) (console.ConsoleListApiKeysResponseObject, error) {
	after, err := decodeCursor[postgres.APIKeyCursor](h.codec, req.Params.StartingAfter)
	if err != nil {
		return nil, err
	}
	var status *string
	if req.Params.Status != nil {
		s := string(*req.Params.Status)
		status = &s
	}
	list, more, err := h.svc.ListAPIKeys(ctx, httpx.TenantID(ctx), status, after, pageLimit(req.Params.Limit))
	if err != nil {
		return nil, problem(err)
	}
	resp := console.ConsoleListApiKeys200JSONResponse{Data: make([]console.ApiKey, len(list)), HasMore: more}
	for i, s := range list {
		resp.Data[i] = toAPIKey(s)
	}
	if more {
		last := list[len(list)-1]
		c := h.codec.Encode(postgres.APIKeyCursor{CreatedAt: last.CreatedAt, ID: last.ID.UUID()})
		resp.NextCursor = &c
	}
	return resp, nil
}

func (h *consoleHandlers) ConsoleRevokeApiKey(ctx context.Context, req console.ConsoleRevokeApiKeyRequestObject) (console.ConsoleRevokeApiKeyResponseObject, error) {
	if err := h.svc.RevokeAPIKey(ctx, httpx.TenantID(ctx), sk.IDFromUUID[domain.APIKeyTag](req.ApiKeyId), subjectOf(ctx)); err != nil {
		return nil, problem(err)
	}
	return console.ConsoleRevokeApiKey204Response{}, nil
}

// limiter はスタッフごとの固定窓の件数制限（画面のエラーの収集用。プロセスの中だけで数える）。
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	seen   map[string]*window
}

type window struct {
	start time.Time
	n     int
}

func newLimiter(max int, w time.Duration) *limiter {
	return &limiter{max: max, window: w, seen: map[string]*window{}}
}

func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.seen[key]
	if !ok || now.Sub(w.start) >= l.window {
		if len(l.seen) > 10000 {
			l.seen = map[string]*window{} // 古い窓を捨てる（メモリの上限）
		}
		l.seen[key] = &window{start: now, n: 1}
		return true
	}
	w.n++
	return w.n <= l.max
}
