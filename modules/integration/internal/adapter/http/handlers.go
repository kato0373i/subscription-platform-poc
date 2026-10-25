// Package http は integration モジュールの API（console の送信先・配信履歴、ops の配信履歴・再送。integration.md §7）。
package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"subscription-platform/api/openapi"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/httpx/errcodes"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/integration/internal/adapter/http/gen/console"
	"subscription-platform/modules/integration/internal/adapter/http/gen/ops"
	"subscription-platform/modules/integration/internal/app"
	"subscription-platform/modules/integration/internal/domain"
)

// Register は console・ops の API を登録する。
func Register(r *httpx.Router, svc *app.Service, codec *httpx.CursorCodec) error {
	h := &handlers{svc: svc, codec: codec}
	reqErr, resErr := httpx.StrictRequestError, httpx.StrictResponseError
	for _, m := range []struct {
		spec     string
		register func(httpx.ServeMux)
	}{
		{"console/integration.yaml", func(mux httpx.ServeMux) {
			s := console.NewStrictHandlerWithOptions(consoleHandlers{h}, nil, console.StrictHTTPServerOptions{RequestErrorHandlerFunc: reqErr, ResponseErrorHandlerFunc: resErr})
			console.HandlerWithOptions(s, console.StdHTTPServerOptions{BaseRouter: mux, ErrorHandlerFunc: reqErr})
		}},
		{"ops/integration.yaml", func(mux httpx.ServeMux) {
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
	return nil
}

// Problem は integration のエラーを Problem にする。対応がなければ nil。
func Problem(err error) *httpx.Problem {
	var ve *app.ValidationError
	switch {
	case errors.As(err, &ve):
		return httpx.Error(errcodes.ValidationFailed, err.Error(), httpx.FieldError{Field: ve.Field, Code: ve.Code})
	case errors.Is(err, app.ErrNotFound):
		return httpx.Error(errcodes.NotFound, "")
	case errors.Is(err, app.ErrConflict):
		return httpx.Error(errcodes.Conflict, "")
	case errors.Is(err, app.ErrEndpointLimit):
		return httpx.Error(errcodes.WebhookEndpointLimitExceeded, "")
	case errors.Is(err, app.ErrDeliveryInProgress):
		return httpx.Error(errcodes.WebhookDeliveryInProgress, "")
	case errors.Is(err, app.ErrEndpointUnavailable):
		return httpx.Error(errcodes.WebhookEndpointUnavailable, "")
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
	svc   *app.Service
	codec *httpx.CursorCodec
}

// convert は JSON を通して生成された型に写す。
func convert[T any](v any) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("integration: convert response: %w", err)
	}
	return out, nil
}

type cursor struct {
	At time.Time `json:"t"`
	ID uuid.UUID `json:"i"`
}

func (h *handlers) decode(after *string) (*cursor, error) {
	if after == nil || *after == "" {
		return nil, nil
	}
	var c cursor
	if err := h.codec.Decode(*after, &c); err != nil {
		return nil, httpx.Error(errcodes.ValidationFailed, "starting_after is invalid", httpx.FieldError{Field: "starting_after", Code: "invalid"})
	}
	return &c, nil
}

func limitOf(l *int) int {
	if l == nil {
		return 20
	}
	return *l
}

func endpointJSON(ep *domain.Endpoint) map[string]any {
	return map[string]any{
		"id": ep.ID, "url": ep.URL, "description": ep.Description, "event_types": ep.EventTypes, "status": ep.Status, "api_version": ep.APIVersion,
		"version": ep.Version, "secret_rotated_at": ep.RotatedAt, "previous_secret_expires_at": ep.PreviousExpiresAt, "created_at": ep.CreatedAt,
		"updated_at": ep.UpdatedAt,
	}
}

func deliveryJSON(v app.DeliveryView) map[string]any {
	d := v.Delivery
	var last any
	if d.AttemptCount > 0 {
		var kind *string
		if d.LastError != "" {
			kind = &d.LastError
		}
		last = map[string]any{"status_code": d.LastStatusCode, "error_kind": kind}
	}
	return map[string]any{
		"id": d.ID, "endpoint_id": d.EndpointID, "event": map[string]any{"id": d.EventID, "type": v.EventType, "created_at": v.EventCreatedAt.UTC().Truncate(time.Second)},
		"trigger": d.Trigger, "redelivery_of": d.RedeliveryOf, "status": d.Status, "attempt_count": d.AttemptCount, "first_attempted_at": d.FirstAttemptedAt,
		"last_attempted_at": d.LastAttemptedAt, "next_attempt_at": d.NextAttemptAt, "deadline_at": d.DeadlineAt, "last_response": last, "created_at": d.CreatedAt,
	}
}

func (h *handlers) deliveryList(ctx context.Context, scope app.Scope, f app.DeliveryFilter, after *string) (map[string]any, error) {
	c, err := h.decode(after)
	if err != nil {
		return nil, err
	}
	if c != nil {
		f.BeforeCreated, f.BeforeID = &c.At, &c.ID
	}
	page, err := h.svc.ListDeliveries(ctx, scope, f)
	if err != nil {
		return nil, problem(err)
	}
	data := make([]map[string]any, len(page.Data))
	for i, v := range page.Data {
		data[i] = deliveryJSON(v)
	}
	var next *string
	if page.HasMore {
		last := page.Data[len(page.Data)-1].Delivery
		n := h.codec.Encode(cursor{At: last.CreatedAt, ID: last.ID.UUID()})
		next = &n
	}
	return map[string]any{"data": data, "has_more": page.HasMore, "next_cursor": next}, nil
}

// ---- console ----

type consoleHandlers struct{ *handlers }

func (h consoleHandlers) ConsoleCreateWebhookEndpoint(ctx context.Context, req console.ConsoleCreateWebhookEndpointRequestObject) (console.ConsoleCreateWebhookEndpointResponseObject, error) {
	b := req.Body
	in := app.CreateEndpointInput{URL: b.Url, EventTypes: b.EventTypes, Enabled: true}
	if b.Description != nil {
		in.Description = *b.Description
	}
	if b.Enabled != nil {
		in.Enabled = *b.Enabled
	}
	ep, secret, err := h.svc.CreateEndpoint(ctx, httpx.TenantID(ctx), in)
	if err != nil {
		return nil, problem(err)
	}
	m := endpointJSON(ep)
	m["secret"] = string(secret)
	return convert[console.ConsoleCreateWebhookEndpoint201JSONResponse](m)
}

func (h consoleHandlers) ConsoleListWebhookEndpoints(ctx context.Context, req console.ConsoleListWebhookEndpointsRequestObject) (console.ConsoleListWebhookEndpointsResponseObject, error) {
	c, err := h.decode(req.Params.StartingAfter)
	if err != nil {
		return nil, err
	}
	var afterAt *time.Time
	var afterID *domain.EndpointID
	if c != nil {
		id := sk.IDFromUUID[domain.EndpointTag](c.ID)
		afterAt, afterID = &c.At, &id
	}
	page, err := h.svc.ListEndpoints(ctx, httpx.TenantID(ctx), limitOf(req.Params.Limit), afterAt, afterID)
	if err != nil {
		return nil, problem(err)
	}
	data := make([]map[string]any, len(page.Data))
	for i, ep := range page.Data {
		data[i] = endpointJSON(ep)
	}
	var next *string
	if page.HasMore {
		last := page.Data[len(page.Data)-1]
		n := h.codec.Encode(cursor{At: last.CreatedAt, ID: last.ID.UUID()})
		next = &n
	}
	return convert[console.ConsoleListWebhookEndpoints200JSONResponse](map[string]any{"data": data, "has_more": page.HasMore, "next_cursor": next})
}

func (h consoleHandlers) ConsoleGetWebhookEndpoint(ctx context.Context, req console.ConsoleGetWebhookEndpointRequestObject) (console.ConsoleGetWebhookEndpointResponseObject, error) {
	ep, err := h.svc.GetEndpoint(ctx, httpx.TenantID(ctx), sk.IDFromUUID[domain.EndpointTag](req.EndpointId))
	if err != nil {
		return nil, problem(err)
	}
	return convert[console.ConsoleGetWebhookEndpoint200JSONResponse](endpointJSON(ep))
}

func (h consoleHandlers) ConsoleUpdateWebhookEndpoint(ctx context.Context, req console.ConsoleUpdateWebhookEndpointRequestObject) (console.ConsoleUpdateWebhookEndpointResponseObject, error) {
	var ifMatch *int
	if v := req.Params.IfMatch; v != nil && *v != "" {
		n, err := strconv.Atoi(strings.Trim(*v, `"W/`))
		if err != nil {
			return nil, httpx.Error(errcodes.ValidationFailed, "If-Match must be the version", httpx.FieldError{Field: "If-Match", Code: "invalid"})
		}
		ifMatch = &n
	}
	b := req.Body
	p := domain.Patch{URL: b.Url, Description: b.Description, Enabled: b.Enabled}
	if b.EventTypes != nil {
		p.EventTypes = *b.EventTypes
		if p.EventTypes == nil {
			p.EventTypes = []string{}
		}
	}
	ep, err := h.svc.UpdateEndpoint(ctx, httpx.TenantID(ctx), sk.IDFromUUID[domain.EndpointTag](req.EndpointId), p, ifMatch)
	if err != nil {
		return nil, problem(err)
	}
	return convert[console.ConsoleUpdateWebhookEndpoint200JSONResponse](endpointJSON(ep))
}

func (h consoleHandlers) ConsoleDeleteWebhookEndpoint(ctx context.Context, req console.ConsoleDeleteWebhookEndpointRequestObject) (console.ConsoleDeleteWebhookEndpointResponseObject, error) {
	if err := h.svc.DeleteEndpoint(ctx, httpx.TenantID(ctx), sk.IDFromUUID[domain.EndpointTag](req.EndpointId)); err != nil {
		return nil, problem(err)
	}
	return console.ConsoleDeleteWebhookEndpoint204Response{}, nil
}

func (h consoleHandlers) ConsoleRotateWebhookSecret(ctx context.Context, req console.ConsoleRotateWebhookSecretRequestObject) (console.ConsoleRotateWebhookSecretResponseObject, error) {
	grace := 24
	if req.Body != nil && req.Body.GracePeriodHours != nil {
		grace = *req.Body.GracePeriodHours
	}
	secret, exp, err := h.svc.RotateSecret(ctx, httpx.TenantID(ctx), sk.IDFromUUID[domain.EndpointTag](req.EndpointId), grace)
	if err != nil {
		return nil, problem(err)
	}
	return convert[console.ConsoleRotateWebhookSecret200JSONResponse](map[string]any{"secret": string(secret), "previous_secret_expires_at": exp})
}

func (h consoleHandlers) ConsoleListWebhookDeliveries(ctx context.Context, req console.ConsoleListWebhookDeliveriesRequestObject) (console.ConsoleListWebhookDeliveriesResponseObject, error) {
	p := req.Params
	f := app.DeliveryFilter{EndpointID: p.EndpointId, EventID: p.EventId, EventType: p.EventType, Limit: limitOf(p.Limit)}
	if p.Status != nil {
		s := string(*p.Status)
		f.Status = &s
	}
	res, err := h.deliveryList(ctx, app.Scope{Tenant: httpx.TenantID(ctx)}, f, p.StartingAfter)
	if err != nil {
		return nil, err
	}
	return convert[console.ConsoleListWebhookDeliveries200JSONResponse](res)
}

func (h consoleHandlers) ConsoleGetWebhookDelivery(ctx context.Context, req console.ConsoleGetWebhookDeliveryRequestObject) (console.ConsoleGetWebhookDeliveryResponseObject, error) {
	d, err := h.svc.GetDelivery(ctx, httpx.TenantID(ctx), sk.IDFromUUID[domain.DeliveryTag](req.DeliveryId))
	if err != nil {
		return nil, problem(err)
	}
	m := deliveryJSON(d.DeliveryView)
	attempts := make([]map[string]any, len(d.Attempts))
	for i, a := range d.Attempts {
		attempts[i] = map[string]any{
			"attempt_no": a.AttemptNo, "requested_at": a.RequestedAt, "duration_ms": a.DurationMs, "status_code": a.StatusCode, "error_kind": a.ErrorKind,
			"response_excerpt": a.ResponseExcerpt,
		}
	}
	m["attempts"], m["payload"] = attempts, json.RawMessage(d.Payload)
	return convert[console.ConsoleGetWebhookDelivery200JSONResponse](m)
}

func (h consoleHandlers) ConsoleRedeliverWebhook(ctx context.Context, req console.ConsoleRedeliverWebhookRequestObject) (console.ConsoleRedeliverWebhookResponseObject, error) {
	v, err := h.svc.Redeliver(ctx, app.Scope{Tenant: httpx.TenantID(ctx)}, sk.IDFromUUID[domain.DeliveryTag](req.DeliveryId))
	if err != nil {
		return nil, problem(err)
	}
	return convert[console.ConsoleRedeliverWebhook202JSONResponse](deliveryJSON(v))
}

// ---- ops ----

type opsHandlers struct{ *handlers }

func (h opsHandlers) OpsListWebhookDeliveries(ctx context.Context, req ops.OpsListWebhookDeliveriesRequestObject) (ops.OpsListWebhookDeliveriesResponseObject, error) {
	p := req.Params
	f := app.DeliveryFilter{EndpointID: p.EndpointId, Limit: limitOf(p.Limit)}
	if p.Status != nil {
		s := string(*p.Status)
		f.Status = &s
	}
	res, err := h.deliveryList(ctx, app.Scope{Tenant: sk.IDFromUUID[sk.TenantTag](p.TenantId), Ops: true}, f, p.StartingAfter)
	if err != nil {
		return nil, err
	}
	return convert[ops.OpsListWebhookDeliveries200JSONResponse](res)
}

func (h opsHandlers) OpsRedeliverWebhook(ctx context.Context, req ops.OpsRedeliverWebhookRequestObject) (ops.OpsRedeliverWebhookResponseObject, error) {
	v, err := h.svc.Redeliver(ctx, app.Scope{Ops: true}, sk.IDFromUUID[domain.DeliveryTag](req.DeliveryId))
	if err != nil {
		return nil, problem(err)
	}
	return convert[ops.OpsRedeliverWebhook202JSONResponse](deliveryJSON(v))
}
