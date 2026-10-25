// Package http は Orchestration の API（申込・支払方法の変更と削除・退会・ops の課金バッチと一括解約。orchestration.md §7）。
package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	nethttp "net/http"

	"github.com/google/uuid"

	"subscription-platform/api/openapi"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/httpx/errcodes"
	sk "subscription-platform/internal/sharedkernel"
	catalog "subscription-platform/modules/catalog/contracts"
	"subscription-platform/modules/orchestration/internal/adapter/http/gen/me"
	"subscription-platform/modules/orchestration/internal/adapter/http/gen/ops"
	"subscription-platform/modules/orchestration/internal/app"
	payments "subscription-platform/modules/payments/contracts"
	subscription "subscription-platform/modules/subscription/contracts"
)

// Register は me・ops の API を登録する。
func Register(r *httpx.Router, svc *app.Service) error {
	h := &handlers{svc: svc}
	reqErr, resErr := httpx.StrictRequestError, httpx.StrictResponseError
	for _, m := range []struct {
		spec     string
		register func(httpx.ServeMux)
	}{
		{"me/orchestration.yaml", func(mux httpx.ServeMux) {
			s := me.NewStrictHandlerWithOptions(meHandlers{h}, nil, me.StrictHTTPServerOptions{RequestErrorHandlerFunc: reqErr, ResponseErrorHandlerFunc: resErr})
			me.HandlerWithOptions(s, me.StdHTTPServerOptions{BaseRouter: mux, ErrorHandlerFunc: reqErr, Middlewares: []me.MiddlewareFunc{withRequest}})
		}},
		{"ops/orchestration.yaml", func(mux httpx.ServeMux) {
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

type reqKey struct{}

type reqInfo struct {
	key  string
	hash []byte
}

// withRequest は Idempotency-Key と本文の SHA-256 を context に入れる（サガの照合。§4.1 手順 0）。
func withRequest(next nethttp.Handler) nethttp.Handler {
	return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		info := reqInfo{key: r.Header.Get("Idempotency-Key")}
		if r.Body != nil && r.Method == nethttp.MethodPost {
			b, err := io.ReadAll(r.Body)
			if err == nil {
				sum := sha256.Sum256(b)
				info.hash = sum[:]
				r.Body = io.NopCloser(bytes.NewReader(b))
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), reqKey{}, info)))
	})
}

func requestInfo(ctx context.Context) reqInfo {
	info, _ := ctx.Value(reqKey{}).(reqInfo)
	return info
}

func unprocessable(code errcodes.Code, detail string) *httpx.Problem {
	p := httpx.Error(code, detail)
	p.Status = nethttp.StatusUnprocessableEntity // 申込では 422（§7.1）
	return p
}

// Problem は Orchestration のエラーを Problem にする。対応がなければ nil。
func Problem(err error) *httpx.Problem {
	var declined *app.PaymentDeclinedError
	var stored *app.StoredReplyError
	switch {
	case errors.As(err, &declined):
		p := httpx.Error(errcodes.PaymentDeclined, "")
		p.DeclineCategory = declined.Category
		return p
	case errors.As(err, &stored):
		return unprocessable(errcodes.Code(stored.Code), "")
	case errors.Is(err, app.ErrTenantNotActive):
		return unprocessable(errcodes.TenantNotActive, "")
	case errors.Is(err, app.ErrPlanUnavailable):
		return unprocessable(errcodes.PlanUnavailable, "")
	case errors.Is(err, app.ErrPaymentMethodUnavailable):
		return httpx.Error(errcodes.PaymentMethodUnavailable, "")
	case errors.Is(err, app.ErrPaymentMethodReentry):
		return httpx.Error(errcodes.PaymentMethodReentryRequired, "")
	case errors.Is(err, app.ErrIdempotencyKeyReused):
		return httpx.Error(errcodes.IdempotencyKeyReused, "")
	case errors.Is(err, subscription.ErrTrialNotEligible):
		return httpx.Error(errcodes.TrialNotEligible, "")
	case errors.Is(err, catalog.ErrCouponExpired):
		return httpx.Error(errcodes.CouponExpired, "")
	case errors.Is(err, catalog.ErrCouponExhausted):
		return httpx.Error(errcodes.CouponExhausted, "")
	case errors.Is(err, catalog.ErrCouponAlreadyUsed):
		return httpx.Error(errcodes.CouponAlreadyUsed, "")
	case errors.Is(err, catalog.ErrCouponNotApplicable):
		return httpx.Error(errcodes.CouponNotApplicable, "")
	case errors.Is(err, catalog.ErrCouponNotFound):
		return httpx.Error(errcodes.CouponNotFound, "")
	case errors.Is(err, payments.ErrProviderUnavailable):
		p := httpx.Error(errcodes.ProviderUnavailable, "")
		p.RetryAfter = 30
		return p
	case errors.Is(err, app.ErrNotFound):
		return httpx.Error(errcodes.NotFound, "")
	case errors.Is(err, app.ErrSubscriptionNotModifiable):
		return httpx.Error(errcodes.SubscriptionNotModifiable, "")
	case errors.Is(err, app.ErrPaymentMethodInUse):
		return httpx.Error(errcodes.PaymentMethodInUse, "")
	case errors.Is(err, app.ErrHasOpenSubscriptions):
		return httpx.Error(errcodes.HasOpenSubscriptions, "")
	case errors.Is(err, app.ErrBillingRunInProgress):
		return httpx.Error(errcodes.BillingRunInProgress, "")
	case errors.Is(err, app.ErrConflict):
		return httpx.Error(errcodes.Conflict, "")
	case errors.Is(err, app.ErrInvalidDate):
		return httpx.Error(errcodes.ValidationFailed, "run_date must not be in the future", httpx.FieldError{Field: "run_date", Code: "future_date"})
	}
	return nil
}

func problem(err error) error {
	if p := Problem(err); p != nil {
		return p
	}
	return err
}

type handlers struct{ svc *app.Service }

func convert[T any](v any) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("orchestration: convert response: %w", err)
	}
	return out, nil
}

// ---- me ----

type meHandlers struct{ *handlers }

var _ me.StrictServerInterface = meHandlers{}

// accepted は 202（Platform に保存させない。Location に契約の URL）。
type accepted struct {
	sub subscription.SubscriptionView
}

func (a accepted) VisitMeCreateSubscriptionResponse(w nethttp.ResponseWriter) error {
	httpx.MarkTransient(w)
	w.Header().Set("Location", "/v1/me/subscriptions/"+a.sub.ID.String())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(nethttp.StatusAccepted)
	return json.NewEncoder(w).Encode(map[string]any{"subscription": map[string]any{"id": a.sub.ID, "status": a.sub.Status}, "processing": true})
}

func (h meHandlers) MeCreateSubscription(ctx context.Context, req me.MeCreateSubscriptionRequestObject) (me.MeCreateSubscriptionResponseObject, error) {
	info := requestInfo(ctx)
	b := req.Body
	in := app.CheckoutInput{
		CustomerAccountID: httpx.ConsumerID(ctx), IdempotencyKey: info.key, RequestHash: info.hash,
		TenantID: sk.IDFromUUID[sk.TenantTag](b.TenantId), PlanID: sk.IDFromUUID[sk.PlanTag](b.PlanId), PaymentMethodID: sk.IDFromUUID[sk.PaymentMethodTag](b.PaymentMethodId),
	}
	if b.CouponCode != nil {
		in.CouponCode = *b.CouponCode
	}
	if b.ExpectTrial != nil {
		in.ExpectTrial = *b.ExpectTrial
	}
	res, err := h.svc.Checkout(ctx, in)
	if err != nil {
		return nil, problem(err)
	}
	if res.Status == nethttp.StatusAccepted {
		return accepted{sub: res.Subscription}, nil
	}
	v := res.Subscription
	body := map[string]any{
		"subscription": map[string]any{
			"id": v.ID, "tenant_id": v.TenantID, "plan_id": v.PlanID, "status": v.Status, "trial_applied": v.TrialEnd != nil, "trial_end": v.TrialEnd,
			"current_period": map[string]any{"start": v.CurrentPeriod.Start, "end": v.CurrentPeriod.End}, "next_renewal_date": v.NextRenewalDate,
		},
		"invoice": nil,
	}
	if inv := res.Invoice; inv != nil {
		var tax int64
		for _, t := range inv.Tax {
			tax += t.TaxAmount.Int64()
		}
		body["invoice"] = map[string]any{"id": inv.ID, "total": inv.Total.Int64(), "tax": tax, "status": inv.Status}
	}
	return convert[me.MeCreateSubscription201JSONResponse](body)
}

func (h meHandlers) MeChangeSubscriptionPaymentMethod(ctx context.Context, req me.MeChangeSubscriptionPaymentMethodRequestObject) (me.MeChangeSubscriptionPaymentMethodResponseObject, error) {
	res, err := h.svc.ChangePaymentMethod(ctx, httpx.ConsumerID(ctx), sk.IDFromUUID[sk.SubscriptionTag](req.SubscriptionId), sk.IDFromUUID[sk.PaymentMethodTag](req.Body.PaymentMethodId))
	if err != nil {
		return nil, problem(err)
	}
	v := res.Subscription
	return convert[me.MeChangeSubscriptionPaymentMethod200JSONResponse](map[string]any{
		"subscription_id": v.ID, "payment_method_id": v.PaymentMethodID, "status": v.Status, "recollection": res.Recollection,
	})
}

func (h meHandlers) MeDeletePaymentMethod(ctx context.Context, req me.MeDeletePaymentMethodRequestObject) (me.MeDeletePaymentMethodResponseObject, error) {
	if err := h.svc.DeletePaymentMethod(ctx, httpx.ConsumerID(ctx), sk.IDFromUUID[sk.PaymentMethodTag](req.PaymentMethodId)); err != nil {
		return nil, problem(err)
	}
	return me.MeDeletePaymentMethod204Response{}, nil
}

func (h meHandlers) MeWithdraw(ctx context.Context, _ me.MeWithdrawRequestObject) (me.MeWithdrawResponseObject, error) {
	if err := h.svc.Withdraw(ctx, httpx.ConsumerID(ctx)); err != nil {
		return nil, problem(err)
	}
	return me.MeWithdraw204Response{}, nil
}

// ---- ops ----

type opsHandlers struct{ *handlers }

var _ ops.StrictServerInterface = opsHandlers{}

func runDate(d any) (sk.Date, error) {
	s := fmt.Sprint(d)
	if t, ok := d.(interface{ String() string }); ok {
		s = t.String()
	}
	if len(s) > 10 {
		s = s[:10]
	}
	date, err := sk.ParseDate(s)
	if err != nil {
		return sk.Date{}, httpx.Error(errcodes.ValidationFailed, "run_date is invalid", httpx.FieldError{Field: "run_date", Code: "invalid"})
	}
	return date, nil
}

func (h opsHandlers) OpsRerunBillingRun(ctx context.Context, req ops.OpsRerunBillingRunRequestObject) (ops.OpsRerunBillingRunResponseObject, error) {
	date, err := runDate(req.RunDate)
	if err != nil {
		return nil, err
	}
	var only *sk.TenantID
	if req.Body != nil && req.Body.TenantId != nil {
		t := sk.IDFromUUID[sk.TenantTag](*req.Body.TenantId)
		only = &t
	}
	pr, _ := httpx.PrincipalFrom(ctx)
	res, err := h.svc.Rerun(ctx, date, only, pr.Subject)
	if err != nil {
		return nil, problem(err)
	}
	return convert[ops.OpsRerunBillingRun202JSONResponse](map[string]any{
		"billing_run_id": res.BillingRunID, "run_date": res.RunDate, "seq": res.Seq, "trigger": "rerun", "status": "dispatching",
	})
}

func (h opsHandlers) OpsGetBillingRuns(ctx context.Context, req ops.OpsGetBillingRunsRequestObject) (ops.OpsGetBillingRunsResponseObject, error) {
	date, err := runDate(req.RunDate)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.BillingRuns(ctx, date)
	if err != nil {
		return nil, problem(err)
	}
	runs := make([]map[string]any, len(v.Runs))
	for i, r := range v.Runs {
		runs[i] = map[string]any{"id": r.ID, "seq": r.Seq, "trigger": r.Trigger, "status": r.Status, "enqueued_count": r.EnqueuedCount,
			"resume_count": r.ResumeCount, "started_at": r.StartedAt, "finished_at": r.FinishedAt}
	}
	return convert[ops.OpsGetBillingRuns200JSONResponse](map[string]any{"run_date": date, "runs": runs, "outcomes": v.Outcomes})
}

func bulkJSON(v app.BulkJobView) map[string]any {
	j := v.Job
	failures := make([]map[string]any, len(v.Failures))
	for i, f := range v.Failures {
		failures[i] = map[string]any{"subscription_id": f.SubscriptionID, "error": f.Error}
	}
	return map[string]any{
		"id": j.ID, "tenant_id": j.TenantID, "status": j.Status, "sweep_round": j.SweepRound,
		"counts": map[string]any{"processed": j.ProcessedCount, "scheduled_at_period_end": j.ScheduledCount, "canceled_immediately": j.CanceledCount,
			"skipped": j.SkippedCount, "failed": j.FailedCount},
		"failures": failures, "started_at": j.StartedAt, "completed_at": j.CompletedAt,
	}
}

func (h opsHandlers) OpsListBulkCancellationJobs(ctx context.Context, req ops.OpsListBulkCancellationJobsRequestObject) (ops.OpsListBulkCancellationJobsResponseObject, error) {
	jobs, err := h.svc.BulkJobsOf(ctx, sk.IDFromUUID[sk.TenantTag](req.Params.TenantId))
	if err != nil {
		return nil, problem(err)
	}
	data := make([]map[string]any, len(jobs))
	for i, j := range jobs {
		data[i] = bulkJSON(j)
	}
	return convert[ops.OpsListBulkCancellationJobs200JSONResponse](map[string]any{"data": data})
}

func (h opsHandlers) OpsGetBulkCancellationJob(ctx context.Context, req ops.OpsGetBulkCancellationJobRequestObject) (ops.OpsGetBulkCancellationJobResponseObject, error) {
	v, err := h.svc.BulkJob(ctx, uuid.UUID(req.JobId))
	if err != nil {
		return nil, problem(err)
	}
	return convert[ops.OpsGetBulkCancellationJob200JSONResponse](bulkJSON(v))
}
