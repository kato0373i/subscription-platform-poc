// Package http は監査記録の ops の閲覧 API（integration.md §7.2）。
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"subscription-platform/api/openapi"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/httpx/errcodes"
	"subscription-platform/modules/audit/internal/adapter/http/gen/ops"
	"subscription-platform/modules/audit/internal/app"
)

// Register は ops の API を登録する。
func Register(r *httpx.Router, svc *app.Service, codec *httpx.CursorCodec) error {
	h := opsHandlers{svc: svc, codec: codec}
	spec, err := openapi.Load("ops/audit.yaml")
	if err != nil {
		return err
	}
	return r.Mount(spec, func(mux httpx.ServeMux) {
		reqErr, resErr := httpx.StrictRequestError, httpx.StrictResponseError
		s := ops.NewStrictHandlerWithOptions(h, nil, ops.StrictHTTPServerOptions{RequestErrorHandlerFunc: reqErr, ResponseErrorHandlerFunc: resErr})
		ops.HandlerWithOptions(s, ops.StdHTTPServerOptions{BaseRouter: mux, ErrorHandlerFunc: reqErr})
	})
}

type opsHandlers struct {
	svc   *app.Service
	codec *httpx.CursorCodec
}

type cursor struct {
	At time.Time `json:"t"`
	ID uuid.UUID `json:"i"`
}

func (h opsHandlers) OpsListAuditRecords(ctx context.Context, req ops.OpsListAuditRecordsRequestObject) (ops.OpsListAuditRecordsResponseObject, error) {
	p := req.Params
	f := app.Filter{TenantID: p.TenantId, Action: p.Action, ActorID: p.ActorId, OccurredFrom: p.OccurredFrom, OccurredTo: p.OccurredTo, Limit: 20}
	if p.Limit != nil {
		f.Limit = *p.Limit
	}
	if p.StartingAfter != nil && *p.StartingAfter != "" {
		var c cursor
		if err := h.codec.Decode(*p.StartingAfter, &c); err != nil {
			return nil, httpx.Error(errcodes.ValidationFailed, "starting_after is invalid", httpx.FieldError{Field: "starting_after", Code: "invalid"})
		}
		f.BeforeAt, f.BeforeID = &c.At, &c.ID
	}
	rows, more, err := h.svc.List(ctx, f)
	if err != nil {
		return nil, err
	}
	data := make([]map[string]any, len(rows))
	for i, r := range rows {
		var ip *string
		if r.SourceIp != "" {
			ip = &r.SourceIp
		}
		data[i] = map[string]any{
			"id": r.ID, "occurred_at": r.OccurredAt, "tenant_id": r.TenantID, "actor": map[string]any{"type": r.ActorType, "id": r.ActorID, "role": r.ActorRole},
			"action": r.Action, "target": map[string]any{"type": r.TargetType, "id": r.TargetID}, "before": raw(r.Before), "after": raw(r.After),
			"outcome": r.Outcome, "request_id": r.RequestID, "correlation_id": r.CorrelationID, "http_method": r.HttpMethod, "route": r.Route, "source_ip": ip,
		}
	}
	var next *string
	if more {
		last := rows[len(rows)-1]
		n := h.codec.Encode(cursor{At: last.OccurredAt, ID: last.ID})
		next = &n
	}
	b, err := json.Marshal(map[string]any{"data": data, "has_more": more, "next_cursor": next})
	if err != nil {
		return nil, err
	}
	var out ops.OpsListAuditRecords200JSONResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("audit: convert response: %w", err)
	}
	return out, nil
}

func raw(b []byte) any {
	if b == nil {
		return nil
	}
	return json.RawMessage(b)
}
