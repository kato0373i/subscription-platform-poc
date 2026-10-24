// Package httpx は HTTP の共通処理（ルーター・認証・認可・スコープ・Idempotency・Problem Details・ページング・監査）を受け持つ（platform.md §3.2・§4・§7）。
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"subscription-platform/internal/platform/httpx/errcodes"
	"subscription-platform/internal/platform/observability"
)

// FieldError は項目ごとの誤り。
type FieldError struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

// Problem は application/problem+json の本文。error を実装する。
type Problem struct {
	Type          string        `json:"type"`
	Title         string        `json:"title"`
	Status        int           `json:"status"`
	Detail        string        `json:"detail,omitempty"`
	Code          errcodes.Code `json:"code"`
	RequestID     string        `json:"request_id"`
	CorrelationID string        `json:"correlation_id"`
	Errors        []FieldError  `json:"errors,omitempty"`
	RetryAfter    int           `json:"-"`
}

func (p *Problem) Error() string {
	if p.Detail != "" {
		return string(p.Code) + ": " + p.Detail
	}
	return string(p.Code)
}

// ErrorCode は observability.ErrorCoder を実装する（ログの error.code）。
func (p *Problem) ErrorCode() string { return string(p.Code) }

// Error はコードから Problem を作る。
func Error(code errcodes.Code, detail string, fields ...FieldError) *Problem {
	return &Problem{
		Type:   "https://docs.example.com/errors/" + string(code),
		Title:  code.Title(),
		Status: code.Status(),
		Detail: detail,
		Code:   code,
		Errors: fields,
	}
}

type mapErrorKey struct{}

// withErrorMapper は Ports.MapError を context に入れる（WriteProblem が使う）。
func withErrorMapper(ctx context.Context, f func(error) *Problem) context.Context {
	return context.WithValue(ctx, mapErrorKey{}, f)
}

// AsProblem は err を Problem にする。*Problem でなければ、モジュールの対応（MapError）を試し、なければ internal_error。
func AsProblem(ctx context.Context, err error) *Problem {
	var p *Problem
	if errors.As(err, &p) {
		cp := *p
		return &cp
	}
	if f, ok := ctx.Value(mapErrorKey{}).(func(error) *Problem); ok && f != nil {
		if mp := f(err); mp != nil {
			return mp
		}
	}
	return Error(errcodes.InternalError, "")
}

// WriteProblem はエラーを Problem Details で書く。*Problem 以外は internal_error にし、内部の詳細は出さない（AC-PLT-034）。
func WriteProblem(w http.ResponseWriter, r *http.Request, err error) {
	p := AsProblem(r.Context(), err)
	p.RequestID = requestIDFrom(r.Context())
	p.CorrelationID, _ = observability.CorrelationFrom(r.Context())
	if p.Status >= 500 {
		lvl := slog.LevelError
		if p.Code == errcodes.ServiceUnavailable || p.Code == "provider_unavailable" {
			lvl = slog.LevelWarn
		}
		observability.Logger("platform.http").LogAttrs(r.Context(), lvl, "http.error", observability.Err(err))
	}
	switch rw := w.(type) {
	case *responseRecorder:
		rw.errCode = string(p.Code)
	case *bufferWriter:
		rw.errCode = string(p.Code)
	}
	if ri := reqInfoFrom(r.Context()); ri != nil {
		ri.errCode = string(p.Code)
	}
	if p.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfter))
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// WriteJSON は JSON の応答を書く。
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// StrictRequestError は oapi-codegen の生成コードで、パラメータ・本文を読めなかったときの処理（ErrorHandlerFunc・RequestErrorHandlerFunc）。
// 形式の誤りは通常 OpenAPI の検証で先に弾かれるので、ここに来るのは JSON として読めない本文など。
func StrictRequestError(w http.ResponseWriter, r *http.Request, err error) {
	observability.Logger("platform.http").LogAttrs(r.Context(), slog.LevelInfo, "http.request_invalid", observability.Err(err))
	WriteProblem(w, r, Error(errcodes.ValidationFailed, "request could not be parsed", FieldError{Field: "body", Code: "invalid"}))
}

// StrictResponseError は strict server のハンドラが返した error の処理（ResponseErrorHandlerFunc）。
func StrictResponseError(w http.ResponseWriter, r *http.Request, err error) { WriteProblem(w, r, err) }
