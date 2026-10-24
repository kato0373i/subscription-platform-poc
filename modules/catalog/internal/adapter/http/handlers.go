// Package http は catalog モジュールの console API と申込ページ用の公開 API（oapi-codegen の strict server を実装する）。
package http

import (
	"context"
	"errors"
	nethttp "net/http"

	"github.com/google/uuid"

	"subscription-platform/api/openapi"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/httpx/errcodes"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/catalog/contracts"
	"subscription-platform/modules/catalog/internal/adapter/http/gen/checkout"
	"subscription-platform/modules/catalog/internal/adapter/http/gen/console"
	"subscription-platform/modules/catalog/internal/adapter/postgres"
	"subscription-platform/modules/catalog/internal/app"
	"subscription-platform/modules/catalog/internal/domain"
	"subscription-platform/modules/catalog/internal/domain/pricing"
)

// Register は console・checkout のルートを登録する。
func Register(r *httpx.Router, svc *app.Service, codec *httpx.CursorCodec) error {
	consoleSpec, err := openapi.Load("console/catalog.yaml")
	if err != nil {
		return err
	}
	checkoutSpec, err := openapi.Load("checkout/catalog.yaml")
	if err != nil {
		return err
	}
	ch := console.NewStrictHandlerWithOptions(&consoleHandlers{svc: svc, codec: codec}, nil,
		console.StrictHTTPServerOptions{RequestErrorHandlerFunc: httpx.StrictRequestError, ResponseErrorHandlerFunc: httpx.StrictResponseError})
	if err := r.Mount(consoleSpec, func(m httpx.ServeMux) {
		console.HandlerWithOptions(ch, console.StdHTTPServerOptions{BaseRouter: m, ErrorHandlerFunc: httpx.StrictRequestError})
	}); err != nil {
		return err
	}
	co := checkout.NewStrictHandlerWithOptions(&checkoutHandlers{svc: svc}, nil,
		checkout.StrictHTTPServerOptions{RequestErrorHandlerFunc: httpx.StrictRequestError, ResponseErrorHandlerFunc: httpx.StrictResponseError})
	return r.Mount(checkoutSpec, func(m httpx.ServeMux) {
		checkout.HandlerWithOptions(co, checkout.StdHTTPServerOptions{BaseRouter: m, ErrorHandlerFunc: httpx.StrictRequestError})
	})
}

// Problem は catalog のエラーを Problem にする。対応がなければ nil（Orchestration の申込 API も cmd 経由で使える）。
func Problem(err error) *httpx.Problem {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		return httpx.Error(errcodes.ValidationFailed, ve.Detail, httpx.FieldError{Field: ve.Field, Code: ve.Code})
	case errors.Is(err, app.ErrNotFound), errors.Is(err, contracts.ErrPlanNotFound), errors.Is(err, contracts.ErrPriceVersionNotFound):
		return httpx.Error(errcodes.NotFound, "")
	case errors.Is(err, postgres.ErrVersionConflict), errors.Is(err, domain.ErrInvalidPlanTransition):
		return httpx.Error(errcodes.Conflict, "")
	case errors.Is(err, domain.ErrTenantNotActive):
		return httpx.Error(errcodes.TenantNotActive, "審査完了後に公開できます")
	case errors.Is(err, domain.ErrPriceUnchanged):
		return httpx.Error(errcodes.PriceUnchanged, "")
	case errors.Is(err, postgres.ErrCodeTaken):
		return httpx.Error(errcodes.CouponCodeTaken, "")
	case errors.Is(err, contracts.ErrPlanUnavailable):
		return httpx.Error(errcodes.PlanUnavailable, "現在お申し込みいただけません")
	case errors.Is(err, contracts.ErrCouponNotFound):
		return httpx.Error(errcodes.CouponNotFound, "")
	case errors.Is(err, contracts.ErrCouponExpired):
		return httpx.Error(errcodes.CouponExpired, "")
	case errors.Is(err, contracts.ErrCouponNotApplicable):
		return httpx.Error(errcodes.CouponNotApplicable, "")
	case errors.Is(err, contracts.ErrCouponAlreadyUsed):
		return httpx.Error(errcodes.CouponAlreadyUsed, "")
	case errors.Is(err, contracts.ErrCouponExhausted):
		return httpx.Error(errcodes.CouponExhausted, "")
	}
	return nil
}

func problem(err error) error {
	if p := Problem(err); p != nil {
		return p
	}
	return err
}

func pageLimit(l *int) int {
	if l == nil {
		return 20
	}
	return *l
}

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

func nextCursor(codec *httpx.CursorCodec, more bool, key any) *string {
	if !more {
		return nil
	}
	c := codec.Encode(key)
	return &c
}

// ---- console ----

type consoleHandlers struct {
	svc   *app.Service
	codec *httpx.CursorCodec
}

var _ console.StrictServerInterface = (*consoleHandlers)(nil)

func toProduct(s domain.ProductState) console.Product {
	return console.Product{Id: s.ID.UUID(), Name: s.Name, Description: s.Description, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
}

func toPrice(v domain.PriceVersion) console.PriceVersion {
	return console.PriceVersion{PriceVersionId: v.ID.UUID(), VersionNo: v.VersionNo, Amount: v.AmountYen(), TaxRate: v.TaxRate.String(), EffectiveFrom: v.EffectiveFrom}
}

func toPlan(s domain.PlanState) console.Plan {
	return console.Plan{
		Id: s.ID.UUID(), ProductId: s.ProductID.UUID(), Name: s.Name, Description: s.Description, BillingInterval: console.PlanBillingInterval(s.Interval),
		TrialDays: s.TrialDays, PricingScheme: console.PlanPricingScheme(s.Scheme), Status: console.PlanStatus(s.Status), CurrentPrice: toPrice(s.CurrentPrice),
		PublishedAt: s.PublishedAt, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}

func toCoupon(s domain.CouponState) console.Coupon {
	ids := make([]uuid.UUID, len(s.PlanIDs))
	for i, id := range s.PlanIDs {
		ids[i] = id.UUID()
	}
	return console.Coupon{
		Id: s.ID.UUID(), Code: s.Code, Name: s.Name,
		Discount:  console.Discount{Type: console.DiscountType(s.Discount.Type), Value: s.Discount.Value, DurationCycles: s.DurationCycles},
		AppliesTo: console.CouponAppliesTo(s.AppliesTo), PlanIds: ids, ExpiresAt: s.ExpiresAt, MaxRedemptions: s.MaxRedemptions,
		RedeemedCount: s.RedeemedCount, Status: console.CouponStatus(s.Status), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}

func (h *consoleHandlers) ConsoleCreateProduct(ctx context.Context, req console.ConsoleCreateProductRequestObject) (console.ConsoleCreateProductResponseObject, error) {
	desc := ""
	if req.Body.Description != nil {
		desc = *req.Body.Description
	}
	st, err := h.svc.CreateProduct(ctx, httpx.TenantID(ctx), req.Body.Name, desc)
	if err != nil {
		return nil, problem(err)
	}
	return console.ConsoleCreateProduct201JSONResponse(toProduct(st)), nil
}

func (h *consoleHandlers) ConsoleListProducts(ctx context.Context, req console.ConsoleListProductsRequestObject) (console.ConsoleListProductsResponseObject, error) {
	after, err := decodeCursor[postgres.Cursor](h.codec, req.Params.StartingAfter)
	if err != nil {
		return nil, err
	}
	list, more, err := h.svc.ListProducts(ctx, after, pageLimit(req.Params.Limit))
	if err != nil {
		return nil, problem(err)
	}
	resp := console.ConsoleListProducts200JSONResponse{Data: make([]console.Product, len(list)), HasMore: more}
	for i, s := range list {
		resp.Data[i] = toProduct(s)
	}
	if more {
		last := list[len(list)-1]
		resp.NextCursor = nextCursor(h.codec, more, postgres.Cursor{CreatedAt: last.CreatedAt, ID: last.ID.UUID()})
	}
	return resp, nil
}

func (h *consoleHandlers) ConsoleGetProduct(ctx context.Context, req console.ConsoleGetProductRequestObject) (console.ConsoleGetProductResponseObject, error) {
	st, err := h.svc.GetProduct(ctx, sk.IDFromUUID[contracts.ProductTag](req.ProductId))
	if err != nil {
		return nil, problem(err)
	}
	return console.ConsoleGetProduct200JSONResponse(toProduct(st)), nil
}

func (h *consoleHandlers) ConsoleUpdateProduct(ctx context.Context, req console.ConsoleUpdateProductRequestObject) (console.ConsoleUpdateProductResponseObject, error) {
	st, err := h.svc.UpdateProduct(ctx, sk.IDFromUUID[contracts.ProductTag](req.ProductId), req.Body.Name, req.Body.Description)
	if err != nil {
		return nil, problem(err)
	}
	return console.ConsoleUpdateProduct200JSONResponse(toProduct(st)), nil
}

func (h *consoleHandlers) ConsoleCreatePlan(ctx context.Context, req console.ConsoleCreatePlanRequestObject) (console.ConsoleCreatePlanResponseObject, error) {
	b := req.Body
	in := domain.PlanInput{
		TenantID: httpx.TenantID(ctx), ProductID: sk.IDFromUUID[contracts.ProductTag](b.ProductId), Name: b.Name,
		Interval: sk.BillingInterval(b.BillingInterval), TrialDays: b.TrialDays, Scheme: pricing.KindFixed, Amount: b.Amount,
	}
	if b.Description != nil {
		in.Description = *b.Description
	}
	if b.PricingScheme != nil {
		in.Scheme = pricing.Kind(*b.PricingScheme)
	}
	st, err := h.svc.CreatePlan(ctx, in)
	if err != nil {
		return nil, problem(err)
	}
	return console.ConsoleCreatePlan201JSONResponse(toPlan(st)), nil
}

func (h *consoleHandlers) ConsoleListPlans(ctx context.Context, req console.ConsoleListPlansRequestObject) (console.ConsoleListPlansResponseObject, error) {
	after, err := decodeCursor[postgres.Cursor](h.codec, req.Params.StartingAfter)
	if err != nil {
		return nil, err
	}
	var f postgres.PlanFilter
	if req.Params.ProductId != nil {
		u := *req.Params.ProductId
		f.ProductID = &u
	}
	if req.Params.Status != nil {
		s := string(*req.Params.Status)
		f.Status = &s
	}
	list, more, err := h.svc.ListPlans(ctx, f, after, pageLimit(req.Params.Limit))
	if err != nil {
		return nil, problem(err)
	}
	resp := console.ConsoleListPlans200JSONResponse{Data: make([]console.Plan, len(list)), HasMore: more}
	for i, s := range list {
		resp.Data[i] = toPlan(s)
	}
	if more {
		last := list[len(list)-1]
		resp.NextCursor = nextCursor(h.codec, more, postgres.Cursor{CreatedAt: last.CreatedAt, ID: last.ID.UUID()})
	}
	return resp, nil
}

func (h *consoleHandlers) ConsoleGetPlan(ctx context.Context, req console.ConsoleGetPlanRequestObject) (console.ConsoleGetPlanResponseObject, error) {
	st, err := h.svc.GetPlan(ctx, sk.IDFromUUID[contracts.PlanTag](req.PlanId))
	if err != nil {
		return nil, problem(err)
	}
	return console.ConsoleGetPlan200JSONResponse(toPlan(st)), nil
}

func (h *consoleHandlers) ConsoleUpdatePlan(ctx context.Context, req console.ConsoleUpdatePlanRequestObject) (console.ConsoleUpdatePlanResponseObject, error) {
	b := req.Body
	st, err := h.svc.UpdatePlan(ctx, sk.IDFromUUID[contracts.PlanTag](req.PlanId), b.Name, b.Description, b.TrialDays)
	if err != nil {
		return nil, problem(err)
	}
	return console.ConsoleUpdatePlan200JSONResponse(toPlan(st)), nil
}

func (h *consoleHandlers) ConsoleAddPriceVersion(ctx context.Context, req console.ConsoleAddPriceVersionRequestObject) (console.ConsoleAddPriceVersionResponseObject, error) {
	pv, err := h.svc.AddPriceVersion(ctx, sk.IDFromUUID[contracts.PlanTag](req.PlanId), req.Body.Amount)
	if err != nil {
		return nil, problem(err)
	}
	return console.ConsoleAddPriceVersion201JSONResponse(toPrice(pv)), nil
}

// priceCursor は価格版の一覧の位置（version_no の降順）。
type priceCursor struct {
	VersionNo int `json:"v"`
}

func (h *consoleHandlers) ConsoleListPriceVersions(ctx context.Context, req console.ConsoleListPriceVersionsRequestObject) (console.ConsoleListPriceVersionsResponseObject, error) {
	after, err := decodeCursor[priceCursor](h.codec, req.Params.StartingAfter)
	if err != nil {
		return nil, err
	}
	var before *int
	if after != nil {
		before = &after.VersionNo
	}
	list, more, err := h.svc.ListPriceVersions(ctx, sk.IDFromUUID[contracts.PlanTag](req.PlanId), before, pageLimit(req.Params.Limit))
	if err != nil {
		return nil, problem(err)
	}
	resp := console.ConsoleListPriceVersions200JSONResponse{Data: make([]console.PriceVersion, len(list)), HasMore: more}
	for i, v := range list {
		resp.Data[i] = toPrice(v)
	}
	if more {
		resp.NextCursor = nextCursor(h.codec, more, priceCursor{VersionNo: list[len(list)-1].VersionNo})
	}
	return resp, nil
}

func (h *consoleHandlers) ConsolePublishPlan(ctx context.Context, req console.ConsolePublishPlanRequestObject) (console.ConsolePublishPlanResponseObject, error) {
	st, err := h.svc.PublishPlan(ctx, httpx.TenantID(ctx), sk.IDFromUUID[contracts.PlanTag](req.PlanId))
	if err != nil {
		return nil, problem(err)
	}
	return console.ConsolePublishPlan200JSONResponse(toPlan(st)), nil
}

func (h *consoleHandlers) ConsoleUnpublishPlan(ctx context.Context, req console.ConsoleUnpublishPlanRequestObject) (console.ConsoleUnpublishPlanResponseObject, error) {
	st, err := h.svc.UnpublishPlan(ctx, sk.IDFromUUID[contracts.PlanTag](req.PlanId))
	if err != nil {
		return nil, problem(err)
	}
	return console.ConsoleUnpublishPlan200JSONResponse(toPlan(st)), nil
}

func (h *consoleHandlers) ConsoleCreateCoupon(ctx context.Context, req console.ConsoleCreateCouponRequestObject) (console.ConsoleCreateCouponResponseObject, error) {
	b := req.Body
	in := domain.CouponInput{
		TenantID: httpx.TenantID(ctx), Code: b.Code, Name: b.Name,
		Discount:       sk.Discount{Type: sk.DiscountType(b.Discount.Type), Value: b.Discount.Value},
		DurationCycles: b.Discount.DurationCycles, AppliesTo: domain.AppliesTo(b.AppliesTo), ExpiresAt: b.ExpiresAt, MaxRedemptions: b.MaxRedemptions,
	}
	if b.ExpiresAt != nil {
		t := b.ExpiresAt.UTC()
		in.ExpiresAt = &t
	}
	if b.PlanIds != nil {
		for _, id := range *b.PlanIds {
			in.PlanIDs = append(in.PlanIDs, sk.IDFromUUID[contracts.PlanTag](id))
		}
	}
	st, err := h.svc.CreateCoupon(ctx, in)
	if err != nil {
		return nil, problem(err)
	}
	return console.ConsoleCreateCoupon201JSONResponse(toCoupon(st)), nil
}

func (h *consoleHandlers) ConsoleListCoupons(ctx context.Context, req console.ConsoleListCouponsRequestObject) (console.ConsoleListCouponsResponseObject, error) {
	after, err := decodeCursor[postgres.Cursor](h.codec, req.Params.StartingAfter)
	if err != nil {
		return nil, err
	}
	var status *string
	if req.Params.Status != nil {
		s := string(*req.Params.Status)
		status = &s
	}
	list, more, err := h.svc.ListCoupons(ctx, status, after, pageLimit(req.Params.Limit))
	if err != nil {
		return nil, problem(err)
	}
	resp := console.ConsoleListCoupons200JSONResponse{Data: make([]console.Coupon, len(list)), HasMore: more}
	for i, s := range list {
		resp.Data[i] = toCoupon(s)
	}
	if more {
		last := list[len(list)-1]
		resp.NextCursor = nextCursor(h.codec, more, postgres.Cursor{CreatedAt: last.CreatedAt, ID: last.ID.UUID()})
	}
	return resp, nil
}

func (h *consoleHandlers) ConsoleGetCoupon(ctx context.Context, req console.ConsoleGetCouponRequestObject) (console.ConsoleGetCouponResponseObject, error) {
	st, err := h.svc.GetCoupon(ctx, sk.IDFromUUID[contracts.CouponTag](req.CouponId))
	if err != nil {
		return nil, problem(err)
	}
	return console.ConsoleGetCoupon200JSONResponse(toCoupon(st)), nil
}

func (h *consoleHandlers) ConsoleUpdateCoupon(ctx context.Context, req console.ConsoleUpdateCouponRequestObject) (console.ConsoleUpdateCouponResponseObject, error) {
	b := req.Body
	st, err := h.svc.UpdateCoupon(ctx, sk.IDFromUUID[contracts.CouponTag](req.CouponId), b.Name, b.Status != nil)
	if err != nil {
		return nil, problem(err)
	}
	return console.ConsoleUpdateCoupon200JSONResponse(toCoupon(st)), nil
}

// ---- checkout ----

type checkoutHandlers struct{ svc *app.Service }

var _ checkout.StrictServerInterface = (*checkoutHandlers)(nil)

// cachePublic は申込ページのプランを 60 秒キャッシュさせる。
type cachePublic struct {
	checkout.CheckoutGetPlan200JSONResponse
}

func (c cachePublic) VisitCheckoutGetPlanResponse(w nethttp.ResponseWriter) error {
	w.Header().Set("Cache-Control", "public, max-age=60")
	return c.CheckoutGetPlan200JSONResponse.VisitCheckoutGetPlanResponse(w)
}

func (h *checkoutHandlers) CheckoutGetPlan(ctx context.Context, req checkout.CheckoutGetPlanRequestObject) (checkout.CheckoutGetPlanResponseObject, error) {
	cp, _, err := h.svc.GetCheckoutPlan(ctx, sk.IDFromUUID[contracts.PlanTag](req.PlanId))
	if err != nil {
		return nil, problem(err)
	}
	p := cp.Plan
	out := checkout.CheckoutGetPlan200JSONResponse{
		PlanId: p.ID.UUID(), Name: p.Name, Description: p.Description, BillingInterval: checkout.CheckoutPlanBillingInterval(p.Interval),
		TrialDays: p.TrialDays, Amount: p.CurrentPrice.AmountYen(), TaxRate: p.CurrentPrice.TaxRate.String(), PriceVersionId: p.CurrentPrice.ID.UUID(),
	}
	out.Product.Id, out.Product.Name = p.ProductID.UUID(), cp.ProductName
	return cachePublic{out}, nil
}

// noStore はクーポンのプレビューをキャッシュさせない（契約者ごとに結果が変わる）。
type noStore struct {
	checkout.CheckoutPreviewCoupon200JSONResponse
}

func (n noStore) VisitCheckoutPreviewCouponResponse(w nethttp.ResponseWriter) error {
	w.Header().Set("Cache-Control", "no-store")
	return n.CheckoutPreviewCoupon200JSONResponse.VisitCheckoutPreviewCouponResponse(w)
}

func (h *checkoutHandlers) CheckoutPreviewCoupon(ctx context.Context, req checkout.CheckoutPreviewCouponRequestObject) (checkout.CheckoutPreviewCouponResponseObject, error) {
	var customer *sk.CustomerAccountID
	if p, ok := httpx.PrincipalFrom(ctx); ok && p.CustomerAccountID != nil {
		customer = p.CustomerAccountID
	}
	pv, err := h.svc.PreviewCoupon(ctx, sk.IDFromUUID[contracts.PlanTag](req.PlanId), req.Code, customer)
	if err != nil {
		return nil, problem(err)
	}
	var out checkout.CheckoutPreviewCoupon200JSONResponse
	c := pv.Coupon
	out.Code = c.Code
	out.Discount.Type, out.Discount.Value, out.Discount.DurationCycles = checkout.CouponPreviewDiscountType(c.Discount.Type), c.Discount.Value, c.DurationCycles
	out.Preview.ListAmount, out.Preview.DiscountAmount, out.Preview.FirstAmount = pv.Preview.ListAmount, pv.Preview.DiscountAmount, pv.Preview.FirstAmount
	out.Preview.DiscountedCycles, out.Preview.AppliesFrom = pv.Preview.DiscountedCycles, checkout.FirstCharge
	return noStore{out}, nil
}
