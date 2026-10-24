//go:build devtools

// Package devapi は devtools ビルドだけに含まれる開発用 API（/v1/dev/*。platform.md §7.3）。認証なし。
package devapi

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"subscription-platform/api/openapi"
	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/devclock"
	"subscription-platform/internal/platform/httpx"
	"subscription-platform/internal/platform/httpx/errcodes"
	"subscription-platform/internal/platform/outbox"
	"subscription-platform/internal/platform/scheduler"
	sk "subscription-platform/internal/sharedkernel"
)

// Deps は開発用 API の依存。
type Deps struct {
	Pool      *db.Pool
	Clock     *devclock.Clock
	Scheduler *scheduler.Service
	DevKey    *ecdsa.PrivateKey // AUTH_MODE=dev のときだけ。nil なら /v1/dev/tokens は 404
}

// SmokeEventType は配線の確認用イベント。
const SmokeEventType = "platform.smoke_requested"

// SmokeConsumer は配線の確認用イベントの購読者名。
const SmokeConsumer = "platform.smoke"

type handlers struct {
	d      Deps
	writer *outbox.Writer
}

// Register は /v1/dev/* を登録する。
func Register(r *httpx.Router, d Deps) error {
	spec, err := openapi.Load("dev/platform.yaml")
	if err != nil {
		return err
	}
	h := &handlers{d: d, writer: outbox.NewWriter("platform", d.Clock)}
	return r.Mount(spec, func(m httpx.ServeMux) {
		m.HandleFunc("GET /v1/dev/clock", h.getClock)
		m.HandleFunc("POST /v1/dev/clock", h.setClock)
		m.HandleFunc("POST /v1/dev/jobs/{job}/run", h.runJob)
		m.HandleFunc("GET /v1/dev/job-runs/{run_id}", h.getRun)
		m.HandleFunc("POST /v1/dev/tokens", h.issueToken)
		m.HandleFunc("POST /v1/dev/smoke", h.smoke)
		m.HandleFunc("GET /v1/dev/smoke/{event_id}", h.smokeStatus)
	})
}

func (h *handlers) getClock(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, h.d.Clock.Get(r.Context()))
}

func (h *handlers) setClock(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Advance *string    `json:"advance"`
		To      *time.Time `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || (body.Advance == nil) == (body.To == nil) {
		httpx.WriteProblem(w, r, httpx.Error(errcodes.ValidationFailed, "specify exactly one of advance or to", httpx.FieldError{Field: "advance", Code: "exactly_one"}))
		return
	}
	var adv *time.Duration
	if body.Advance != nil {
		d, err := time.ParseDuration(*body.Advance)
		if err != nil {
			httpx.WriteProblem(w, r, httpx.Error(errcodes.ValidationFailed, "advance must be a Go duration", httpx.FieldError{Field: "advance", Code: "invalid"}))
			return
		}
		adv = &d
	}
	st, err := h.d.Clock.Set(r.Context(), adv, body.To)
	if errors.Is(err, devclock.ErrBackward) {
		httpx.WriteProblem(w, r, httpx.Error(errcodes.ClockBackwardNotAllowed, ""))
		return
	}
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

type runResponse struct {
	scheduler.RunView
	Deduplicated bool `json:"deduplicated"`
}

func (h *handlers) runJob(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AsOf        *string         `json:"as_of"`
		Params      json.RawMessage `json:"params"`
		Force       bool            `json:"force"`
		WaitSeconds *int            `json:"wait_seconds"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteProblem(w, r, httpx.Error(errcodes.ValidationFailed, "invalid body"))
			return
		}
	}
	opts := scheduler.EnqueueOptions{Trigger: scheduler.TriggerDev, Params: body.Params, Force: body.Force}
	if body.AsOf != nil {
		d, err := sk.ParseDate(*body.AsOf)
		if err != nil {
			httpx.WriteProblem(w, r, httpx.Error(errcodes.ValidationFailed, "as_of must be YYYY-MM-DD", httpx.FieldError{Field: "as_of", Code: "invalid"}))
			return
		}
		opts.AsOf = &d
	}
	res, err := h.d.Scheduler.Enqueue(r.Context(), r.PathValue("job"), opts)
	if errors.Is(err, scheduler.ErrUnknownJob) {
		httpx.WriteProblem(w, r, httpx.Error(errcodes.NotFound, "job is not registered"))
		return
	}
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	wait := 60
	if body.WaitSeconds != nil {
		wait = *body.WaitSeconds
	}
	v, err := h.d.Scheduler.Wait(r.Context(), res.RunID, time.Duration(wait)*time.Second)
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	status := http.StatusOK
	if !v.Done() || (v.Status == "failed" && v.Attempts < 3) {
		status = http.StatusAccepted
	}
	httpx.WriteJSON(w, status, runResponse{RunView: v, Deduplicated: res.Deduplicated})
}

func (h *handlers) getRun(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("run_id"))
	if err != nil {
		httpx.WriteProblem(w, r, httpx.Error(errcodes.NotFound, ""))
		return
	}
	v, err := h.d.Scheduler.Get(r.Context(), id)
	if errors.Is(err, scheduler.ErrRunNotFound) {
		httpx.WriteProblem(w, r, httpx.Error(errcodes.NotFound, ""))
		return
	}
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, runResponse{RunView: v})
}

func (h *handlers) issueToken(w http.ResponseWriter, r *http.Request) {
	if h.d.DevKey == nil {
		httpx.WriteProblem(w, r, httpx.Error(errcodes.NotFound, "AUTH_MODE is not dev"))
		return
	}
	var req httpx.DevTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteProblem(w, r, httpx.Error(errcodes.ValidationFailed, "invalid body"))
		return
	}
	// 署名の有効期限は実時刻（JWT の検証は実時刻で行うため。業務時刻を進めても失効しない）
	tok, exp, err := httpx.IssueDevToken(h.d.DevKey, req, time.Now())
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"token": tok, "expires_at": exp.UTC()})
}

func (h *handlers) smoke(w http.ResponseWriter, r *http.Request) {
	env, err := sk.NewEnvelope(SmokeEventType, "smoke", uuid.Must(uuid.NewV7()), 1, nil, h.d.Clock.Now(), map[string]string{"purpose": "wiring check"})
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	err = h.d.Pool.Platform().InTx(r.Context(), func(ctx context.Context, tx db.Tx) error {
		return h.writer.Append(ctx, tx, env)
	})
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"event_id": env.EventID.String()})
}

func (h *handlers) smokeStatus(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("event_id"))
	if err != nil {
		httpx.WriteProblem(w, r, httpx.Error(errcodes.NotFound, ""))
		return
	}
	var published, consumed bool
	err = h.d.Pool.Platform().ReadTx(r.Context(), func(ctx context.Context, tx db.Tx) error {
		var pub *time.Time
		if err := tx.QueryRow(ctx, `SELECT published_at FROM platform.outbox_events WHERE id = $1`, id).Scan(&pub); err != nil {
			return err
		}
		published = pub != nil
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.processed_messages WHERE consumer = $1 AND message_id = $2)`, SmokeConsumer, id).Scan(&consumed)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteProblem(w, r, httpx.Error(errcodes.NotFound, ""))
		return
	}
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"published": published, "consumed": consumed})
}
