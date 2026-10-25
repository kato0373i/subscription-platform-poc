// Package http は payments モジュールの API（支払方法・申込ページの設定・ops の確認と手動確定・Webhook・dev の StubAdapter の操作）。
package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	nethttp "net/http"

	"github.com/google/uuid"

	"subscription-platform/api/openapi"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/httpx/errcodes"
	"subscription-platform/internal/platform/observability"
	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/payments/contracts"
	"subscription-platform/modules/payments/internal/adapter/http/gen/checkout"
	"subscription-platform/modules/payments/internal/adapter/http/gen/me"
	"subscription-platform/modules/payments/internal/adapter/http/gen/ops"
	"subscription-platform/modules/payments/internal/adapter/postgres"
	"subscription-platform/modules/payments/internal/adapter/provider"
	"subscription-platform/modules/payments/internal/app"
	"subscription-platform/modules/payments/internal/domain"
)

// Register は me・checkout・ops の API と POST /webhooks/{provider} を登録する。
func Register(r *httpx.Router, svc *app.Service, codec *httpx.CursorCodec, publicKeys map[string]string) error {
	h := &handlers{svc: svc, codec: codec, publicKeys: publicKeys}
	reqErr, resErr := httpx.StrictRequestError, httpx.StrictResponseError
	for _, m := range []struct {
		spec     string
		register func(httpx.ServeMux)
	}{
		{"me/payments.yaml", func(mux httpx.ServeMux) {
			s := me.NewStrictHandlerWithOptions(meHandlers{h}, nil, me.StrictHTTPServerOptions{RequestErrorHandlerFunc: reqErr, ResponseErrorHandlerFunc: resErr})
			me.HandlerWithOptions(s, me.StdHTTPServerOptions{BaseRouter: mux, ErrorHandlerFunc: reqErr, Middlewares: []me.MiddlewareFunc{WithIdempotencyKey}})
		}},
		{"checkout/payments.yaml", func(mux httpx.ServeMux) {
			s := checkout.NewStrictHandlerWithOptions(checkoutHandlers{h}, nil, checkout.StrictHTTPServerOptions{RequestErrorHandlerFunc: reqErr, ResponseErrorHandlerFunc: resErr})
			checkout.HandlerWithOptions(s, checkout.StdHTTPServerOptions{BaseRouter: mux, ErrorHandlerFunc: reqErr})
		}},
		{"ops/payments.yaml", func(mux httpx.ServeMux) {
			s := ops.NewStrictHandlerWithOptions(opsHandlers{h}, nil, ops.StrictHTTPServerOptions{RequestErrorHandlerFunc: reqErr, ResponseErrorHandlerFunc: resErr})
			ops.HandlerWithOptions(s, ops.StdHTTPServerOptions{BaseRouter: mux, ErrorHandlerFunc: reqErr})
		}},
	} {
		spec, err := openapi.Load(m.spec)
		if err != nil {
			return err
		}
		if err := r.Mount(spec, m.register); err != nil {
			return err
		}
	}
	r.HandleUnauthenticated("POST /webhooks/{provider}", nethttp.HandlerFunc(h.webhook))
	return nil
}

// Problem は payments のエラーを Problem にする。対応がなければ nil（申込 API の配線でも使う）。
func Problem(err error) *httpx.Problem {
	switch {
	case errors.Is(err, c.ErrPaymentMethodNotFound), errors.Is(err, c.ErrAttemptNotFound):
		return httpx.Error(errcodes.NotFound, "")
	case errors.Is(err, c.ErrPaymentMethodLimitExceeded):
		return httpx.Error(errcodes.PaymentMethodLimitExceeded, "")
	case errors.Is(err, c.ErrInvalidToken):
		return httpx.Error(errcodes.InvalidPaymentToken, "")
	case errors.Is(err, c.ErrUnsupportedMethod):
		return httpx.Error(errcodes.UnsupportedPaymentMethod, "")
	case errors.Is(err, c.ErrIdempotencyKeyReused):
		return httpx.Error(errcodes.IdempotencyKeyReused, "")
	case errors.Is(err, c.ErrProviderUnavailable):
		p := httpx.Error(errcodes.ProviderUnavailable, "")
		p.RetryAfter = 30
		return p
	case errors.Is(err, c.ErrAttemptFinalized):
		return httpx.Error(errcodes.AttemptAlreadyFinalized, "")
	case errors.Is(err, c.ErrAttemptNotResolvable):
		return httpx.Error(errcodes.AttemptNotResolvable, "")
	case errors.Is(err, c.ErrOutcomeMismatch):
		return httpx.Error(errcodes.OutcomeMismatch, "")
	case errors.Is(err, c.ErrPayableAlreadyPaidKind):
		return httpx.Error(errcodes.PayableAlreadyPaid, "")
	case errors.Is(err, c.ErrInvalidAmount), errors.Is(err, c.ErrInvalidIdempotencyKey):
		return httpx.Error(errcodes.ValidationFailed, err.Error())
	}
	return nil
}

func problem(err error) error {
	if p := Problem(err); p != nil {
		return p
	}
	return err
}

type handlers struct {
	svc        *app.Service
	codec      *httpx.CursorCodec
	publicKeys map[string]string
}

// convert は JSON を通して生成された型に写す（匿名の構造体を含む型を手で組み立てないため）。
func convert[T any](v any) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("payments: convert response: %w", err)
	}
	return out, nil
}

func methodJSON(v c.PaymentMethodView) map[string]any {
	return map[string]any{"id": v.ID, "method_type": v.MethodType, "status": v.Status, "display": v.Display, "expired": v.Expired, "created_at": v.CreatedAt}
}

// ---- me ----

type meHandlers struct{ *handlers }

var _ me.StrictServerInterface = meHandlers{}

func (h meHandlers) MeRegisterPaymentMethod(ctx context.Context, req me.MeRegisterPaymentMethodRequestObject) (me.MeRegisterPaymentMethodResponseObject, error) {
	v, err := h.svc.RegisterPaymentMethod(ctx, c.RegisterPaymentMethodCmd{
		CustomerAccountID: httpx.ConsumerID(ctx), MethodType: c.MethodType(req.Body.MethodType), Provider: req.Body.Provider, Token: req.Body.Token,
		IdempotencyKey: idempotencyKey(ctx),
	})
	if err != nil {
		return nil, problem(err)
	}
	return convert[me.MeRegisterPaymentMethod201JSONResponse](methodJSON(v))
}

type idemKey struct{}

// idempotencyKey は HTTP の Idempotency-Key（middleware が context に入れる）。
func idempotencyKey(ctx context.Context) string {
	s, _ := ctx.Value(idemKey{}).(string)
	return s
}

func (h meHandlers) MeListPaymentMethods(ctx context.Context, req me.MeListPaymentMethodsRequestObject) (me.MeListPaymentMethodsResponseObject, error) {
	var after *postgres.Cursor
	if s := req.Params.StartingAfter; s != nil && *s != "" {
		var cur postgres.Cursor
		if err := h.codec.Decode(*s, &cur); err != nil {
			return nil, httpx.Error(errcodes.ValidationFailed, "starting_after is invalid", httpx.FieldError{Field: "starting_after", Code: "invalid"})
		}
		after = &cur
	}
	limit := 20
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	items, more, err := h.svc.ListMethods(ctx, httpx.ConsumerID(ctx), after, limit)
	if err != nil {
		return nil, problem(err)
	}
	data := make([]map[string]any, len(items))
	for i, v := range items {
		data[i] = methodJSON(v)
	}
	var next *string
	if more {
		last := items[len(items)-1]
		s := h.codec.Encode(postgres.Cursor{CreatedAt: last.CreatedAt, ID: last.ID.UUID()})
		next = &s
	}
	return convert[me.MeListPaymentMethods200JSONResponse](map[string]any{"data": data, "has_more": more, "next_cursor": next})
}

// ---- checkout ----

type checkoutHandlers struct{ *handlers }

var _ checkout.StrictServerInterface = checkoutHandlers{}

func (h checkoutHandlers) CheckoutPaymentConfig(context.Context, checkout.CheckoutPaymentConfigRequestObject) (checkout.CheckoutPaymentConfigResponseObject, error) {
	methods := []map[string]any{}
	for _, o := range h.svc.PaymentConfig(h.publicKeys) {
		methods = append(methods, map[string]any{"method_type": o.MethodType, "provider": o.Provider, "public_key": o.PublicKey})
	}
	return convert[checkout.CheckoutPaymentConfig200JSONResponse](map[string]any{"methods": methods})
}

// ---- ops ----

type opsHandlers struct{ *handlers }

var _ ops.StrictServerInterface = opsHandlers{}

func attemptJSON(st domain.AttemptState) map[string]any {
	return map[string]any{
		"id": st.ID, "tenant_id": st.TenantID, "payable": map[string]any{"type": st.Payable.Type, "id": st.Payable.ID}, "customer_account_id": st.CustomerAccountID,
		"payment_method_id": st.PaymentMethodID, "method_type": st.MethodType, "provider": st.Provider, "amount": st.Amount.Int64(),
		"idempotency_key": st.IdempotencyKey, "collection_seq": st.CollectionSeq, "provider_order_ref": st.ProviderOrderRef, "status": st.Status,
		"failure_code": st.FailureCode, "retryable": st.Retryable, "provider_raw_code": st.ProviderRawCode, "resolution": st.Resolution,
		"resolved_by": st.ResolvedBy, "resolve_note": st.ResolveNote, "check_count": st.CheckCount, "requested_at": st.RequestedAt, "finalized_at": st.FinalizedAt,
	}
}

func attemptID(u uuid.UUID) sk.PaymentAttemptID { return sk.IDFromUUID[sk.PaymentAttemptTag](u) }

func (h opsHandlers) OpsGetPaymentAttempt(ctx context.Context, req ops.OpsGetPaymentAttemptRequestObject) (ops.OpsGetPaymentAttemptResponseObject, error) {
	st, err := h.svc.GetAttempt(ctx, attemptID(req.AttemptId))
	if err != nil {
		return nil, problem(err)
	}
	return convert[ops.OpsGetPaymentAttempt200JSONResponse](attemptJSON(st))
}

func (h opsHandlers) OpsResolvePaymentAttempt(ctx context.Context, req ops.OpsResolvePaymentAttemptRequestObject) (ops.OpsResolvePaymentAttemptResponseObject, error) {
	b := req.Body
	in := app.ResolveInput{Succeeded: b.Outcome == "succeeded", Note: b.Note, Force: b.Force != nil && *b.Force}
	if p, ok := httpx.PrincipalFrom(ctx); ok {
		in.Operator = p.Subject
	}
	if !in.Succeeded {
		if b.FailureCode == nil || b.Retryable == nil || !domain.FailureCode(*b.FailureCode).Known() {
			return nil, httpx.Error(errcodes.ValidationFailed, "failure_code and retryable are required for failed", httpx.FieldError{Field: "failure_code", Code: "required"})
		}
		in.FailureCode, in.Retryable = domain.FailureCode(*b.FailureCode), *b.Retryable
	}
	st, err := h.svc.ResolveManually(ctx, attemptID(req.AttemptId), in)
	if err != nil {
		return nil, problem(err)
	}
	return convert[ops.OpsResolvePaymentAttempt200JSONResponse](attemptJSON(st))
}

func (h opsHandlers) OpsListReconciliationReports(ctx context.Context, req ops.OpsListReconciliationReportsRequestObject) (ops.OpsListReconciliationReportsResponseObject, error) {
	var date *sk.Date
	if req.Params.BusinessDate != nil {
		d := sk.DateOf(req.Params.BusinessDate.Time) // 00:00 UTC は JST でも同じ日付
		date = &d
	}
	var status *string
	if req.Params.Status != nil {
		s := string(*req.Params.Status)
		status = &s
	}
	var after *uuid.UUID
	if s := req.Params.StartingAfter; s != nil && *s != "" {
		var u uuid.UUID
		if err := h.codec.Decode(*s, &u); err != nil {
			return nil, httpx.Error(errcodes.ValidationFailed, "starting_after is invalid", httpx.FieldError{Field: "starting_after", Code: "invalid"})
		}
		after = &u
	}
	limit := 20
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	rows, more, err := h.svc.Reports(ctx, date, status, after, limit)
	if err != nil {
		return nil, problem(err)
	}
	data := make([]map[string]any, len(rows))
	for i, r := range rows {
		var detail map[string]any
		_ = json.Unmarshal(r.Detail, &detail)
		data[i] = map[string]any{"id": r.ID, "business_date": r.BusinessDate, "provider": r.Provider, "kind": r.Kind, "attempt_id": r.AttemptID,
			"tenant_id": r.TenantID, "provider_order_ref": r.ProviderOrderRef, "detail": detail, "status": r.Status, "detected_at": r.DetectedAt}
	}
	var next *string
	if more {
		s := h.codec.Encode(rows[len(rows)-1].ID)
		next = &s
	}
	return convert[ops.OpsListReconciliationReports200JSONResponse](map[string]any{"data": data, "has_more": more, "next_cursor": next})
}

// ---- POST /webhooks/{provider}（認証の主体なし、署名で検証。§7.9） ----

const webhookBodyLimit = 64 << 10

func (h *handlers) webhook(w nethttp.ResponseWriter, r *nethttp.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, webhookBodyLimit+1))
	if err != nil || len(body) > webhookBodyLimit {
		httpx.WriteProblem(w, r, httpx.Error(errcodes.ValidationFailed, "body too large"))
		return
	}
	name := r.PathValue("provider")
	err = h.svc.IngestWebhook(r.Context(), name, r.Header, body)
	switch {
	case errors.Is(err, app.ErrUnknownProvider):
		httpx.WriteProblem(w, r, httpx.Error(errcodes.NotFound, ""))
	case errors.Is(err, provider.ErrNotImplemented):
		w.WriteHeader(nethttp.StatusNotImplemented)
	case errors.Is(err, provider.ErrInvalidSignature):
		observability.Logger("payments.webhook").LogAttrs(r.Context(), slog.LevelWarn, "payments.webhook_signature_invalid", slog.String("provider", name))
		w.WriteHeader(nethttp.StatusUnauthorized)
	case err != nil:
		httpx.WriteProblem(w, r, err)
	default:
		if ack := h.svc.WebhookAck(name); ack != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(nethttp.StatusOK)
			_, _ = w.Write(ack) //nolint:gosec // 代行が決める固定の本文（要求の内容を含まない）
			return
		}
		w.WriteHeader(nethttp.StatusOK)
	}
}

// WithIdempotencyKey は Idempotency-Key を context に入れる middleware（登録の代行への冪等キーと、二重登録の防止に使う）。
func WithIdempotencyKey(next nethttp.Handler) nethttp.Handler {
	return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if k := r.Header.Get("Idempotency-Key"); k != "" {
			r = r.WithContext(context.WithValue(r.Context(), idemKey{}, k))
		}
		next.ServeHTTP(w, r)
	})
}
