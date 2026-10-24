// Package postgres は billing スキーマのリポジトリ（sqlc の生成コードを包み、domain の型に変換する）。
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
	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/billing/contracts"
	"subscription-platform/modules/billing/internal/adapter/postgres/sqlcgen"
	"subscription-platform/modules/billing/internal/domain"
)

var (
	ErrNotFound        = errors.New("billing: row not found")
	ErrVersionConflict = errors.New("billing: version conflict")
	// ErrPeriodExists は同じ (subscription_id, period_start) の請求がある（invoices_period）。
	ErrPeriodExists = errors.New("billing: invoice for period exists")
	// ErrUnsettledExists は同じ契約に非終端の請求がある（invoices_one_unsettled）。
	ErrUnsettledExists = errors.New("billing: unsettled invoice exists")
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

func uuidPtr[T any](id *sk.ID[T]) *uuid.UUID {
	if id == nil {
		return nil
	}
	u := id.UUID()
	return &u
}

func idPtr[T any](u *uuid.UUID) *sk.ID[T] {
	if u == nil {
		return nil
	}
	id := sk.IDFromUUID[T](*u)
	return &id
}

func strPtr[S ~string](s *S) *string {
	if s == nil {
		return nil
	}
	v := string(*s)
	return &v
}

func enumPtr[S ~string](s *string) *S {
	if s == nil {
		return nil
	}
	v := S(*s)
	return &v
}

func jsonOrNil(v any, isNil bool) ([]byte, error) {
	if isNil {
		return nil, nil
	}
	return json.Marshal(v)
}

type taxJSON struct {
	RateNum       int64 `json:"rate_num"`
	RateDen       int64 `json:"rate_den"`
	TaxableAmount int64 `json:"taxable_amount"`
	TaxAmount     int64 `json:"tax_amount"`
}

func rate(num, den int64) (sk.TaxRate, error) {
	r, err := sk.NewTaxRate(num, den)
	if err != nil {
		return sk.TaxRate{}, fmt.Errorf("billing: tax rate %d/%d: %w", num, den, err)
	}
	return r, nil
}

// ---- invoices ----

func (r *Repo) toInvoice(row sqlcgen.BillingInvoice, lines []sqlcgen.BillingInvoiceLine) (*domain.Invoice, error) {
	s := domain.State{
		ID: sk.IDFromUUID[sk.InvoiceTag](row.ID), TenantID: sk.IDFromUUID[sk.TenantTag](row.TenantID),
		SubscriptionID: sk.IDFromUUID[sk.SubscriptionTag](row.SubscriptionID), CustomerAccountID: sk.IDFromUUID[sk.CustomerAccountTag](row.CustomerAccountID),
		Kind: c.InvoiceKind(row.Kind), Mode: c.CollectionMode(row.CollectionMode), Period: sk.BillingPeriod{Start: row.PeriodStart, End: row.PeriodEnd},
		Status: c.InvoiceStatus(row.Status), Subtotal: sk.MustMoney(int64(row.SubtotalAmount)), DiscountTotal: sk.MustMoney(int64(row.DiscountAmount)),
		Total: sk.MustMoney(int64(row.TotalAmount)), IssuedAt: row.IssuedAt.UTC(), PaidAt: utc(row.PaidAt), PaidAttemptID: idPtr[sk.PaymentAttemptTag](row.PaidAttemptID),
		PaidVia: row.PaidVia, UncollectibleAt: utc(row.UncollectibleAt), VoidedAt: utc(row.VoidedAt), VoidReason: enumPtr[c.VoidReason](row.VoidReason),
		VoidRequestedAt: utc(row.VoidRequestedAt), VoidRequestReason: enumPtr[c.VoidReason](row.VoidRequestedReason), AttemptCount: int(row.AttemptCount),
		DunningStage: int(row.DunningStage), NextCollectionAt: utc(row.NextCollectionAt), CollectionSeq: int(row.CollectionSeq), LeaseUntil: utc(row.CollectionLeaseUntil),
		LastAttemptID: idPtr[sk.PaymentAttemptTag](row.LastAttemptID), LastFailureCode: row.LastFailureCode, LastRetryable: row.LastFailureRetryable, Version: int(row.Version),
	}
	var tax []taxJSON
	if err := json.Unmarshal(row.TaxBreakdown, &tax); err != nil {
		return nil, fmt.Errorf("billing: tax_breakdown: %w", err)
	}
	for _, t := range tax {
		rt, err := rate(t.RateNum, t.RateDen)
		if err != nil {
			return nil, err
		}
		s.Tax = append(s.Tax, c.TaxLine{Rate: rt, TaxableAmount: sk.MustMoney(t.TaxableAmount), TaxAmount: sk.MustMoney(t.TaxAmount)})
	}
	if row.DunningSchedule != nil {
		s.Dunning = &domain.DunningSchedule{}
		if err := json.Unmarshal(row.DunningSchedule, s.Dunning); err != nil {
			return nil, fmt.Errorf("billing: dunning_schedule: %w", err)
		}
	}
	if row.PendingRequest != nil {
		s.Pending = &domain.PendingRequest{}
		if err := json.Unmarshal(row.PendingRequest, s.Pending); err != nil {
			return nil, fmt.Errorf("billing: pending_request: %w", err)
		}
	}
	for _, l := range lines {
		rt, err := rate(int64(l.TaxRateNum), int64(l.TaxRateDen))
		if err != nil {
			return nil, err
		}
		line := domain.Line{
			LineNo: int(l.LineNo), Kind: l.Kind, Description: l.Description, PriceVersionID: idPtr[sk.PriceVersionTag](l.PriceVersionID), Quantity: int(l.Quantity),
			UnitAmount: sk.MustMoney(int64(l.UnitAmount)), Amount: sk.MustMoney(int64(l.Amount)), TaxRate: rt, Period: sk.BillingPeriod{Start: l.PeriodStart, End: l.PeriodEnd},
		}
		if l.DiscountSnapshot != nil {
			line.Discount = &domain.DiscountSnapshot{}
			if err := json.Unmarshal(l.DiscountSnapshot, line.Discount); err != nil {
				return nil, fmt.Errorf("billing: discount_snapshot: %w", err)
			}
		}
		s.Lines = append(s.Lines, line)
	}
	return domain.Restore(s), nil
}

func (r *Repo) load(ctx context.Context, row sqlcgen.BillingInvoice, err error) (*domain.Invoice, error) {
	if err != nil {
		return nil, notFound(err)
	}
	lines, err := r.q.ListInvoiceLines(ctx, row.ID)
	if err != nil {
		return nil, fmt.Errorf("billing: lines: %w", err)
	}
	return r.toInvoice(row, lines)
}

// Get は請求を読む（ロックなし）。
func (r *Repo) Get(ctx context.Context, id sk.InvoiceID) (*domain.Invoice, error) {
	row, err := r.q.GetInvoice(ctx, id.UUID())
	return r.load(ctx, row, err)
}

// GetForUpdate は請求を行ロックして読む。
func (r *Repo) GetForUpdate(ctx context.Context, id sk.InvoiceID) (*domain.Invoice, error) {
	row, err := r.q.GetInvoiceForUpdate(ctx, id.UUID())
	return r.load(ctx, row, err)
}

// GetForUpdateSkipLocked は他のトランザクションがロック中なら ErrNotFound（回収時刻ジョブ）。
func (r *Repo) GetForUpdateSkipLocked(ctx context.Context, id sk.InvoiceID) (*domain.Invoice, error) {
	row, err := r.q.GetInvoiceForUpdateSkipLocked(ctx, id.UUID())
	return r.load(ctx, row, err)
}

// GetByPeriod は請求キー（subscription_id, period_start）で読む。
func (r *Repo) GetByPeriod(ctx context.Context, sub sk.SubscriptionID, start sk.Date) (*domain.Invoice, error) {
	row, err := r.q.GetInvoiceByPeriod(ctx, sqlcgen.GetInvoiceByPeriodParams{SubscriptionID: sub.UUID(), PeriodStart: start})
	return r.load(ctx, row, err)
}

// GetUnsettled は契約の非終端の請求。forUpdate なら行ロックする。
func (r *Repo) GetUnsettled(ctx context.Context, sub sk.SubscriptionID, forUpdate bool) (*domain.Invoice, error) {
	if forUpdate {
		row, err := r.q.GetUnsettledInvoiceForUpdate(ctx, sub.UUID())
		return r.load(ctx, row, err)
	}
	row, err := r.q.GetUnsettledInvoice(ctx, sub.UUID())
	return r.load(ctx, row, err)
}

// InvoiceTenant は請求の事業者（BYPASSRLS のスコープで使う）。
func (r *Repo) InvoiceTenant(ctx context.Context, id sk.InvoiceID) (sk.TenantID, error) {
	t, err := r.q.GetInvoiceTenant(ctx, id.UUID())
	if err != nil {
		return sk.TenantID{}, notFound(err)
	}
	return sk.IDFromUUID[sk.TenantTag](t), nil
}

// SubscriptionTenant は契約の請求の事業者。請求がなければ ErrNotFound。
func (r *Repo) SubscriptionTenant(ctx context.Context, sub sk.SubscriptionID) (sk.TenantID, error) {
	t, err := r.q.GetSubscriptionTenant(ctx, sub.UUID())
	if err != nil {
		return sk.TenantID{}, notFound(err)
	}
	return sk.IDFromUUID[sk.TenantTag](t), nil
}

type stateJSON struct {
	dunning, pending, tax []byte
}

func encodeState(s domain.State) (stateJSON, error) {
	var out stateJSON
	var err error
	if out.dunning, err = jsonOrNil(s.Dunning, s.Dunning == nil); err != nil {
		return out, err
	}
	if out.pending, err = jsonOrNil(s.Pending, s.Pending == nil); err != nil {
		return out, err
	}
	tax := make([]taxJSON, len(s.Tax))
	for i, t := range s.Tax {
		tax[i] = taxJSON{RateNum: t.Rate.Num(), RateDen: t.Rate.Den(), TaxableAmount: t.TaxableAmount.Int64(), TaxAmount: t.TaxAmount.Int64()}
	}
	out.tax, err = json.Marshal(tax)
	return out, err
}

// Insert は新しい請求と明細を保存する。一意制約の違反は ErrPeriodExists / ErrUnsettledExists。
func (r *Repo) Insert(ctx context.Context, x *domain.Invoice) error {
	s := x.State()
	js, err := encodeState(s)
	if err != nil {
		return err
	}
	var taxAmount int64
	for _, t := range s.Tax {
		taxAmount += t.TaxAmount.Int64()
	}
	err = r.q.InsertInvoice(ctx, sqlcgen.InsertInvoiceParams{
		ID: s.ID.UUID(), TenantID: s.TenantID.UUID(), SubscriptionID: s.SubscriptionID.UUID(), CustomerAccountID: s.CustomerAccountID.UUID(),
		Kind: string(s.Kind), CollectionMode: string(s.Mode), PeriodStart: s.Period.Start, PeriodEnd: s.Period.End, Status: string(s.Status),
		SubtotalAmount: int32(s.Subtotal.Int64()), DiscountAmount: int32(s.DiscountTotal.Int64()), TotalAmount: int32(s.Total.Int64()), //nolint:gosec // 1,000,000 以下
		TaxAmount: int32(taxAmount), TaxBreakdown: js.tax, IssuedAt: s.IssuedAt, PaidAt: s.PaidAt, PaidAttemptID: uuidPtr(s.PaidAttemptID), PaidVia: s.PaidVia, //nolint:gosec // 1,000,000 以下
		NextCollectionAt: s.NextCollectionAt, CollectionSeq: int32(s.CollectionSeq), PendingRequest: js.pending, CollectionLeaseUntil: s.LeaseUntil, //nolint:gosec // 小さい
	})
	switch constraint(err) {
	case "":
	case "invoices_period":
		return ErrPeriodExists
	case "invoices_one_unsettled":
		return ErrUnsettledExists
	default:
		return err
	}
	if err != nil {
		return err
	}
	for _, l := range s.Lines {
		var snap []byte
		if l.Discount != nil {
			if snap, err = json.Marshal(l.Discount); err != nil {
				return err
			}
		}
		if err := r.q.InsertInvoiceLine(ctx, sqlcgen.InsertInvoiceLineParams{
			InvoiceID: s.ID.UUID(), LineNo: int16(l.LineNo), TenantID: s.TenantID.UUID(), Kind: l.Kind, Description: l.Description, //nolint:gosec // 明細は数行
			PriceVersionID: uuidPtr(l.PriceVersionID), DiscountSnapshot: snap, Quantity: int32(l.Quantity), UnitAmount: int32(l.UnitAmount.Int64()), //nolint:gosec // 1,000,000 以下
			Amount: int32(l.Amount.Int64()), TaxRateNum: int32(l.TaxRate.Num()), TaxRateDen: int32(l.TaxRate.Den()), PeriodStart: l.Period.Start, PeriodEnd: l.Period.End, //nolint:gosec // 1,000,000 以下
		}); err != nil {
			return fmt.Errorf("billing: insert line: %w", err)
		}
	}
	return nil
}

// Update は請求の可変な列を保存する（楽観ロック）。
func (r *Repo) Update(ctx context.Context, x *domain.Invoice, now time.Time) error {
	s := x.State()
	js, err := encodeState(s)
	if err != nil {
		return err
	}
	n, err := r.q.UpdateInvoice(ctx, sqlcgen.UpdateInvoiceParams{
		ID: s.ID.UUID(), Status: string(s.Status), PaidAt: s.PaidAt, PaidAttemptID: uuidPtr(s.PaidAttemptID), PaidVia: s.PaidVia,
		UncollectibleAt: s.UncollectibleAt, VoidedAt: s.VoidedAt, VoidReason: strPtr(s.VoidReason), VoidRequestedAt: s.VoidRequestedAt,
		VoidRequestedReason: strPtr(s.VoidRequestReason), AttemptCount: int32(s.AttemptCount), DunningStage: int32(s.DunningStage), DunningSchedule: js.dunning, //nolint:gosec // 小さい
		NextCollectionAt: s.NextCollectionAt, CollectionSeq: int32(s.CollectionSeq), PendingRequest: js.pending, CollectionLeaseUntil: s.LeaseUntil, //nolint:gosec // 小さい
		LastAttemptID: uuidPtr(s.LastAttemptID), LastFailureCode: s.LastFailureCode, LastFailureRetryable: s.LastRetryable, UpdatedAt: now, Version: int32(s.Version), //nolint:gosec // 小さい
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrVersionConflict
	}
	return nil
}

// Page は一覧の 1 ページ。
type Page struct {
	AfterStart *sk.Date
	AfterID    *uuid.UUID
	Limit      int
}

// ListBySubscription は契約の請求を period_start の新しい順で返す（RLS で見える範囲だけ）。
func (r *Repo) ListBySubscription(ctx context.Context, sub sk.SubscriptionID, p Page) ([]*domain.Invoice, error) {
	rows, err := r.q.ListSubscriptionInvoices(ctx, sqlcgen.ListSubscriptionInvoicesParams{
		SubscriptionID: sub.UUID(), Limit: int32(p.Limit), AfterPeriodStart: p.AfterStart, AfterID: p.AfterID, //nolint:gosec // 101 以下
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	lines, err := r.q.ListInvoiceLinesFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	byInv := map[uuid.UUID][]sqlcgen.BillingInvoiceLine{}
	for _, l := range lines {
		byInv[l.InvoiceID] = append(byInv[l.InvoiceID], l)
	}
	out := make([]*domain.Invoice, len(rows))
	for i, row := range rows {
		if out[i], err = r.toInvoice(row, byInv[row.ID]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// DueRef は回収時刻ジョブの対象。
type DueRef struct {
	ID       sk.InvoiceID
	TenantID sk.TenantID
	At       time.Time
}

// ListDue は回収時刻が来た請求をキーセットで返す（BYPASSRLS のスコープ）。
func (r *Repo) ListDue(ctx context.Context, now time.Time, afterAt *time.Time, afterID *uuid.UUID, limit int) ([]DueRef, error) {
	rows, err := r.q.ListDueInvoices(ctx, sqlcgen.ListDueInvoicesParams{Now: now, AfterAt: afterAt, AfterID: afterID, Lim: int32(limit)}) //nolint:gosec // 500
	if err != nil {
		return nil, err
	}
	out := make([]DueRef, len(rows))
	for i, row := range rows {
		out[i] = DueRef{ID: sk.IDFromUUID[sk.InvoiceTag](row.ID), TenantID: sk.IDFromUUID[sk.TenantTag](row.TenantID), At: row.NextCollectionAt.UTC()}
	}
	return out, nil
}

// ---- invoice_payment_records ----

// Record は結果の記録の 1 行。
type Record struct {
	AttemptID   sk.PaymentAttemptID
	Outcome     c.OutcomeStatus
	InvoiceID   sk.InvoiceID
	TenantID    sk.TenantID
	Amount      int64
	FailureCode *string
	Retryable   *bool
	Source      c.OutcomeSource
	Applied     bool
	Anomaly     string
	RecordedAt  time.Time
}

// InsertRecord は結果を追記する。同じ (attempt_id, outcome) が既にあれば false。
func (r *Repo) InsertRecord(ctx context.Context, rec Record) (bool, error) {
	var anomaly *string
	if rec.Anomaly != "" {
		anomaly = &rec.Anomaly
	}
	n, err := r.q.InsertPaymentRecord(ctx, sqlcgen.InsertPaymentRecordParams{
		AttemptID: rec.AttemptID.UUID(), Outcome: string(rec.Outcome), InvoiceID: rec.InvoiceID.UUID(), TenantID: rec.TenantID.UUID(),
		Amount: int32(rec.Amount), FailureCode: rec.FailureCode, Retryable: rec.Retryable, Source: string(rec.Source), Applied: rec.Applied, //nolint:gosec // 1,000,000 以下
		Anomaly: anomaly, RecordedAt: rec.RecordedAt,
	})
	return n == 1, err
}

// AttemptFinalized はこの試行に確定結果（succeeded / failed）が記録済みか。
func (r *Repo) AttemptFinalized(ctx context.Context, a sk.PaymentAttemptID) (bool, error) {
	return r.q.AttemptFinalized(ctx, a.UUID())
}

// ---- dunning_policies ----

// GetPolicy は事業者の方針。行がなければデフォルト（DP-4）。
func (r *Repo) GetPolicy(ctx context.Context, t sk.TenantID) (domain.DunningPolicy, error) {
	row, err := r.q.GetDunningPolicy(ctx, t.UUID())
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DefaultPolicy(t), nil
	}
	if err != nil {
		return domain.DunningPolicy{}, err
	}
	return toPolicy(row), nil
}

func toPolicy(row sqlcgen.BillingDunningPolicy) domain.DunningPolicy {
	offs := make([]int, len(row.RetryOffsetsDays))
	for i, d := range row.RetryOffsetsDays {
		offs[i] = int(d)
	}
	at := row.UpdatedAt.UTC()
	return domain.DunningPolicy{TenantID: sk.IDFromUUID[sk.TenantTag](row.TenantID), Offsets: offs, Version: int(row.Version), UpdatedAt: &at, UpdatedBy: row.UpdatedBy}
}

// UpsertPolicy は方針を保存する。
func (r *Repo) UpsertPolicy(ctx context.Context, t sk.TenantID, offsets []int, by string, now time.Time) (domain.DunningPolicy, error) {
	offs := make([]int32, len(offsets))
	for i, d := range offsets {
		offs[i] = int32(d) //nolint:gosec // 1〜60
	}
	row, err := r.q.UpsertDunningPolicy(ctx, sqlcgen.UpsertDunningPolicyParams{TenantID: t.UUID(), RetryOffsetsDays: offs, UpdatedBy: by, CreatedAt: now})
	if err != nil {
		return domain.DunningPolicy{}, err
	}
	return toPolicy(row), nil
}
