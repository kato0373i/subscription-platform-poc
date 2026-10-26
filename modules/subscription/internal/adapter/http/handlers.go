// Package http は subscription モジュールの me・console・tenant API（oapi-codegen の strict server を実装する）。
package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"subscription-platform/api/openapi"
	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/httpx/errcodes"
	sk "subscription-platform/internal/sharedkernel"
	customer "subscription-platform/modules/customer/contracts"
	c "subscription-platform/modules/subscription/contracts"
	"subscription-platform/modules/subscription/internal/adapter/http/gen/console"
	"subscription-platform/modules/subscription/internal/adapter/http/gen/me"
	"subscription-platform/modules/subscription/internal/adapter/http/gen/tenant"
	"subscription-platform/modules/subscription/internal/adapter/postgres"
	"subscription-platform/modules/subscription/internal/app"
)

// Register は me・console・tenant のルートを登録する。
func Register(r *httpx.Router, svc *app.Service, codec *httpx.CursorCodec, customer customer.Service) error {
	h := &handlers{svc: svc, codec: codec, customer: customer}
	for _, m := range []struct {
		spec     string
		register func(httpx.ServeMux)
	}{
		{"me/subscription.yaml", func(mux httpx.ServeMux) {
			s := me.NewStrictHandlerWithOptions(meHandlers{h}, nil, me.StrictHTTPServerOptions{RequestErrorHandlerFunc: httpx.StrictRequestError, ResponseErrorHandlerFunc: httpx.StrictResponseError})
			me.HandlerWithOptions(s, me.StdHTTPServerOptions{BaseRouter: mux, ErrorHandlerFunc: httpx.StrictRequestError})
		}},
		{"console/subscription.yaml", func(mux httpx.ServeMux) {
			s := console.NewStrictHandlerWithOptions(consoleHandlers{h}, nil, console.StrictHTTPServerOptions{RequestErrorHandlerFunc: httpx.StrictRequestError, ResponseErrorHandlerFunc: httpx.StrictResponseError})
			console.HandlerWithOptions(s, console.StdHTTPServerOptions{BaseRouter: mux, ErrorHandlerFunc: httpx.StrictRequestError})
		}},
		{"tenant/subscription.yaml", func(mux httpx.ServeMux) {
			s := tenant.NewStrictHandlerWithOptions(tenantHandlers{h}, nil, tenant.StrictHTTPServerOptions{RequestErrorHandlerFunc: httpx.StrictRequestError, ResponseErrorHandlerFunc: httpx.StrictResponseError})
			tenant.HandlerWithOptions(s, tenant.StdHTTPServerOptions{BaseRouter: mux, ErrorHandlerFunc: httpx.StrictRequestError})
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

// Problem は subscription のエラーを Problem にする。対応がなければ nil（Orchestration の申込 API も cmd 経由で使う）。
func Problem(err error) *httpx.Problem {
	var pc *c.PlanChangeInvalidError
	switch {
	case errors.Is(err, c.ErrNotFound), errors.Is(err, app.ErrCustomerNotFound):
		return httpx.Error(errcodes.NotFound, "")
	case errors.Is(err, c.ErrTerminal):
		return httpx.Error(errcodes.SubscriptionTerminal, "")
	case errors.Is(err, c.ErrStateConflict):
		return httpx.Error(errcodes.SubscriptionStateConflict, "")
	case errors.Is(err, c.ErrCancellationScheduled):
		return httpx.Error(errcodes.CancellationScheduled, "")
	case errors.Is(err, c.ErrCancellationNotRevocable):
		return httpx.Error(errcodes.CancellationNotRevocable, "")
	case errors.As(err, &pc):
		return httpx.Error(errcodes.PlanChangeNotAllowed, "", httpx.FieldError{Field: "plan_id", Code: pc.Reason})
	case errors.Is(err, c.ErrTrialNotEligible):
		return httpx.Error(errcodes.TrialNotEligible, "")
	case errors.Is(err, c.ErrVersionConflict):
		return httpx.Error(errcodes.Conflict, "")
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
	svc      *app.Service
	codec    *httpx.CursorCodec
	customer customer.Service // console の契約者情報（nil なら付けない）
}

// ---- 共通のリソースの形（§7.1）。生成された型は package ごとに名前が違うので、JSON の形で作ってから変換する ----

type period struct {
	Start sk.Date `json:"start"`
	End   sk.Date `json:"end"`
}

type resource struct {
	ID                    sk.SubscriptionID   `json:"id"`
	TenantID              *sk.TenantID        `json:"tenant_id,omitempty"`
	CustomerID            *string             `json:"customer_id,omitempty"`
	ProductID             sk.ProductID        `json:"product_id"`
	PlanID                sk.PlanID           `json:"plan_id"`
	PriceVersionID        sk.PriceVersionID   `json:"price_version_id"`
	Interval              sk.BillingInterval  `json:"interval"`
	Quantity              int                 `json:"quantity"`
	Status                c.Status            `json:"status"`
	Entitled              bool                `json:"entitled"`
	CurrentPeriod         period              `json:"current_period"`
	NextRenewalDate       *sk.Date            `json:"next_renewal_date"`
	Trial                 *period             `json:"trial"`
	ScheduledPlanChange   any                 `json:"scheduled_plan_change"`
	ScheduledCancellation any                 `json:"scheduled_cancellation"`
	RenewalInProgress     bool                `json:"renewal_in_progress"`
	CancelReason          *c.CancelReason     `json:"cancel_reason"`
	CanceledAt            *time.Time          `json:"canceled_at"`
	CreatedAt             time.Time           `json:"created_at"`
	PaymentMethodID       *sk.PaymentMethodID `json:"payment_method_id,omitempty"`
	AppliedDiscount       *map[string]int     `json:"applied_discount,omitempty"`
	Customer              *customerInfo       `json:"customer,omitempty"`
}

// customerInfo は console に出す契約者情報（事業者に渡してよい範囲。上位 FR-009・business-console FR-010）。
type customerInfo struct {
	ID        string  `json:"id"`
	Name      *string `json:"name"`
	Email     *string `json:"email"`
	Withdrawn bool    `json:"withdrawn"`
}

// withCustomer は console の契約に契約者情報を付ける。取れなければ ID だけにする（一覧の表示を止めない）。
func (h *handlers) withCustomer(ctx context.Context, r *resource, v c.SubscriptionView) {
	info := &customerInfo{ID: v.TenantCustomerID}
	r.Customer = info
	if h.customer == nil {
		return
	}
	p, err := h.customer.GetTenantCustomerProfile(db.ClearScope(ctx), v.TenantID, v.CustomerAccountID)
	if err != nil {
		return
	}
	info.Withdrawn = p.Withdrawn
	if !p.Withdrawn {
		info.Name, info.Email = &p.Name, &p.Email
		if p.Name == "" {
			info.Name = nil
		}
	}
}

type audience int

const (
	forMe audience = iota
	forConsole
	forTenant
)

func toResource(v c.SubscriptionView, a audience) resource {
	r := resource{
		ID: v.ID, ProductID: v.ProductID, PlanID: v.PlanID, PriceVersionID: v.PriceVersionID, Interval: v.Interval, Quantity: v.Quantity,
		Status: v.Status, Entitled: v.Entitled, CurrentPeriod: period{v.CurrentPeriod.Start, v.CurrentPeriod.End}, NextRenewalDate: v.NextRenewalDate,
		RenewalInProgress: v.RenewalInProgress, CancelReason: v.CancelReason, CanceledAt: v.CanceledAt, CreatedAt: v.CreatedAt,
	}
	if v.TrialStart != nil && v.TrialEnd != nil {
		r.Trial = &period{*v.TrialStart, *v.TrialEnd}
	}
	if pc := v.PendingPlanChange; pc != nil {
		r.ScheduledPlanChange = map[string]any{"plan_id": pc.PlanID, "effective_date": pc.EffectiveDate}
	}
	if cn := v.Cancellation; cn != nil {
		r.ScheduledCancellation = map[string]any{"effective_date": cn.EffectiveDate, "revocable": cn.Source == c.SourceCustomer, "source": cn.Source}
	}
	var disc *map[string]int
	if d := v.AppliedDiscount; d != nil {
		m := map[string]int{"remaining_cycles": d.RemainingCycles}
		disc = &m
	}
	switch a {
	case forMe:
		t, pm := v.TenantID, v.PaymentMethodID
		r.TenantID, r.PaymentMethodID = &t, &pm
		if disc == nil {
			var none map[string]int
			disc = &none // me では applied_discount を必ず返す（null を含む）
		}
		r.AppliedDiscount = disc
	case forConsole:
		cus := v.TenantCustomerID
		r.CustomerID, r.AppliedDiscount = &cus, disc
	case forTenant:
		cus := v.TenantCustomerID
		r.CustomerID = &cus
	}
	return r
}

// convert は JSON を通して生成された型に写す（形は OpenAPI で同じ）。
func convert[T any](v any) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("subscription: convert response: %w", err)
	}
	return out, nil
}

func pageLimit(l *int) int {
	if l == nil {
		return 20
	}
	return *l
}

func (h *handlers) cursor(s *string) (*postgres.Cursor, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	var c postgres.Cursor
	if err := h.codec.Decode(*s, &c); err != nil {
		return nil, httpx.Error(errcodes.ValidationFailed, "starting_after is invalid", httpx.FieldError{Field: "starting_after", Code: "invalid"})
	}
	return &c, nil
}

type listResult struct {
	Data       []resource `json:"data"`
	HasMore    bool       `json:"has_more"`
	NextCursor *string    `json:"next_cursor"`
}

func (h *handlers) list(ctx context.Context, f postgres.Filter, after *string, limit *int, a audience) (listResult, error) {
	cur, err := h.cursor(after)
	if err != nil {
		return listResult{}, err
	}
	items, more, err := h.svc.List(ctx, f, cur, pageLimit(limit))
	if err != nil {
		return listResult{}, problem(err)
	}
	out := listResult{Data: make([]resource, len(items)), HasMore: more}
	for i, v := range items {
		out.Data[i] = toResource(v, a)
	}
	if more {
		last := items[len(items)-1]
		next := h.codec.Encode(postgres.Cursor{CreatedAt: last.CreatedAt, ID: last.ID.UUID()})
		out.NextCursor = &next
	}
	return out, nil
}

func statuses[T ~string](s *[]T) []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(*s))
	for i, v := range *s {
		out[i] = string(v)
	}
	return out
}

func subID(u uuid.UUID) sk.SubscriptionID { return sk.IDFromUUID[sk.SubscriptionTag](u) }

// ---- me ----

type meHandlers struct{ *handlers }

var _ me.StrictServerInterface = meHandlers{}

// meJSON は契約者に返す契約（表示用の値を付ける。取れなければ display は null）。
func (h meHandlers) meJSON(ctx context.Context, v c.SubscriptionView) map[string]any {
	r := toResource(v, forMe)
	out, _ := asMap(r)
	var d any
	if x, ok := h.svc.Displays(ctx, []c.SubscriptionView{v})[v.ID]; ok {
		d = x
	}
	out["display"] = d
	return out
}

func meResource[T any](h meHandlers, ctx context.Context, v c.SubscriptionView, err error) (T, error) {
	var zero T
	if err != nil {
		return zero, problem(err)
	}
	return convert[T](h.meJSON(ctx, v))
}

func asMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(b, &m)
}

func (h meHandlers) MeListSubscriptions(ctx context.Context, req me.MeListSubscriptionsRequestObject) (me.MeListSubscriptionsResponseObject, error) {
	cur, err := h.cursor(req.Params.StartingAfter)
	if err != nil {
		return nil, err
	}
	items, more, err := h.svc.List(ctx, postgres.Filter{Statuses: statuses(req.Params.Status)}, cur, pageLimit(req.Params.Limit))
	if err != nil {
		return nil, problem(err)
	}
	displays := h.svc.Displays(ctx, items)
	data := make([]map[string]any, len(items))
	for i, v := range items {
		m, err := asMap(toResource(v, forMe))
		if err != nil {
			return nil, err
		}
		var d any
		if x, ok := displays[v.ID]; ok {
			d = x
		}
		m["display"] = d
		data[i] = m
	}
	out := map[string]any{"data": data, "has_more": more, "next_cursor": nil}
	if more {
		last := items[len(items)-1]
		out["next_cursor"] = h.codec.Encode(postgres.Cursor{CreatedAt: last.CreatedAt, ID: last.ID.UUID()})
	}
	return convert[me.MeListSubscriptions200JSONResponse](out)
}

func (h meHandlers) MeGetSubscription(ctx context.Context, req me.MeGetSubscriptionRequestObject) (me.MeGetSubscriptionResponseObject, error) {
	v, err := h.svc.Find(ctx, subID(req.SubscriptionId))
	return meResource[me.MeGetSubscription200JSONResponse](h, ctx, v, err)
}

func (h meHandlers) MeListPlanChangeOptions(ctx context.Context, req me.MeListPlanChangeOptionsRequestObject) (me.MeListPlanChangeOptionsResponseObject, error) {
	opts, err := h.svc.PlanChangeOptions(ctx, subID(req.SubscriptionId))
	if err != nil {
		return nil, problem(err)
	}
	data := make([]map[string]any, len(opts))
	for i, o := range opts {
		data[i] = map[string]any{"plan_id": o.PlanID, "name": o.Name, "billing_interval": o.Interval, "amount": o.Amount.Int64(), "effective_date": o.EffectiveDate}
	}
	return convert[me.MeListPlanChangeOptions200JSONResponse](map[string]any{"data": data})
}

func (h meHandlers) MeSchedulePlanChange(ctx context.Context, req me.MeSchedulePlanChangeRequestObject) (me.MeSchedulePlanChangeResponseObject, error) {
	v, err := h.svc.SchedulePlanChange(ctx, subID(req.SubscriptionId), sk.IDFromUUID[sk.PlanTag](req.Body.PlanId))
	return meResource[me.MeSchedulePlanChange200JSONResponse](h, ctx, v, err)
}

func (h meHandlers) MeRevokePlanChange(ctx context.Context, req me.MeRevokePlanChangeRequestObject) (me.MeRevokePlanChangeResponseObject, error) {
	v, err := h.svc.RevokePlanChange(ctx, subID(req.SubscriptionId))
	return meResource[me.MeRevokePlanChange200JSONResponse](h, ctx, v, err)
}

func (h meHandlers) MeScheduleCancellation(ctx context.Context, req me.MeScheduleCancellationRequestObject) (me.MeScheduleCancellationResponseObject, error) {
	v, err := h.svc.ScheduleCancellation(ctx, subID(req.SubscriptionId))
	return meResource[me.MeScheduleCancellation200JSONResponse](h, ctx, v, err)
}

func (h meHandlers) MeRevokeCancellation(ctx context.Context, req me.MeRevokeCancellationRequestObject) (me.MeRevokeCancellationResponseObject, error) {
	v, err := h.svc.RevokeCancellation(ctx, subID(req.SubscriptionId))
	return meResource[me.MeRevokeCancellation200JSONResponse](h, ctx, v, err)
}

// ---- console ----

type consoleHandlers struct{ *handlers }

var _ console.StrictServerInterface = consoleHandlers{}

func (h consoleHandlers) ConsoleListSubscriptions(ctx context.Context, req console.ConsoleListSubscriptionsRequestObject) (console.ConsoleListSubscriptionsResponseObject, error) {
	f := postgres.Filter{Statuses: statuses(req.Params.Status), TenantCustomerID: req.Params.CustomerId}
	if req.Params.PlanId != nil {
		u := *req.Params.PlanId
		f.PlanID = &u
	}
	if e := req.Params.Email; e != nil && *e != "" && h.customer != nil {
		cus, err := h.customer.FindTenantCustomerIDByEmail(db.ClearScope(ctx), httpx.TenantID(ctx), *e)
		if errors.Is(err, customer.ErrLinkNotFound) {
			return console.ConsoleListSubscriptions200JSONResponse{Data: []console.Subscription{}}, nil // 該当なし（存在を区別しない）
		}
		if err != nil {
			return nil, err
		}
		s := string(cus)
		if f.TenantCustomerID != nil && *f.TenantCustomerID != s {
			return console.ConsoleListSubscriptions200JSONResponse{Data: []console.Subscription{}}, nil
		}
		f.TenantCustomerID = &s
	}
	cur, err := h.cursor(req.Params.StartingAfter)
	if err != nil {
		return nil, err
	}
	items, more, err := h.svc.List(ctx, f, cur, pageLimit(req.Params.Limit))
	if err != nil {
		return nil, problem(err)
	}
	out := listResult{Data: make([]resource, len(items)), HasMore: more}
	for i, v := range items {
		out.Data[i] = toResource(v, forConsole)
		h.withCustomer(ctx, &out.Data[i], v)
	}
	if more {
		last := items[len(items)-1]
		next := h.codec.Encode(postgres.Cursor{CreatedAt: last.CreatedAt, ID: last.ID.UUID()})
		out.NextCursor = &next
	}
	return convert[console.ConsoleListSubscriptions200JSONResponse](out)
}

func (h consoleHandlers) ConsoleGetSubscription(ctx context.Context, req console.ConsoleGetSubscriptionRequestObject) (console.ConsoleGetSubscriptionResponseObject, error) {
	v, err := h.svc.Find(ctx, subID(req.SubscriptionId))
	if err != nil {
		return nil, problem(err)
	}
	r := toResource(v, forConsole)
	h.withCustomer(ctx, &r, v)
	return convert[console.ConsoleGetSubscription200JSONResponse](r)
}

// ---- tenant ----

type tenantHandlers struct{ *handlers }

var _ tenant.StrictServerInterface = tenantHandlers{}

func (h tenantHandlers) TenantListSubscriptions(ctx context.Context, req tenant.TenantListSubscriptionsRequestObject) (tenant.TenantListSubscriptionsResponseObject, error) {
	res, err := h.list(ctx, postgres.Filter{Statuses: statuses(req.Params.Status), TenantCustomerID: req.Params.CustomerId}, req.Params.StartingAfter, req.Params.Limit, forTenant)
	if err != nil {
		return nil, err
	}
	return convert[tenant.TenantListSubscriptions200JSONResponse](res)
}

func (h tenantHandlers) TenantGetSubscription(ctx context.Context, req tenant.TenantGetSubscriptionRequestObject) (tenant.TenantGetSubscriptionResponseObject, error) {
	v, err := h.svc.Find(ctx, subID(req.SubscriptionId))
	if err != nil {
		return nil, problem(err)
	}
	return convert[tenant.TenantGetSubscription200JSONResponse](toResource(v, forTenant))
}

func (h tenantHandlers) TenantGetEntitlements(ctx context.Context, req tenant.TenantGetEntitlementsRequestObject) (tenant.TenantGetEntitlementsResponseObject, error) {
	entitled, products, err := h.svc.Entitlements(ctx, req.CustomerId, req.Params.ProductId)
	if err != nil {
		return nil, problem(err)
	}
	type product struct {
		ProductID       sk.ProductID        `json:"product_id"`
		Entitled        bool                `json:"entitled"`
		SubscriptionIDs []sk.SubscriptionID `json:"subscription_ids"`
	}
	ps := make([]product, len(products))
	for i, p := range products {
		ps[i] = product{ProductID: p.ProductID, Entitled: p.Entitled, SubscriptionIDs: p.SubscriptionIDs}
	}
	return convert[tenant.TenantGetEntitlements200JSONResponse](map[string]any{"customer_id": req.CustomerId, "entitled": entitled, "products": ps})
}
