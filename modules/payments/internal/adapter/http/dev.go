package http

import (
	"encoding/json"
	"errors"
	nethttp "net/http"

	"subscription-platform/api/openapi"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/httpx/errcodes"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/payments/internal/adapter/provider/stub"
	"subscription-platform/modules/payments/internal/app"
)

// RegisterDev は devtools ビルドだけの StubAdapter の操作（§7.8）を登録する。settle は署名した通知を Webhook と同じ処理（IngestWebhook）に流す。
func RegisterDev(r *httpx.Router, svc *app.Service, st *stub.Adapter, stubName string) error {
	spec, err := openapi.Load("dev/payments.yaml")
	if err != nil {
		return err
	}
	settle := func(w nethttp.ResponseWriter, req *nethttp.Request, orderRef string) {
		var body struct {
			Result      string `json:"result"`
			FailureCode string `json:"failure_code"`
		}
		if req.ContentLength != 0 {
			_ = json.NewDecoder(req.Body).Decode(&body)
		}
		payload, h, err := st.Settle(req.Context(), orderRef, body.Result, body.FailureCode)
		if errors.Is(err, stub.ErrNotPending) {
			httpx.WriteProblem(w, req, httpx.Error(errcodes.Conflict, "charge is not pending"))
			return
		}
		if err != nil {
			httpx.WriteProblem(w, req, err)
			return
		}
		if err := svc.IngestWebhook(req.Context(), stubName, h, payload); err != nil {
			httpx.WriteProblem(w, req, err)
			return
		}
		w.WriteHeader(nethttp.StatusAccepted)
	}
	return r.Mount(spec, func(m httpx.ServeMux) {
		m.HandleFunc("POST /v1/dev/payments/stub/charges/{provider_order_ref}/settle", func(w nethttp.ResponseWriter, req *nethttp.Request) {
			settle(w, req, req.PathValue("provider_order_ref"))
		})
		m.HandleFunc("POST /v1/dev/payments/stub/payables/invoice/{invoice_id}/settle", func(w nethttp.ResponseWriter, req *nethttp.Request) {
			ref, _, err := svc.InflightOrderRef(req.Context(), req.PathValue("invoice_id"))
			if err != nil {
				httpx.WriteProblem(w, req, problem(err))
				return
			}
			settle(w, req, ref)
		})
		m.HandleFunc("PUT /v1/dev/payments/stub/payment-methods/{payment_method_id}/behavior", func(w nethttp.ResponseWriter, req *nethttp.Request) {
			var body struct {
				Scenario  string `json:"scenario"`
				Remaining int    `json:"remaining"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				httpx.WriteProblem(w, req, httpx.Error(errcodes.ValidationFailed, "invalid body"))
				return
			}
			id, err := sk.ParseID[sk.PaymentMethodTag](req.PathValue("payment_method_id"))
			if err != nil {
				httpx.WriteProblem(w, req, httpx.Error(errcodes.NotFound, ""))
				return
			}
			ref, err := svc.MethodRef(req.Context(), id, stubName)
			if err != nil {
				httpx.WriteProblem(w, req, problem(err))
				return
			}
			if err := st.SetBehavior(req.Context(), ref, body.Scenario, body.Remaining); err != nil {
				httpx.WriteProblem(w, req, httpx.Error(errcodes.ValidationFailed, err.Error(), httpx.FieldError{Field: "scenario", Code: "invalid"}))
				return
			}
			w.WriteHeader(nethttp.StatusNoContent)
		})
	})
}
