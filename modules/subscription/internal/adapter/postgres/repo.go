// Package postgres は subscription スキーマのリポジトリ（sqlc の生成コードを包み、domain の型に変換する）。
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
	c "subscription-platform/modules/subscription/contracts"
	"subscription-platform/modules/subscription/internal/adapter/postgres/sqlcgen"
	"subscription-platform/modules/subscription/internal/domain"
)

var (
	ErrNotFound         = errors.New("subscription: row not found")
	ErrDuplicateSignup  = errors.New("subscription: duplicate signup request")
	ErrTrialAlreadyUsed = errors.New("subscription: trial already used")
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

func strPtr[T ~string](v *T) *string {
	if v == nil {
		return nil
	}
	s := string(*v)
	return &s
}

func idPtr[T any](u *uuid.UUID) *sk.ID[T] {
	if u == nil {
		return nil
	}
	id := sk.IDFromUUID[T](*u)
	return &id
}

func uuidPtr[T any](id *sk.ID[T]) *uuid.UUID {
	if id == nil {
		return nil
	}
	u := id.UUID()
	return &u
}

func toState(r sqlcgen.SubscriptionSubscription) (domain.State, error) {
	s := domain.State{
		ID: sk.IDFromUUID[sk.SubscriptionTag](r.ID), TenantID: sk.IDFromUUID[sk.TenantTag](r.TenantID),
		CustomerAccountID: sk.IDFromUUID[sk.CustomerAccountTag](r.CustomerAccountID), TenantCustomerID: r.TenantCustomerID,
		SignupRequestID: r.SignupRequestID, ProductID: sk.IDFromUUID[sk.ProductTag](r.ProductID), PlanID: sk.IDFromUUID[sk.PlanTag](r.PlanID),
		PriceVersionID: sk.IDFromUUID[sk.PriceVersionTag](r.PriceVersionID), Interval: sk.BillingInterval(r.BillingInterval), Quantity: int(r.Quantity),
		Status: c.Status(r.Status), SignupDate: r.SignupDate, CurrentPeriod: sk.BillingPeriod{Start: r.CurrentPeriodStart, End: r.CurrentPeriodEnd},
		NextRenewalDate: r.NextRenewalDate, AnchorDay: int(r.BillingAnchorDay), TrialStart: r.TrialStart, TrialEnd: r.TrialEnd,
		TrialWillEndNotified: r.TrialWillEndNotified, PaymentMethodID: sk.IDFromUUID[sk.PaymentMethodTag](r.PaymentMethodID),
		PastDueInvoiceID: idPtr[sk.InvoiceTag](r.PastDueInvoiceID), LastPaidInvoiceID: idPtr[sk.InvoiceTag](r.LastPaidInvoiceID),
		SignupFailureReason: r.SignupFailureReason, Version: int(r.Version), CreatedAt: r.CreatedAt.UTC(),
	}
	if r.CanceledAt != nil {
		t := r.CanceledAt.UTC()
		s.CanceledAt = &t
	}
	if r.CancelReason != nil {
		cr := c.CancelReason(*r.CancelReason)
		s.CancelReason = &cr
	}
	if r.PendingPlanID != nil {
		s.PendingPlanChange = &c.PendingPlanChange{
			PlanID: sk.IDFromUUID[sk.PlanTag](*r.PendingPlanID), PriceVersionID: sk.IDFromUUID[sk.PriceVersionTag](*r.PendingPriceVersionID),
			Interval: sk.BillingInterval(*r.PendingPlanInterval), EffectiveDate: *r.PendingPlanEffectiveDate,
		}
	}
	if r.CancellationSource != nil {
		s.Cancellation = &c.Cancellation{
			Source: c.CancellationSource(*r.CancellationSource), Reason: c.CancelReason(*r.CancellationReason), EffectiveDate: *r.CancellationEffectiveDate,
		}
	}
	if len(r.PendingRenewal) > 0 {
		s.PendingRenewal = &domain.PendingRenewal{}
		if err := json.Unmarshal(r.PendingRenewal, s.PendingRenewal); err != nil {
			return s, fmt.Errorf("subscription: pending_renewal: %w", err)
		}
	}
	if len(r.AppliedDiscount) > 0 {
		s.AppliedDiscount = &c.AppliedDiscount{}
		if err := json.Unmarshal(r.AppliedDiscount, s.AppliedDiscount); err != nil {
			return s, fmt.Errorf("subscription: applied_discount: %w", err)
		}
	}
	return s, nil
}

// columns は保存する列の値（Insert と Update で共通）。
type columns struct {
	pendingPlanID, pendingPV        *uuid.UUID
	pendingInterval                 *string
	pendingEffective                *sk.Date
	cancelSource, cancelReasonSched *string
	cancelEffective                 *sk.Date
	pendingRenewal, appliedDiscount []byte
}

func columnsOf(s domain.State) columns {
	var col columns
	if pc := s.PendingPlanChange; pc != nil {
		p, v, iv, e := pc.PlanID.UUID(), pc.PriceVersionID.UUID(), string(pc.Interval), pc.EffectiveDate
		col.pendingPlanID, col.pendingPV, col.pendingInterval, col.pendingEffective = &p, &v, &iv, &e
	}
	if cn := s.Cancellation; cn != nil {
		src, rs, e := string(cn.Source), string(cn.Reason), cn.EffectiveDate
		col.cancelSource, col.cancelReasonSched, col.cancelEffective = &src, &rs, &e
	}
	if s.PendingRenewal != nil {
		col.pendingRenewal, _ = json.Marshal(s.PendingRenewal)
	}
	if s.AppliedDiscount != nil {
		col.appliedDiscount, _ = json.Marshal(s.AppliedDiscount)
	}
	return col
}

func (r *Repo) Get(ctx context.Context, id sk.SubscriptionID) (*domain.Subscription, error) {
	row, err := r.q.GetSubscription(ctx, id.UUID())
	if err != nil {
		return nil, notFound(err)
	}
	st, err := toState(row)
	if err != nil {
		return nil, err
	}
	return domain.Restore(st), nil
}

func (r *Repo) GetBySignupRequest(ctx context.Context, customer sk.CustomerAccountID, reqID string) (*domain.Subscription, error) {
	row, err := r.q.GetBySignupRequest(ctx, sqlcgen.GetBySignupRequestParams{CustomerAccountID: customer.UUID(), SignupRequestID: reqID})
	if err != nil {
		return nil, notFound(err)
	}
	st, err := toState(row)
	if err != nil {
		return nil, err
	}
	return domain.Restore(st), nil
}

// Insert は新しい契約を保存する。同じ (customer, signup_request_id) があれば ErrDuplicateSignup。
func (r *Repo) Insert(ctx context.Context, x *domain.Subscription, now time.Time) error {
	s := x.State()
	col := columnsOf(s)
	err := r.q.InsertSubscription(ctx, sqlcgen.InsertSubscriptionParams{
		ID: s.ID.UUID(), TenantID: s.TenantID.UUID(), CustomerAccountID: s.CustomerAccountID.UUID(), TenantCustomerID: s.TenantCustomerID,
		SignupRequestID: s.SignupRequestID, ProductID: s.ProductID.UUID(), PlanID: s.PlanID.UUID(), PriceVersionID: s.PriceVersionID.UUID(),
		BillingInterval: string(s.Interval), Quantity: int32(s.Quantity), Status: string(s.Status), SignupDate: s.SignupDate,
		CurrentPeriodStart: s.CurrentPeriod.Start, CurrentPeriodEnd: s.CurrentPeriod.End, NextRenewalDate: s.NextRenewalDate,
		BillingAnchorDay: int16(s.AnchorDay), TrialStart: s.TrialStart, TrialEnd: s.TrialEnd, TrialWillEndNotified: s.TrialWillEndNotified,
		PendingPlanID: col.pendingPlanID, PendingPriceVersionID: col.pendingPV, PendingPlanInterval: col.pendingInterval,
		PendingPlanEffectiveDate: col.pendingEffective, CancelAtPeriodEnd: s.Cancellation != nil, CancellationSource: col.cancelSource,
		CancellationReason: col.cancelReasonSched, CancellationEffectiveDate: col.cancelEffective, PendingRenewal: col.pendingRenewal,
		AppliedDiscount: col.appliedDiscount, PaymentMethodID: s.PaymentMethodID.UUID(), PastDueInvoiceID: uuidPtr(s.PastDueInvoiceID),
		LastPaidInvoiceID: uuidPtr(s.LastPaidInvoiceID), CancelReason: strPtr(s.CancelReason), CanceledAt: s.CanceledAt,
		SignupFailureReason: s.SignupFailureReason, CreatedAt: now,
	})
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return ErrDuplicateSignup
	}
	return err
}

// Update は楽観ロックで保存する。衝突したら contracts.ErrVersionConflict。from は保存前の状態（domain.state_changed のログ）。
func (r *Repo) Update(ctx context.Context, x *domain.Subscription, from c.Status, now time.Time) error {
	s := x.State()
	col := columnsOf(s)
	n, err := r.q.UpdateSubscription(ctx, sqlcgen.UpdateSubscriptionParams{
		ID: s.ID.UUID(), ProductID: s.ProductID.UUID(), PlanID: s.PlanID.UUID(), PriceVersionID: s.PriceVersionID.UUID(),
		BillingInterval: string(s.Interval), Quantity: int32(s.Quantity), Status: string(s.Status),
		CurrentPeriodStart: s.CurrentPeriod.Start, CurrentPeriodEnd: s.CurrentPeriod.End, NextRenewalDate: s.NextRenewalDate,
		TrialStart: s.TrialStart, TrialEnd: s.TrialEnd, TrialWillEndNotified: s.TrialWillEndNotified,
		PendingPlanID: col.pendingPlanID, PendingPriceVersionID: col.pendingPV, PendingPlanInterval: col.pendingInterval,
		PendingPlanEffectiveDate: col.pendingEffective, CancelAtPeriodEnd: s.Cancellation != nil, CancellationSource: col.cancelSource,
		CancellationReason: col.cancelReasonSched, CancellationEffectiveDate: col.cancelEffective, PendingRenewal: col.pendingRenewal,
		AppliedDiscount: col.appliedDiscount, PaymentMethodID: s.PaymentMethodID.UUID(), PastDueInvoiceID: uuidPtr(s.PastDueInvoiceID),
		LastPaidInvoiceID: uuidPtr(s.LastPaidInvoiceID), CancelReason: strPtr(s.CancelReason), CanceledAt: s.CanceledAt,
		SignupFailureReason: s.SignupFailureReason, UpdatedAt: now, Version: int32(s.Version),
	})
	if err != nil {
		return fmt.Errorf("subscription: update: %w", err)
	}
	if n == 0 {
		return c.ErrVersionConflict
	}
	reason := ""
	if s.CancelReason != nil {
		reason = string(*s.CancelReason)
	}
	observability.StateChanged(ctx, "subscription", s.ID.String(), string(from), string(s.Status), reason)
	return nil
}

func (r *Repo) HasTrialUsage(ctx context.Context, customer sk.CustomerAccountID, product sk.ProductID) (bool, error) {
	return r.q.HasTrialUsage(ctx, sqlcgen.HasTrialUsageParams{CustomerAccountID: customer.UUID(), ProductID: product.UUID()})
}

// InsertTrialUsage はトライアルの利用を記録する。同時申込で先に記録されていれば ErrTrialAlreadyUsed。
func (r *Repo) InsertTrialUsage(ctx context.Context, s domain.State, now time.Time) error {
	err := r.q.InsertTrialUsage(ctx, sqlcgen.InsertTrialUsageParams{
		CustomerAccountID: s.CustomerAccountID.UUID(), ProductID: s.ProductID.UUID(), TenantID: s.TenantID.UUID(), SubscriptionID: s.ID.UUID(),
		UsedOn: s.SignupDate, CreatedAt: now,
	})
	if db.IsUniqueViolation(err) {
		return ErrTrialAlreadyUsed
	}
	return err
}

// ---- 一覧 ----

func toRef(id, tenant uuid.UUID, status string, next, trialEnd *sk.Date) c.SubscriptionRef {
	return c.SubscriptionRef{
		SubscriptionID: sk.IDFromUUID[sk.SubscriptionTag](id), TenantID: sk.IDFromUUID[sk.TenantTag](tenant), Status: c.Status(status),
		NextRenewalDate: next, TrialEnd: trialEnd,
	}
}

func (r *Repo) ListDueForRenewal(ctx context.Context, date sk.Date, afterDate *sk.Date, afterID *uuid.UUID, limit int) ([]c.SubscriptionRef, error) {
	rows, err := r.q.ListDueForRenewal(ctx, sqlcgen.ListDueForRenewalParams{Limit: int32(limit), Date: date, AfterDate: afterDate, AfterID: afterID})
	if err != nil {
		return nil, fmt.Errorf("subscription: list due: %w", err)
	}
	out := make([]c.SubscriptionRef, len(rows))
	for i, row := range rows {
		out[i] = toRef(row.ID, row.TenantID, row.Status, row.NextRenewalDate, row.TrialEnd)
	}
	return out, nil
}

func (r *Repo) ListTrialEndingOn(ctx context.Context, date sk.Date, afterID *uuid.UUID, limit int) ([]c.SubscriptionRef, error) {
	rows, err := r.q.ListTrialEndingOn(ctx, sqlcgen.ListTrialEndingOnParams{Limit: int32(limit), Date: date, AfterID: afterID})
	if err != nil {
		return nil, fmt.Errorf("subscription: list trial ending: %w", err)
	}
	out := make([]c.SubscriptionRef, len(rows))
	for i, row := range rows {
		out[i] = toRef(row.ID, row.TenantID, row.Status, row.NextRenewalDate, row.TrialEnd)
	}
	return out, nil
}

func (r *Repo) ListOpenByTenant(ctx context.Context, tenant sk.TenantID, afterID *uuid.UUID, limit int) ([]c.SubscriptionRef, error) {
	rows, err := r.q.ListOpenByTenant(ctx, sqlcgen.ListOpenByTenantParams{Limit: int32(limit), TenantID: tenant.UUID(), AfterID: afterID})
	if err != nil {
		return nil, fmt.Errorf("subscription: list open: %w", err)
	}
	out := make([]c.SubscriptionRef, len(rows))
	for i, row := range rows {
		out[i] = toRef(row.ID, row.TenantID, row.Status, row.NextRenewalDate, row.TrialEnd)
	}
	return out, nil
}

func (r *Repo) HasOpenSubscriptions(ctx context.Context, customer sk.CustomerAccountID) (bool, error) {
	return r.q.HasOpenSubscriptions(ctx, customer.UUID())
}

func (r *Repo) IsPaymentMethodInUse(ctx context.Context, pm sk.PaymentMethodID) (bool, error) {
	return r.q.IsPaymentMethodInUse(ctx, pm.UUID())
}

// Cursor は API の一覧の位置（作成日時の新しい順）。
type Cursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}

// Filter は API の一覧の絞り込み。RLS（tenant_isolation / consumer_self）でさらに絞られる。
type Filter struct {
	Statuses         []string
	PlanID           *uuid.UUID
	TenantCustomerID *string
}

func (r *Repo) List(ctx context.Context, f Filter, after *Cursor, limit int) ([]*domain.Subscription, error) {
	p := sqlcgen.ListSubscriptionsParams{Limit: int32(limit), Statuses: f.Statuses, PlanID: f.PlanID, TenantCustomerID: f.TenantCustomerID}
	if after != nil {
		p.AfterCreatedAt, p.AfterID = &after.CreatedAt, &after.ID
	}
	rows, err := r.q.ListSubscriptions(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("subscription: list: %w", err)
	}
	out := make([]*domain.Subscription, 0, len(rows))
	for _, row := range rows {
		st, err := toState(row)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.Restore(st))
	}
	return out, nil
}

// EntitlementRow は entitlements の材料。
type EntitlementRow struct {
	SubscriptionID sk.SubscriptionID
	ProductID      sk.ProductID
	Status         c.Status
}

// ListByTenantCustomer は当テナントの cus_ の契約（RLS で他テナントは見えない）。
func (r *Repo) ListByTenantCustomer(ctx context.Context, cus string, product *uuid.UUID) ([]EntitlementRow, error) {
	rows, err := r.q.ListByTenantCustomer(ctx, sqlcgen.ListByTenantCustomerParams{TenantCustomerID: cus, ProductID: product})
	if err != nil {
		return nil, fmt.Errorf("subscription: entitlements: %w", err)
	}
	out := make([]EntitlementRow, len(rows))
	for i, row := range rows {
		out[i] = EntitlementRow{SubscriptionID: sk.IDFromUUID[sk.SubscriptionTag](row.ID), ProductID: sk.IDFromUUID[sk.ProductTag](row.ProductID), Status: c.Status(row.Status)}
	}
	return out, nil
}

func (r *Repo) TenantCustomerHasSubscriptions(ctx context.Context, cus string) (bool, error) {
	return r.q.TenantCustomerHasSubscriptions(ctx, cus)
}
