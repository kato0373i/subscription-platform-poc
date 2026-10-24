package httpx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/httpx/errcodes"
	"subscription-platform/internal/platform/observability"
)

// IdemState は Begin の結果。
type IdemState int

const (
	IdemNew IdemState = iota
	IdemReplay
	IdemMismatch
	IdemInProgress
)

// StoredResponse は保存した応答。
type StoredResponse struct {
	Status  int
	Headers map[string]string // Content-Type・Location のみ
	Body    []byte
}

// IdempotencyStore は Idempotency-Key の記録（platform.idempotency_keys）。
type IdempotencyStore interface {
	Begin(ctx context.Context, scope, key string, hash []byte) (IdemState, *StoredResponse, error)
	Complete(ctx context.Context, scope, key string, resp StoredResponse) error
	Abort(ctx context.Context, scope, key string) error
}

const (
	idemLease = 60 * time.Second
	idemTTL   = 24 * time.Hour
)

// PGIdempotencyStore は PostgreSQL の実装。業務とは別のトランザクションで書く（E15）。
type PGIdempotencyStore struct{ db *db.ModuleDB }

func NewPGIdempotencyStore(pool *db.Pool) *PGIdempotencyStore {
	return &PGIdempotencyStore{db: pool.Platform()}
}

func (s *PGIdempotencyStore) Begin(ctx context.Context, scope, key string, hash []byte) (IdemState, *StoredResponse, error) {
	var state IdemState
	var stored *StoredResponse
	err := s.db.InTx(db.WithSystemScope(ctx, "idempotency"), func(ctx context.Context, tx db.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO platform.idempotency_keys (scope, key, request_hash, status, locked_until, expires_at)
			VALUES ($1, $2, $3, 'in_progress', now() + $4::interval, now() + $5::interval) ON CONFLICT DO NOTHING`,
			scope, key, hash, interval(idemLease), interval(idemTTL))
		if err != nil {
			return fmt.Errorf("idempotency: insert: %w", err)
		}
		if tag.RowsAffected() == 1 {
			state = IdemNew
			return nil
		}
		var (
			h                []byte
			status           string
			respStatus       *int16
			headersJSON      []byte
			body             []byte
			leaseOK, expired bool
		)
		err = tx.QueryRow(ctx, `SELECT request_hash, status, response_status, response_headers, response_body,
				coalesce(locked_until > now(), false), expires_at < now()
			FROM platform.idempotency_keys WHERE scope = $1 AND key = $2 FOR UPDATE`, scope, key).
			Scan(&h, &status, &respStatus, &headersJSON, &body, &leaseOK, &expired)
		if errors.Is(err, pgx.ErrNoRows) {
			return errIdemRace
		}
		if err != nil {
			return fmt.Errorf("idempotency: select: %w", err)
		}
		takeOver := func() error {
			_, err := tx.Exec(ctx, `UPDATE platform.idempotency_keys SET request_hash = $3, status = 'in_progress', response_status = NULL,
				response_headers = NULL, response_body = NULL, locked_until = now() + $4::interval, expires_at = now() + $5::interval, created_at = now()
				WHERE scope = $1 AND key = $2`, scope, key, hash, interval(idemLease), interval(idemTTL))
			state = IdemNew
			return err //nolint:wrapcheck
		}
		switch {
		case expired:
			return takeOver()
		case !bytes.Equal(h, hash):
			state = IdemMismatch
		case status == "completed":
			state = IdemReplay
			stored = &StoredResponse{Status: int(*respStatus), Body: body, Headers: map[string]string{}}
			_ = json.Unmarshal(headersJSON, &stored.Headers)
		case leaseOK:
			state = IdemInProgress
		default:
			return takeOver() // 前のプロセスが落ちたとみなす
		}
		return nil
	})
	if errors.Is(err, errIdemRace) {
		return s.Begin(ctx, scope, key, hash)
	}
	return state, stored, err
}

var errIdemRace = errors.New("idempotency: row vanished")

func interval(d time.Duration) string { return fmt.Sprintf("%d milliseconds", d.Milliseconds()) }

func (s *PGIdempotencyStore) Complete(ctx context.Context, scope, key string, resp StoredResponse) error {
	hj, _ := json.Marshal(resp.Headers)
	return s.db.InTx(db.WithSystemScope(ctx, "idempotency"), func(ctx context.Context, tx db.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE platform.idempotency_keys SET status = 'completed', response_status = $3, response_headers = $4, response_body = $5, locked_until = NULL
			WHERE scope = $1 AND key = $2`, scope, key, resp.Status, hj, resp.Body)
		if err != nil {
			return fmt.Errorf("idempotency: complete: %w", err)
		}
		return nil
	})
}

func (s *PGIdempotencyStore) Abort(ctx context.Context, scope, key string) error {
	return s.db.InTx(db.WithSystemScope(ctx, "idempotency"), func(ctx context.Context, tx db.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM platform.idempotency_keys WHERE scope = $1 AND key = $2`, scope, key)
		if err != nil {
			return fmt.Errorf("idempotency: abort: %w", err)
		}
		return nil
	})
}

func idemSubject(p Principal) string {
	switch p.Kind {
	case KindConsumer:
		if p.CustomerAccountID != nil {
			return p.CustomerAccountID.String()
		}
	case KindTenantAPI:
		if p.TenantID != nil {
			return p.TenantID.String()
		}
	case KindStaff:
		if p.TenantID != nil {
			return p.TenantID.String() + "/" + p.Subject
		}
	case KindOperator:
	}
	if p.Subject != "" {
		return p.Subject
	}
	return "anonymous"
}

// idempotent は x-sp-idempotency: required のルートの処理（§7.1 の表）。
func (r *Router) idempotent(pol *routePolicy, h http.Handler, w http.ResponseWriter, req *http.Request) {
	key := req.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 255 {
		WriteProblem(w, req, Error(errcodes.ValidationFailed, "Idempotency-Key header is required", FieldError{Field: "Idempotency-Key", Code: "required"}))
		return
	}
	if r.idem == nil {
		WriteProblem(w, req, Error(errcodes.InternalError, "idempotency store is not configured"))
		return
	}
	p, _ := PrincipalFrom(req.Context())
	scope := fmt.Sprintf("%s:%s:%s", p.Kind, idemSubject(p), pol.pattern)
	if p.Kind == "" {
		scope = "anonymous:" + pol.pattern
	}
	body, _ := io.ReadAll(req.Body)
	req.Body = io.NopCloser(bytes.NewReader(body))
	sum := sha256.New()
	sum.Write([]byte(req.Method))
	sum.Write([]byte(req.URL.Path))
	sum.Write(body)
	hash := sum.Sum(nil)

	ctx := req.Context()
	state, stored, err := r.idem.Begin(ctx, scope, key, hash)
	if err != nil {
		WriteProblem(w, req, err)
		return
	}
	switch state {
	case IdemReplay:
		for k, v := range stored.Headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Idempotent-Replayed", "true")
		w.WriteHeader(stored.Status)
		_, _ = w.Write(stored.Body) //nolint:gosec // 自分が保存した応答をそのまま返す（Content-Type も保存したもの）
		return
	case IdemMismatch:
		WriteProblem(w, req, Error(errcodes.IdempotencyKeyReused, ""))
		return
	case IdemInProgress:
		w.Header().Set("Retry-After", "1")
		WriteProblem(w, req, Error(errcodes.RequestInProgress, ""))
		return
	case IdemNew:
	}
	buf := newBufferWriter()
	func() {
		var perr error
		defer func() {
			if perr != nil && buf.status == 0 {
				WriteProblem(buf, req, perr)
			}
		}()
		defer observability.Recover(ctx, "http", &perr)
		h.ServeHTTP(buf, req)
	}()
	bg := context.WithoutCancel(ctx)
	if buf.code() >= 500 || buf.transient {
		if err := r.idem.Abort(bg, scope, key); err != nil {
			observability.Logger("platform.http").LogAttrs(ctx, slog.LevelWarn, "idempotency.abort_failed", observability.Err(err))
		}
	} else {
		headers := map[string]string{}
		for _, k := range []string{"Content-Type", "Location"} {
			if v := buf.header.Get(k); v != "" {
				headers[k] = v
			}
		}
		if err := r.idem.Complete(bg, scope, key, StoredResponse{Status: buf.code(), Headers: headers, Body: buf.body.Bytes()}); err != nil {
			// E15: 業務はコミット済み。再送時は業務側の冪等性で守る
			observability.Logger("platform.http").LogAttrs(ctx, slog.LevelError, "idempotency.save_failed", observability.Err(err))
		}
	}
	buf.flushTo(w)
}
