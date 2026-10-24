// Package http は customer モジュールの me API（oapi-codegen の strict server を実装する）。
package http

import (
	"context"
	"errors"

	"subscription-platform/api/openapi"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/httpx/errcodes"
	"subscription-platform/modules/customer/contracts"
	"subscription-platform/modules/customer/internal/adapter/http/gen/me"
	"subscription-platform/modules/customer/internal/app"
)

// Register は /v1/me のルートを登録する。
func Register(r *httpx.Router, svc *app.Service) error {
	spec, err := openapi.Load("me/customer.yaml")
	if err != nil {
		return err
	}
	h := me.NewStrictHandlerWithOptions(&handlers{svc: svc}, nil,
		me.StrictHTTPServerOptions{RequestErrorHandlerFunc: httpx.StrictRequestError, ResponseErrorHandlerFunc: httpx.StrictResponseError})
	return r.Mount(spec, func(m httpx.ServeMux) {
		me.HandlerWithOptions(h, me.StdHTTPServerOptions{BaseRouter: m, ErrorHandlerFunc: httpx.StrictRequestError})
	})
}

// Problem は customer の contracts のエラーを Problem にする（JIT のミドルウェアでも使う）。対応がなければ nil。
func Problem(err error) *httpx.Problem {
	switch {
	case errors.Is(err, contracts.ErrAccountWithdrawn):
		return httpx.Error(errcodes.AccountWithdrawn, "")
	case errors.Is(err, contracts.ErrEmailUnverified):
		return httpx.Error(errcodes.EmailUnverified, "")
	case errors.Is(err, contracts.ErrEmailAlreadyRegistered):
		return httpx.Error(errcodes.EmailAlreadyRegistered, "")
	case errors.Is(err, contracts.ErrAccountNotFound):
		return httpx.Error(errcodes.NotFound, "")
	}
	return nil
}

type handlers struct{ svc *app.Service }

var _ me.StrictServerInterface = (*handlers)(nil)

func (h *handlers) GetMe(ctx context.Context, _ me.GetMeRequestObject) (me.GetMeResponseObject, error) {
	v, err := h.svc.GetAccount(ctx, httpx.ConsumerID(ctx))
	if err != nil {
		if p := Problem(err); p != nil {
			return nil, p
		}
		return nil, err
	}
	return me.GetMe200JSONResponse{Id: v.ID.UUID(), Email: v.Email, Name: v.Name, Status: me.AccountStatus(v.Status), CreatedAt: v.CreatedAt}, nil
}
