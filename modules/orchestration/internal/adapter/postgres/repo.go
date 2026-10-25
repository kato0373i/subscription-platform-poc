// Package postgres は orchestration スキーマのリポジトリ。プロセスは Process に変換し、サガ・BillingRun・一括解約は sqlc の行をそのまま使う
// （Orchestration の状態は「どこまで進めたか」の記録だけで、業務の集約を持たないため）。
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"subscription-platform/internal/platform/db"
	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/orchestration/internal/adapter/postgres/sqlcgen"
	"subscription-platform/modules/orchestration/internal/domain"
)

var (
	ErrNotFound        = errors.New("orchestration: row not found")
	ErrVersionConflict = errors.New("orchestration: version conflict")
	// ErrActiveExists は同じ契約・請求に終端でないプロセスがある（不変条件 1・2）。
	ErrActiveExists = errors.New("orchestration: active process exists")
	// ErrRunDispatching は同じ課金日の BillingRun が投入中。
	ErrRunDispatching = errors.New("orchestration: billing run dispatching")
)

// Repo は 1 トランザクション分のリポジトリ。
type Repo struct{ Q *sqlcgen.Queries }

func New(tx db.Tx) *Repo { return &Repo{Q: sqlcgen.New(tx)} }

// NotFound は sqlc の ErrNoRows を ErrNotFound にする。
func NotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func uniqueViolation(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return pe.ConstraintName
	}
	return ""
}

// Process は BillingCycleProcess（§3.1）。
type Process struct {
	ID             uuid.UUID
	TenantID       sk.TenantID
	SubscriptionID sk.SubscriptionID
	InvoiceID      *sk.InvoiceID
	Kind           domain.Kind
	State          domain.State
	Outcome        *domain.Outcome
	RunDate        *sk.Date
	BillingRunID   *uuid.UUID
	CollectionSeq  *int
	AttemptID      *uuid.UUID
	AttemptStatus  *string
	LastError      *string
	ResumeCount    int
	CorrelationID  string
	Version        int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Active は判定用の要約。
func (p *Process) Active() *domain.ActiveProcess {
	if p == nil {
		return nil
	}
	return &domain.ActiveProcess{State: p.State, CollectionSeq: p.CollectionSeq}
}

// Complete は outcome で閉じる。
func (p *Process) Complete(o domain.Outcome) {
	p.State, p.Outcome = domain.StateCompleted, &o
}

func toProcess(r sqlcgen.OrchestrationBillingCycleProcess) *Process {
	p := &Process{
		ID: r.ID, TenantID: sk.IDFromUUID[sk.TenantTag](r.TenantID), SubscriptionID: sk.IDFromUUID[sk.SubscriptionTag](r.SubscriptionID),
		Kind: domain.Kind(r.Kind), State: domain.State(r.State), RunDate: r.RunDate, BillingRunID: r.BillingRunID, AttemptID: r.AttemptID,
		AttemptStatus: r.AttemptStatus, LastError: r.LastError, ResumeCount: int(r.ResumeCount), CorrelationID: r.CorrelationID,
		Version: int(r.Version), CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
	if r.InvoiceID != nil {
		id := sk.IDFromUUID[sk.InvoiceTag](*r.InvoiceID)
		p.InvoiceID = &id
	}
	if r.Outcome != nil {
		o := domain.Outcome(*r.Outcome)
		p.Outcome = &o
	}
	if r.CollectionSeq != nil {
		n := int(*r.CollectionSeq)
		p.CollectionSeq = &n
	}
	return p
}

func invoiceUUID(id *sk.InvoiceID) *uuid.UUID {
	if id == nil {
		return nil
	}
	u := id.UUID()
	return &u
}

func seq32(n *int) *int32 {
	if n == nil {
		return nil
	}
	v := int32(*n) //nolint:gosec // 回収要求の連番は小さい
	return &v
}

// InsertProcess はプロセスを作る。同じ契約・請求に進行中のプロセスがあれば ErrActiveExists。
func (r *Repo) InsertProcess(ctx context.Context, p *Process, now time.Time) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.Must(uuid.NewV7())
	}
	err := r.Q.InsertProcess(ctx, sqlcgen.InsertProcessParams{
		ID: p.ID, TenantID: p.TenantID.UUID(), SubscriptionID: p.SubscriptionID.UUID(), InvoiceID: invoiceUUID(p.InvoiceID), Kind: string(p.Kind),
		State: string(p.State), RunDate: p.RunDate, BillingRunID: p.BillingRunID, CollectionSeq: seq32(p.CollectionSeq), CorrelationID: p.CorrelationID, CreatedAt: now,
	})
	if c := uniqueViolation(err); c == "bcp_active_subscription" || c == "bcp_active_invoice" {
		return ErrActiveExists
	}
	if err != nil {
		return err
	}
	p.Version, p.CreatedAt, p.UpdatedAt = 1, now, now
	return nil
}

// UpdateProcess は楽観ロックで保存する。
func (r *Repo) UpdateProcess(ctx context.Context, p *Process, now time.Time) error {
	var outcome *string
	var completed *time.Time
	if p.Outcome != nil {
		o := string(*p.Outcome)
		outcome = &o
	}
	if p.State.Terminal() {
		completed = &now
	}
	n, err := r.Q.UpdateProcess(ctx, sqlcgen.UpdateProcessParams{
		ID: p.ID, InvoiceID: invoiceUUID(p.InvoiceID), State: string(p.State), Outcome: outcome, CollectionSeq: seq32(p.CollectionSeq), AttemptID: p.AttemptID,
		AttemptStatus: p.AttemptStatus, LastError: p.LastError, ResumeCount: int32(p.ResumeCount), CompletedAt: completed, UpdatedAt: now, Version: int32(p.Version), //nolint:gosec // 小さい
	})
	if c := uniqueViolation(err); c == "bcp_active_invoice" {
		return ErrActiveExists
	}
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrVersionConflict
	}
	p.Version++
	p.UpdatedAt = now
	return nil
}

// GetProcess は ID で読む。
func (r *Repo) GetProcess(ctx context.Context, id uuid.UUID) (*Process, error) {
	row, err := r.Q.GetProcess(ctx, id)
	if err != nil {
		return nil, NotFound(err)
	}
	return toProcess(row), nil
}

// ActiveBySubscription は契約の進行中のプロセス（なければ nil）。
func (r *Repo) ActiveBySubscription(ctx context.Context, sub sk.SubscriptionID) (*Process, error) {
	row, err := r.Q.GetActiveProcessBySubscription(ctx, sub.UUID())
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toProcess(row), nil
}

// ActiveByInvoice は請求の進行中のプロセス（なければ nil）。
func (r *Repo) ActiveByInvoice(ctx context.Context, inv sk.InvoiceID) (*Process, error) {
	u := inv.UUID()
	row, err := r.Q.GetActiveProcessByInvoice(ctx, &u)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toProcess(row), nil
}

// Stalled は updated_at が before より古い進行中のプロセス（BYPASSRLS のスコープ）。
func (r *Repo) Stalled(ctx context.Context, before time.Time, limit int) ([]*Process, error) {
	rows, err := r.Q.ListStalledProcesses(ctx, sqlcgen.ListStalledProcessesParams{UpdatedAt: before, Limit: int32(limit)}) //nolint:gosec // 500
	if err != nil {
		return nil, err
	}
	out := make([]*Process, len(rows))
	for i, row := range rows {
		out[i] = toProcess(row)
	}
	return out, nil
}

// BySubscription は契約のプロセス（作成順。テストと調査用）。
func (r *Repo) BySubscription(ctx context.Context, sub sk.SubscriptionID) ([]*Process, error) {
	rows, err := r.Q.ListProcessesBySubscription(ctx, sub.UUID())
	if err != nil {
		return nil, err
	}
	out := make([]*Process, len(rows))
	for i, row := range rows {
		out[i] = toProcess(row)
	}
	return out, nil
}

// InsertBillingRun は BillingRun を作る（seq は同じ課金日の最大 + 1）。投入中のものがあれば ErrRunDispatching。
func (r *Repo) InsertBillingRun(ctx context.Context, id uuid.UUID, date sk.Date, trigger string, tenant *uuid.UUID, by *string, now time.Time) error {
	err := r.Q.InsertBillingRun(ctx, sqlcgen.InsertBillingRunParams{ID: id, RunDate: date, Trigger: trigger, OnlyTenantID: tenant, RequestedBy: by, StartedAt: now})
	switch uniqueViolation(err) {
	case "br_one_dispatching":
		return ErrRunDispatching
	case "billing_runs_run_date_seq_key":
		return ErrVersionConflict // 同時の作成。呼び出し元が再試行する
	}
	return err
}
