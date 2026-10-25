// Package http は reporting モジュールの API（ダッシュボードと MRR の目標。console-dashboard 設計 §5）。
package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"subscription-platform/api/openapi"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/httpx/errcodes"
	"subscription-platform/modules/reporting/internal/adapter/http/gen/console"
	"subscription-platform/modules/reporting/internal/app"
	"subscription-platform/modules/reporting/internal/domain"
)

// Register は console の API を登録する。
func Register(r *httpx.Router, svc *app.Service) error {
	h := consoleHandlers{svc: svc}
	reqErr, resErr := httpx.StrictRequestError, httpx.StrictResponseError
	spec, err := openapi.Load("console/reporting.yaml")
	if err != nil {
		return err
	}
	return r.Mount(spec, func(mux httpx.ServeMux) {
		s := console.NewStrictHandlerWithOptions(h, nil, console.StrictHTTPServerOptions{RequestErrorHandlerFunc: reqErr, ResponseErrorHandlerFunc: resErr})
		console.HandlerWithOptions(s, console.StdHTTPServerOptions{BaseRouter: mux, ErrorHandlerFunc: reqErr})
	})
}

// Problem は reporting のエラーを Problem にする。対応がなければ nil。
func Problem(err error) *httpx.Problem {
	switch {
	case errors.Is(err, app.ErrInvalidMonth):
		return httpx.Error(errcodes.ValidationFailed, err.Error(), httpx.FieldError{Field: "month", Code: "out_of_range"})
	case errors.Is(err, app.ErrInvalidAmount):
		return httpx.Error(errcodes.ValidationFailed, err.Error(), httpx.FieldError{Field: "amount", Code: "out_of_range"})
	case errors.Is(err, app.ErrVersionConflict):
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

func convert[T any](v any) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("reporting: convert response: %w", err)
	}
	return out, nil
}

func parseMonth(s string) (domain.Month, error) {
	m, err := domain.ParseMonth(s)
	if err != nil {
		return m, httpx.Error(errcodes.ValidationFailed, err.Error(), httpx.FieldError{Field: "month", Code: "invalid"})
	}
	return m, nil
}

type consoleHandlers struct{ svc *app.Service }

var _ console.StrictServerInterface = consoleHandlers{}

func (h consoleHandlers) ConsoleGetReportOverview(ctx context.Context, req console.ConsoleGetReportOverviewRequestObject) (console.ConsoleGetReportOverviewResponseObject, error) {
	var month *domain.Month
	if v := req.Params.Month; v != nil && *v != "" {
		m, err := parseMonth(*v)
		if err != nil {
			return nil, err
		}
		month = &m
	}
	ov, err := h.svc.Overview(ctx, httpx.TenantID(ctx), month)
	if err != nil {
		return nil, problem(err)
	}
	return convert[console.ConsoleGetReportOverview200JSONResponse](overviewJSON(ov))
}

func overviewJSON(ov app.OverviewResult) map[string]any {
	cur := ov.Current
	var goal any
	if g := ov.Goal; g != nil {
		goal = map[string]any{"amount": g.Amount, "version": g.Version, "achievement_rate": float64(cur.MRR) / float64(g.Amount)}
	}
	return map[string]any{
		"month": ov.Month.String(), "as_of": ov.AsOf, "today": ov.Today.String(), "is_current": ov.IsCurrent,
		"kgi": map[string]any{
			"mrr": cur.MRR, "mrr_start": cur.MRRStart, "arr": cur.MRR * 12, "net_new_mrr": cur.Breakdown.Net(), "breakdown": cur.Breakdown, "goal": goal,
		},
		"kpis":      map[string]any{"current": cur, "previous": ov.Previous},
		"trend":     ov.Trend,
		"daily":     ov.Daily,
		"attention": map[string]any{"past_due": ov.PastDue, "trials_ending_7d": ov.TrialsEnding7d},
	}
}

func (h consoleHandlers) ConsolePutMrrGoal(ctx context.Context, req console.ConsolePutMrrGoalRequestObject) (console.ConsolePutMrrGoalResponseObject, error) {
	m, err := parseMonth(req.Month)
	if err != nil {
		return nil, err
	}
	var expected *int
	if v := req.Params.IfMatch; v != nil && *v != "" {
		n, err := strconv.Atoi(strings.Trim(*v, `W/"`))
		if err != nil {
			return nil, httpx.Error(errcodes.ValidationFailed, "If-Match must be the version", httpx.FieldError{Field: "If-Match", Code: "invalid"})
		}
		expected = &n
	}
	pr, _ := httpx.PrincipalFrom(ctx)
	g, err := h.svc.PutGoal(ctx, httpx.TenantID(ctx), m, req.Body.Amount, pr.Subject, expected)
	if err != nil {
		return nil, problem(err)
	}
	return console.ConsolePutMrrGoal200JSONResponse{Amount: g.Amount, Version: g.Version}, nil
}

func (h consoleHandlers) ConsoleDeleteMrrGoal(ctx context.Context, req console.ConsoleDeleteMrrGoalRequestObject) (console.ConsoleDeleteMrrGoalResponseObject, error) {
	m, err := parseMonth(req.Month)
	if err != nil {
		return nil, err
	}
	if err := h.svc.DeleteGoal(ctx, httpx.TenantID(ctx), m); err != nil {
		return nil, problem(err)
	}
	return console.ConsoleDeleteMrrGoal204Response{}, nil
}
