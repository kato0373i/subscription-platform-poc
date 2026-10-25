// Package http は billing モジュールの API（契約者・事業者の請求の照会、リトライの方針、ops の再決済。billing.md §7）。
package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"subscription-platform/api/openapi"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/httpx/errcodes"
	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/billing/contracts"
	"subscription-platform/modules/billing/internal/adapter/http/gen/console"
	"subscription-platform/modules/billing/internal/adapter/http/gen/me"
	"subscription-platform/modules/billing/internal/adapter/http/gen/ops"
	"subscription-platform/modules/billing/internal/adapter/postgres"
	"subscription-platform/modules/billing/internal/app"
	"subscription-platform/modules/billing/internal/domain"
)

// Register は me・console・ops の API を登録する。
func Register(r *httpx.Router, svc *app.Service, codec *httpx.CursorCodec) error {
	h := &handlers{svc: svc, codec: codec}
	reqErr, resErr := httpx.StrictRequestError, httpx.StrictResponseError
	for _, m := range []struct {
		spec     string
		register func(httpx.ServeMux)
	}{
		{"me/billing.yaml", func(mux httpx.ServeMux) {
			s := me.NewStrictHandlerWithOptions(meHandlers{h}, nil, me.StrictHTTPServerOptions{RequestErrorHandlerFunc: reqErr, ResponseErrorHandlerFunc: resErr})
			me.HandlerWithOptions(s, me.StdHTTPServerOptions{BaseRouter: mux, ErrorHandlerFunc: reqErr})
		}},
		{"console/billing.yaml", func(mux httpx.ServeMux) {
			s := console.NewStrictHandlerWithOptions(consoleHandlers{h}, nil, console.StrictHTTPServerOptions{RequestErrorHandlerFunc: reqErr, ResponseErrorHandlerFunc: resErr})
			console.HandlerWithOptions(s, console.StdHTTPServerOptions{BaseRouter: mux, ErrorHandlerFunc: reqErr})
		}},
		{"ops/billing.yaml", func(mux httpx.ServeMux) {
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

// Problem は billing のエラーを Problem にする。対応がなければ nil。
func Problem(err error) *httpx.Problem {
	var dp *c.InvalidDunningPolicyError
	switch {
	case errors.Is(err, c.ErrInvoiceNotFound):
		return httpx.Error(errcodes.NotFound, "")
	case errors.Is(err, c.ErrInvoiceNotCollectible):
		return httpx.Error(errcodes.InvoiceNotCollectible, "")
	case errors.Is(err, c.ErrCollectionInProgress):
		return httpx.Error(errcodes.CollectionInProgress, "")
	case errors.As(err, &dp):
		return httpx.Error(errcodes.ValidationFailed, err.Error(), httpx.FieldError{Field: "retry_offsets_days", Code: dp.Code})
	case errors.Is(err, c.ErrValidation):
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
		return out, fmt.Errorf("billing: convert response: %w", err)
	}
	return out, nil
}

type cursor struct {
	PeriodStart sk.Date   `json:"p"`
	ID          uuid.UUID `json:"i"`
}

type audience int

const (
	forMe audience = iota
	forConsole
)

func invoiceJSON(s domain.State, a audience) map[string]any {
	lines := make([]map[string]any, len(s.Lines))
	for i, l := range s.Lines {
		lines[i] = map[string]any{"kind": l.Kind, "description": l.Description, "amount": l.Amount.Int64()}
	}
	var tax int64
	rate := ""
	for _, t := range s.Tax {
		tax += t.TaxAmount.Int64()
		rate = percent(t.Rate)
	}
	var next *time.Time
	if s.Status == c.StatusOpen && s.Dunning != nil {
		next = s.NextCollectionAt
	}
	m := map[string]any{
		"id": s.ID, "status": s.Status, "kind": s.Kind, "period": map[string]any{"start": s.Period.Start, "end": s.Period.End},
		"total": s.Total.Int64(), "tax": map[string]any{"rate": rate, "amount": tax}, "lines": lines, "issued_at": s.IssuedAt, "paid_at": s.PaidAt,
		"next_collection_at": next,
	}
	if a == forMe {
		if s.Status == c.StatusPaymentUnknown {
			m["status"] = c.StatusProcessing // 内部の不確実さを見せない（§7.1）
		}
		return m
	}
	var sched any
	if d := s.Dunning; d != nil {
		sched = map[string]any{"offsets_days": d.OffsetsDays, "first_failed_on": d.FirstFailedOn}
	}
	for k, v := range map[string]any{
		"subscription_id": s.SubscriptionID, "collection_mode": s.Mode, "subtotal": s.Subtotal.Int64(), "discount_total": s.DiscountTotal.Int64(),
		"next_collection_at": s.NextCollectionAt, "attempt_count": s.AttemptCount, "dunning_stage": s.DunningStage, "dunning_schedule": sched,
		"collection_seq": s.CollectionSeq, "last_failure_code": s.LastFailureCode, "retryable": s.LastRetryable, "voided_at": s.VoidedAt,
		"void_reason": s.VoidReason, "uncollectible_at": s.UncollectibleAt,
	} {
		m[k] = v
	}
	return m
}

// percent は税率の表示（"10%"。整数でなければ小数 1 桁。§7.1）。
func percent(r sk.TaxRate) string {
	if r.Den() == 0 {
		return "0%"
	}
	if r.Num()*100%r.Den() == 0 {
		return fmt.Sprintf("%d%%", r.Num()*100/r.Den())
	}
	d := r.Num() * 1000 / r.Den()
	return fmt.Sprintf("%d.%d%%", d/10, d%10)
}

func (h *handlers) list(ctx context.Context, sub uuid.UUID, after *string, limit *int, a audience) (map[string]any, error) {
	p := postgres.Page{Limit: 20}
	if limit != nil {
		p.Limit = *limit
	}
	if after != nil && *after != "" {
		var cur cursor
		if err := h.codec.Decode(*after, &cur); err != nil {
			return nil, httpx.Error(errcodes.ValidationFailed, "starting_after is invalid", httpx.FieldError{Field: "starting_after", Code: "invalid"})
		}
		p.AfterStart, p.AfterID = &cur.PeriodStart, &cur.ID
	}
	items, more, err := h.svc.ListInvoices(ctx, sk.IDFromUUID[sk.SubscriptionTag](sub), p)
	if err != nil {
		return nil, problem(err)
	}
	data := make([]map[string]any, len(items))
	for i, s := range items {
		data[i] = invoiceJSON(s, a)
	}
	var next *string
	if more {
		last := items[len(items)-1]
		n := h.codec.Encode(cursor{PeriodStart: last.Period.Start, ID: last.ID.UUID()})
		next = &n
	}
	return map[string]any{"data": data, "has_more": more, "next_cursor": next}, nil
}

// ---- me ----

type meHandlers struct{ *handlers }

var _ me.StrictServerInterface = meHandlers{}

func (h meHandlers) MeListInvoices(ctx context.Context, req me.MeListInvoicesRequestObject) (me.MeListInvoicesResponseObject, error) {
	res, err := h.list(ctx, req.SubscriptionId, req.Params.StartingAfter, req.Params.Limit, forMe)
	if err != nil {
		return nil, err
	}
	return convert[me.MeListInvoices200JSONResponse](res)
}

// ---- console ----

type consoleHandlers struct{ *handlers }

var _ console.StrictServerInterface = consoleHandlers{}

func (h consoleHandlers) ConsoleListInvoices(ctx context.Context, req console.ConsoleListInvoicesRequestObject) (console.ConsoleListInvoicesResponseObject, error) {
	res, err := h.list(ctx, req.SubscriptionId, req.Params.StartingAfter, req.Params.Limit, forConsole)
	if err != nil {
		return nil, err
	}
	return convert[console.ConsoleListInvoices200JSONResponse](res)
}

func (h consoleHandlers) ConsoleGetInvoice(ctx context.Context, req console.ConsoleGetInvoiceRequestObject) (console.ConsoleGetInvoiceResponseObject, error) {
	s, err := h.svc.GetInvoiceState(ctx, sk.IDFromUUID[sk.InvoiceTag](req.InvoiceId))
	if err != nil {
		return nil, problem(err)
	}
	return convert[console.ConsoleGetInvoice200JSONResponse](invoiceJSON(s, forConsole))
}

func policyJSON(p domain.DunningPolicy) map[string]any {
	offs := p.Offsets
	if offs == nil {
		offs = []int{}
	}
	return map[string]any{"retry_offsets_days": offs, "is_default": p.IsDefault, "updated_at": p.UpdatedAt}
}

func (h consoleHandlers) ConsoleGetDunningPolicy(ctx context.Context, _ console.ConsoleGetDunningPolicyRequestObject) (console.ConsoleGetDunningPolicyResponseObject, error) {
	p, err := h.svc.Policy(ctx, httpx.TenantID(ctx))
	if err != nil {
		return nil, problem(err)
	}
	return convert[console.ConsoleGetDunningPolicy200JSONResponse](policyJSON(p))
}

func (h consoleHandlers) ConsolePutDunningPolicy(ctx context.Context, req console.ConsolePutDunningPolicyRequestObject) (console.ConsolePutDunningPolicyResponseObject, error) {
	pr, _ := httpx.PrincipalFrom(ctx)
	p, err := h.svc.UpdatePolicy(ctx, httpx.TenantID(ctx), req.Body.RetryOffsetsDays, pr.Subject)
	if err != nil {
		return nil, problem(err)
	}
	return convert[console.ConsolePutDunningPolicy200JSONResponse](policyJSON(p))
}

// ---- ops ----

type opsHandlers struct{ *handlers }

var _ ops.StrictServerInterface = opsHandlers{}

func (h opsHandlers) OpsCollectInvoice(ctx context.Context, req ops.OpsCollectInvoiceRequestObject) (ops.OpsCollectInvoiceResponseObject, error) {
	pr, _ := httpx.PrincipalFrom(ctx)
	note := ""
	if req.Body != nil && req.Body.Note != nil {
		note = *req.Body.Note
	}
	v, at, err := h.svc.OpsCollect(ctx, sk.IDFromUUID[sk.InvoiceTag](req.InvoiceId), pr.Subject, note)
	if err != nil {
		return nil, problem(err)
	}
	return convert[ops.OpsCollectInvoice202JSONResponse](map[string]any{"invoice_id": v.ID, "status": v.Status, "collection_seq": v.CollectionSeq, "requested_at": at})
}
