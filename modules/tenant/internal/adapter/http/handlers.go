// Package http は tenant モジュールの ops・console API（oapi-codegen の strict server を実装する）。
package http

import (
	"context"
	"errors"
	nethttp "net/http"

	"subscription-platform/api/openapi"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/httpx/errcodes"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/tenant/contracts"
	"subscription-platform/modules/tenant/internal/adapter/http/gen/console"
	"subscription-platform/modules/tenant/internal/adapter/http/gen/ops"
	"subscription-platform/modules/tenant/internal/adapter/postgres"
	"subscription-platform/modules/tenant/internal/app"
	"subscription-platform/modules/tenant/internal/domain"
)

// Register は ops・console のルートを登録する。
func Register(r *httpx.Router, svc *app.Service, codec *httpx.CursorCodec) error {
	opsSpec, err := openapi.Load("ops/tenant.yaml")
	if err != nil {
		return err
	}
	consoleSpec, err := openapi.Load("console/tenant.yaml")
	if err != nil {
		return err
	}
	opsH := ops.NewStrictHandlerWithOptions(&opsHandlers{svc: svc, codec: codec}, nil,
		ops.StrictHTTPServerOptions{RequestErrorHandlerFunc: httpx.StrictRequestError, ResponseErrorHandlerFunc: httpx.StrictResponseError})
	if err := r.Mount(opsSpec, func(m httpx.ServeMux) {
		ops.HandlerWithOptions(opsH, ops.StdHTTPServerOptions{BaseRouter: m, ErrorHandlerFunc: httpx.StrictRequestError})
	}); err != nil {
		return err
	}
	consoleH := console.NewStrictHandlerWithOptions(&consoleHandlers{svc: svc, codec: codec}, nil,
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
	}
	return err
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
	svc   *app.Service
	codec *httpx.CursorCodec
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

func (h *opsHandlers) OpsCreateStaff(ctx context.Context, req ops.OpsCreateStaffRequestObject) (ops.OpsCreateStaffResponseObject, error) {
	b := req.Body
	st, err := h.svc.CreateStaff(ctx, sk.IDFromUUID[sk.TenantTag](req.TenantId), b.Subject, b.Email, b.DisplayName, contracts.Role(b.Role))
	if err != nil {
		return nil, problem(err)
	}
	return ops.OpsCreateStaff201JSONResponse{
		Id: st.ID.UUID(), Subject: st.Subject, Email: st.Email, DisplayName: st.DisplayName, Role: ops.ConsoleTenantRole(st.Role), CreatedAt: st.CreatedAt,
	}, nil
}

// ---- console ----

type consoleHandlers struct {
	svc   *app.Service
	codec *httpx.CursorCodec
}

var _ console.StrictServerInterface = (*consoleHandlers)(nil)

func toStaff(s domain.StaffState) console.Staff {
	return console.Staff{Id: s.ID.UUID(), Subject: s.Subject, Email: s.Email, DisplayName: s.DisplayName, Role: console.Role(s.Role), CreatedAt: s.CreatedAt}
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
		resp.Data[i] = toStaff(s)
	}
	if more {
		last := list[len(list)-1]
		c := h.codec.Encode(postgres.StaffCursor{CreatedAt: last.CreatedAt, ID: last.ID.UUID()})
		resp.NextCursor = &c
	}
	return resp, nil
}

func (h *consoleHandlers) ConsoleUpdateStaff(ctx context.Context, req console.ConsoleUpdateStaffRequestObject) (console.ConsoleUpdateStaffResponseObject, error) {
	st, err := h.svc.ChangeStaffRole(ctx, httpx.TenantID(ctx), sk.IDFromUUID[domain.StaffTag](req.StaffId), contracts.Role(req.Body.Role))
	if err != nil {
		return nil, problem(err)
	}
	return console.ConsoleUpdateStaff200JSONResponse(toStaff(st)), nil
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
