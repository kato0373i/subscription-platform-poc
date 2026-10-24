// Package postgres は payments スキーマのリポジトリ（sqlc の生成コードを包み、domain の型に変換する）。
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"subscription-platform/internal/platform/db"
	"subscription-platform/internal/platform/observability"
	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/payments/contracts"
	"subscription-platform/modules/payments/internal/adapter/postgres/sqlcgen"
	"subscription-platform/modules/payments/internal/domain"
)

var (
	ErrNotFound        = errors.New("payments: row not found")
	ErrVersionConflict = errors.New("payments: version conflict")
	ErrDuplicateKey    = errors.New("payments: duplicate idempotency key")
	ErrInflightExists  = errors.New("payments: in-flight attempt exists")
	ErrSucceededExists = errors.New("payments: succeeded attempt exists")
	ErrDuplicateRegKey = errors.New("payments: duplicate registration key")
)

// Repo は 1 トランザクション分のリポジトリ。
type Repo struct{ q *sqlcgen.Queries }

func New(tx db.Tx) *Repo { return &Repo{q: sqlcgen.New(tx)} }

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func constraint(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return pe.ConstraintName
	}
	return ""
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// ---- payment_methods ----

func (r *Repo) toMethod(ctx context.Context, row sqlcgen.PaymentsPaymentMethod) (*domain.PaymentMethod, error) {
	s := domain.MethodState{
		ID: sk.IDFromUUID[sk.PaymentMethodTag](row.ID), CustomerAccountID: sk.IDFromUUID[sk.CustomerAccountTag](row.CustomerAccountID),
		MethodType: c.MethodType(row.MethodType), Status: row.Status, CreatedAt: row.CreatedAt.UTC(), DeletedAt: utc(row.DeletedAt), Version: int(row.Version),
	}
	if err := json.Unmarshal(row.Display, &s.Display); err != nil {
		return nil, fmt.Errorf("payments: display: %w", err)
	}
	refs, err := r.q.ListProviderRefs(ctx, row.ID)
	if err != nil {
		return nil, fmt.Errorf("payments: provider refs: %w", err)
	}
	for _, ref := range refs {
		s.Refs = append(s.Refs, domain.ProviderRef{Provider: ref.Provider, TenantShopID: ref.TenantShopID, CustomerRef: ref.CustomerRef, MethodRef: ref.MethodRef})
	}
	return domain.RestoreMethod(s), nil
}

// InsertMethod は支払方法と参照を保存する。registrationKey が同じものがあれば ErrDuplicateRegKey。
func (r *Repo) InsertMethod(ctx context.Context, m *domain.PaymentMethod, registrationKey *string) error {
	s := m.State()
	disp, _ := json.Marshal(s.Display)
	err := r.q.InsertPaymentMethod(ctx, sqlcgen.InsertPaymentMethodParams{
		ID: s.ID.UUID(), CustomerAccountID: s.CustomerAccountID.UUID(), MethodType: string(s.MethodType), Display: disp, RegistrationKey: registrationKey, CreatedAt: s.CreatedAt,
	})
	if constraint(err) != "" {
		return ErrDuplicateRegKey
	}
	if err != nil {
		return fmt.Errorf("payments: insert method: %w", err)
	}
	for _, ref := range s.Refs {
		if err := r.InsertRef(ctx, m, ref); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) InsertRef(ctx context.Context, m *domain.PaymentMethod, ref domain.ProviderRef) error {
	s := m.State()
	return r.q.InsertProviderRef(ctx, sqlcgen.InsertProviderRefParams{
		ID: uuid.Must(uuid.NewV7()), PaymentMethodID: s.ID.UUID(), CustomerAccountID: s.CustomerAccountID.UUID(), Provider: ref.Provider,
		TenantShopID: ref.TenantShopID, CustomerRef: ref.CustomerRef, MethodRef: ref.MethodRef, CreatedAt: s.CreatedAt,
	})
}

func (r *Repo) GetMethod(ctx context.Context, id sk.PaymentMethodID) (*domain.PaymentMethod, error) {
	row, err := r.q.GetPaymentMethod(ctx, id.UUID())
	if err != nil {
		return nil, notFound(err)
	}
	return r.toMethod(ctx, row)
}

func (r *Repo) GetMethodByRegistrationKey(ctx context.Context, customer sk.CustomerAccountID, key string) (*domain.PaymentMethod, error) {
	row, err := r.q.GetPaymentMethodByRegistrationKey(ctx, sqlcgen.GetPaymentMethodByRegistrationKeyParams{CustomerAccountID: customer.UUID(), RegistrationKey: &key})
	if err != nil {
		return nil, notFound(err)
	}
	return r.toMethod(ctx, row)
}

func (r *Repo) CountActiveMethods(ctx context.Context, customer sk.CustomerAccountID) (int, error) {
	n, err := r.q.CountActivePaymentMethods(ctx, customer.UUID())
	return int(n), err
}

// Cursor は一覧の位置（作成日時の新しい順）。
type Cursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}

func (r *Repo) ListActiveMethods(ctx context.Context, customer sk.CustomerAccountID, after *Cursor, limit int) ([]*domain.PaymentMethod, error) {
	p := sqlcgen.ListActivePaymentMethodsParams{CustomerAccountID: customer.UUID(), Limit: int32(limit)}
	if after != nil {
		p.AfterCreatedAt, p.AfterID = &after.CreatedAt, &after.ID
	}
	rows, err := r.q.ListActivePaymentMethods(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("payments: list methods: %w", err)
	}
	out := make([]*domain.PaymentMethod, 0, len(rows))
	for _, row := range rows {
		m, err := r.toMethod(ctx, row)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func (r *Repo) UpdateMethod(ctx context.Context, m *domain.PaymentMethod, now time.Time) error {
	s := m.State()
	disp, _ := json.Marshal(s.Display)
	n, err := r.q.UpdatePaymentMethod(ctx, sqlcgen.UpdatePaymentMethodParams{ID: s.ID.UUID(), Status: s.Status, Display: disp, DeletedAt: s.DeletedAt, UpdatedAt: now, Version: int32(s.Version)})
	if err != nil {
		return fmt.Errorf("payments: update method: %w", err)
	}
	if n == 0 {
		return ErrVersionConflict
	}
	return nil
}

// ---- payment_attempts ----

func toAttempt(row sqlcgen.PaymentsPaymentAttempt) *domain.Attempt {
	s := domain.AttemptState{
		ID: sk.IDFromUUID[sk.PaymentAttemptTag](row.ID), TenantID: sk.IDFromUUID[sk.TenantTag](row.TenantID),
		Payable:           c.PayableRef{Type: c.PayableType(row.PayableType), ID: row.PayableID.String()},
		CustomerAccountID: sk.IDFromUUID[sk.CustomerAccountTag](row.CustomerAccountID), PaymentMethodID: sk.IDFromUUID[sk.PaymentMethodTag](row.PaymentMethodID),
		MethodType: c.MethodType(row.MethodType), Provider: row.Provider, TenantShopID: row.TenantShopID, Amount: sk.MustMoney(row.Amount),
		IdempotencyKey: row.IdempotencyKey, CollectionSeq: int(row.CollectionSeq), RequestHash: row.RequestHash, ProviderOrderRef: row.ProviderOrderRef,
		Status: c.AttemptStatus(row.Status), Retryable: row.FailureRetryable, ProviderRawCode: row.ProviderRawCode, ResolvedBy: row.ResolvedBy,
		ResolveNote: row.ResolveNote, CheckCount: int(row.CheckCount), LastCheckedAt: utc(row.LastCheckedAt), RequestedAt: row.RequestedAt.UTC(),
		FinalizedAt: utc(row.FinalizedAt), Version: int(row.Version),
	}
	if row.FailureCode != nil {
		fc := domain.FailureCode(*row.FailureCode)
		s.FailureCode = &fc
	}
	if row.Resolution != nil {
		rs := domain.Resolution(*row.Resolution)
		s.Resolution = &rs
	}
	return domain.RestoreAttempt(s)
}

// InsertAttempt は送信前の試行を保存する。一意制約の違反は種類ごとのエラーにする（E-01・E-03・E-04）。
func (r *Repo) InsertAttempt(ctx context.Context, a *domain.Attempt) error {
	s := a.State()
	payable, err := uuid.Parse(s.Payable.ID)
	if err != nil {
		return fmt.Errorf("payments: payable id: %w", err)
	}
	err = r.q.InsertAttempt(ctx, sqlcgen.InsertAttemptParams{
		ID: s.ID.UUID(), TenantID: s.TenantID.UUID(), PayableType: string(s.Payable.Type), PayableID: payable, CustomerAccountID: s.CustomerAccountID.UUID(),
		PaymentMethodID: s.PaymentMethodID.UUID(), MethodType: string(s.MethodType), Provider: s.Provider, TenantShopID: s.TenantShopID, Amount: s.Amount.Int64(),
		IdempotencyKey: s.IdempotencyKey, CollectionSeq: int32(s.CollectionSeq), RequestHash: s.RequestHash, ProviderOrderRef: s.ProviderOrderRef, RequestedAt: s.RequestedAt,
	})
	switch constraint(err) {
	case "":
	case "payment_attempts_idempotency_key_key":
		return ErrDuplicateKey
	case "uq_attempt_inflight":
		return ErrInflightExists
	case "uq_attempt_succeeded":
		return ErrSucceededExists
	default:
		return fmt.Errorf("payments: insert attempt: %w", err)
	}
	if err != nil {
		return fmt.Errorf("payments: insert attempt: %w", err)
	}
	return nil
}

// AttemptLock は試行の行ロックの種類。
type AttemptLock int

const (
	NoLock AttemptLock = iota
	ForUpdate
	SkipLocked
)

func (r *Repo) GetAttempt(ctx context.Context, id sk.PaymentAttemptID, lock AttemptLock) (*domain.Attempt, error) {
	var row sqlcgen.PaymentsPaymentAttempt
	var err error
	switch lock {
	case ForUpdate:
		row, err = r.q.GetAttemptForUpdate(ctx, id.UUID())
	case SkipLocked:
		row, err = r.q.GetAttemptForUpdateSkipLocked(ctx, id.UUID())
	case NoLock:
		row, err = r.q.GetAttempt(ctx, id.UUID())
	}
	if err != nil {
		return nil, notFound(err)
	}
	return toAttempt(row), nil
}

func (r *Repo) GetAttemptByKey(ctx context.Context, key string) (*domain.Attempt, error) {
	row, err := r.q.GetAttemptByIdempotencyKey(ctx, key)
	if err != nil {
		return nil, notFound(err)
	}
	return toAttempt(row), nil
}

func (r *Repo) GetAttemptByOrderRef(ctx context.Context, ref string, forUpdate bool) (*domain.Attempt, error) {
	var row sqlcgen.PaymentsPaymentAttempt
	var err error
	if forUpdate {
		row, err = r.q.GetAttemptByOrderRefForUpdate(ctx, &ref)
	} else {
		row, err = r.q.GetAttemptByOrderRef(ctx, &ref)
	}
	if err != nil {
		return nil, notFound(err)
	}
	return toAttempt(row), nil
}

// BlockingAttempt は請求の成功済み（優先）・進行中の試行。なければ nil。
func (r *Repo) BlockingAttempt(ctx context.Context, p c.PayableRef) (*sk.PaymentAttemptID, c.AttemptStatus, error) {
	id, err := uuid.Parse(p.ID)
	if err != nil {
		return nil, "", err
	}
	row, err := r.q.FindBlockingAttempt(ctx, sqlcgen.FindBlockingAttemptParams{PayableType: string(p.Type), PayableID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	aid := sk.IDFromUUID[sk.PaymentAttemptTag](row.ID)
	return &aid, c.AttemptStatus(row.Status), nil
}

// UpdateAttempt は楽観ロックで保存する。from は保存前の状態（domain.state_changed のログ）。
func (r *Repo) UpdateAttempt(ctx context.Context, a *domain.Attempt, from c.AttemptStatus, now time.Time) error {
	s := a.State()
	var fc, res *string
	if s.FailureCode != nil {
		v := string(*s.FailureCode)
		fc = &v
	}
	if s.Resolution != nil {
		v := string(*s.Resolution)
		res = &v
	}
	n, err := r.q.UpdateAttempt(ctx, sqlcgen.UpdateAttemptParams{
		ID: s.ID.UUID(), Status: string(s.Status), FailureCode: fc, FailureRetryable: s.Retryable, ProviderRawCode: s.ProviderRawCode, Resolution: res,
		ResolvedBy: s.ResolvedBy, ResolveNote: s.ResolveNote, CheckCount: int32(s.CheckCount), LastCheckedAt: s.LastCheckedAt, FinalizedAt: s.FinalizedAt,
		UpdatedAt: now, Version: int32(s.Version),
	})
	if err != nil {
		return fmt.Errorf("payments: update attempt: %w", err)
	}
	if n == 0 {
		return ErrVersionConflict
	}
	reason := ""
	if res != nil {
		reason = *res
	}
	observability.StateChanged(ctx, "payment_attempt", s.ID.String(), string(from), string(s.Status), reason)
	return nil
}

func (r *Repo) DeleteProcessingAttempt(ctx context.Context, id sk.PaymentAttemptID) error {
	_, err := r.q.DeleteProcessingAttempt(ctx, id.UUID())
	return err
}

func (r *Repo) ListUnresolved(ctx context.Context, unknownBefore, processingBefore, pendingBefore time.Time, limit int) ([]sk.PaymentAttemptID, error) {
	ids, err := r.q.ListUnresolvedAttempts(ctx, sqlcgen.ListUnresolvedAttemptsParams{UnknownBefore: unknownBefore, ProcessingBefore: processingBefore, PendingBefore: pendingBefore, Lim: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("payments: list unresolved: %w", err)
	}
	out := make([]sk.PaymentAttemptID, len(ids))
	for i, id := range ids {
		out[i] = sk.IDFromUUID[sk.PaymentAttemptTag](id)
	}
	return out, nil
}

func (r *Repo) AttemptsByOrderRefs(ctx context.Context, refs []string) (map[string]*domain.Attempt, error) {
	rows, err := r.q.GetAttemptsByOrderRefs(ctx, refs)
	if err != nil {
		return nil, fmt.Errorf("payments: attempts by refs: %w", err)
	}
	out := map[string]*domain.Attempt{}
	for _, row := range rows {
		if row.ProviderOrderRef != nil {
			out[*row.ProviderOrderRef] = toAttempt(row)
		}
	}
	return out, nil
}

func (r *Repo) AttemptsInRange(ctx context.Context, provider string, from, to time.Time) ([]*domain.Attempt, error) {
	rows, err := r.q.ListAttemptsInRange(ctx, sqlcgen.ListAttemptsInRangeParams{Provider: provider, FromTime: from, ToTime: to})
	if err != nil {
		return nil, fmt.Errorf("payments: attempts in range: %w", err)
	}
	out := make([]*domain.Attempt, len(rows))
	for i, row := range rows {
		out[i] = toAttempt(row)
	}
	return out, nil
}

// ---- provider_events ----

// InsertProviderEvent は通知を保存する。新しく保存できたら true（重複は false）。
func (r *Repo) InsertProviderEvent(ctx context.Context, provider, eventID, orderRef string, payload []byte, now time.Time) (bool, error) {
	var ref *string
	if orderRef != "" {
		ref = &orderRef
	}
	if len(payload) == 0 || !json.Valid(payload) {
		payload, _ = json.Marshal(map[string]string{"raw": string(payload)})
	}
	n, err := r.q.InsertProviderEvent(ctx, sqlcgen.InsertProviderEventParams{Provider: provider, ProviderEventID: eventID, ProviderOrderRef: ref, Payload: payload, ReceivedAt: now})
	return n == 1, err
}

// LockPendingEvent は pending の通知を SKIP LOCKED で取る。ロック中・処理済みなら ErrNotFound。
func (r *Repo) LockPendingEvent(ctx context.Context, provider, eventID string) (sqlcgen.PaymentsProviderEvent, error) {
	row, err := r.q.LockPendingProviderEvent(ctx, sqlcgen.LockPendingProviderEventParams{Provider: provider, ProviderEventID: eventID})
	return row, notFound(err)
}

func (r *Repo) FinishEvent(ctx context.Context, provider, eventID, status string, lastErr *string, now time.Time) error {
	return r.q.FinishProviderEvent(ctx, sqlcgen.FinishProviderEventParams{Provider: provider, ProviderEventID: eventID, Status: status, ProcessedAt: &now, LastError: lastErr})
}

func (r *Repo) RetryEvent(ctx context.Context, provider, eventID string, next time.Time, lastErr string) error {
	return r.q.RetryProviderEvent(ctx, sqlcgen.RetryProviderEventParams{Provider: provider, ProviderEventID: eventID, NextAttemptAt: next, LastError: &lastErr})
}

// EventKey は通知の主キー。
type EventKey struct{ Provider, EventID string }

func (r *Repo) DueEvents(ctx context.Context, now, receivedBefore time.Time) ([]EventKey, error) {
	rows, err := r.q.ListDueProviderEvents(ctx, sqlcgen.ListDueProviderEventsParams{Now: now, ReceivedBefore: receivedBefore})
	if err != nil {
		return nil, fmt.Errorf("payments: due events: %w", err)
	}
	out := make([]EventKey, len(rows))
	for i, row := range rows {
		out[i] = EventKey{Provider: row.Provider, EventID: row.ProviderEventID}
	}
	return out, nil
}

// ---- reconciliation_discrepancies ----

// Discrepancy は照合の不整合 1 件。
type Discrepancy struct {
	BusinessDate     sk.Date
	Provider         string
	Kind             string
	AttemptID        *sk.PaymentAttemptID
	TenantID         *sk.TenantID
	ProviderOrderRef string
	Detail           map[string]any
}

// InsertDiscrepancy は不整合を保存する（UNIQUE で再実行は冪等）。新しく保存できたら true。
func (r *Repo) InsertDiscrepancy(ctx context.Context, d Discrepancy, now time.Time) (bool, error) {
	detail, _ := json.Marshal(d.Detail)
	var aid, tid *uuid.UUID
	if d.AttemptID != nil {
		u := d.AttemptID.UUID()
		aid = &u
	}
	if d.TenantID != nil {
		u := d.TenantID.UUID()
		tid = &u
	}
	n, err := r.q.InsertDiscrepancy(ctx, sqlcgen.InsertDiscrepancyParams{
		ID: uuid.Must(uuid.NewV7()), BusinessDate: d.BusinessDate, Provider: d.Provider, Kind: d.Kind, AttemptID: aid, TenantID: tid,
		ProviderOrderRef: d.ProviderOrderRef, Detail: detail, DetectedAt: now,
	})
	return n == 1, err
}

func (r *Repo) ListDiscrepancies(ctx context.Context, date *sk.Date, status *string, after *uuid.UUID, limit int) ([]sqlcgen.PaymentsReconciliationDiscrepancy, error) {
	return r.q.ListDiscrepancies(ctx, sqlcgen.ListDiscrepanciesParams{Limit: int32(limit), BusinessDate: date, Status: status, AfterID: after})
}
