// Package contracts は subscription モジュールが他のモジュールと cmd に公開する部分（subscription.md §4）。
// 標準ライブラリと sharedkernel だけに依存する。
package contracts

import (
	"context"
	"errors"
	"fmt"
	"time"

	sk "subscription-platform/internal/sharedkernel"
)

// Status は契約の状態。
type Status string

const (
	StatusPendingInitial Status = "pending_initial"
	StatusTrialing       Status = "trialing"
	StatusActive         Status = "active"
	StatusPastDue        Status = "past_due"
	StatusCanceled       Status = "canceled"
	StatusSignupFailed   Status = "signup_failed"
)

// Open は有効な契約か（退会の判定・事業者停止の対象・支払方法の使用中の判定。P-SUB-3）。
func (s Status) Open() bool {
	switch s {
	case StatusPendingInitial, StatusTrialing, StatusActive, StatusPastDue:
		return true
	case StatusCanceled, StatusSignupFailed:
	}
	return false
}

// Entitled は利用権があるか（I-8。日付では判定しない）。
func (s Status) Entitled() bool {
	return s == StatusTrialing || s == StatusActive || s == StatusPastDue
}

// Terminal は終端か。
func (s Status) Terminal() bool { return s == StatusCanceled || s == StatusSignupFailed }

// CancelReason は解約の理由。
type CancelReason string

const (
	CancelByCustomer        CancelReason = "customer"
	CancelByDunning         CancelReason = "dunning"
	CancelByTenantSuspended CancelReason = "tenant_suspended"
)

// CancellationSource は解約予約の出所（system は契約者が取り消せない。D-01）。
type CancellationSource string

const (
	SourceCustomer CancellationSource = "customer"
	SourceSystem   CancellationSource = "system"
)

// AppliedDiscount は契約に適用中の割引。
type AppliedDiscount struct {
	CouponID        sk.CouponID     `json:"coupon_id"`
	RedemptionID    sk.RedemptionID `json:"redemption_id"`
	Type            sk.DiscountType `json:"type"`
	Value           int64           `json:"value"`
	DurationCycles  int             `json:"duration_cycles"`
	RemainingCycles int             `json:"remaining_cycles"`
}

// Discount は割引額の計算用（sharedkernel.Discount.Apply）。
func (d AppliedDiscount) Discount() sk.Discount { return sk.Discount{Type: d.Type, Value: d.Value} }

// DiscountInput は申込で予約したクーポン（catalog の CouponReservation から作る）。
type DiscountInput struct {
	CouponID       sk.CouponID
	RedemptionID   sk.RedemptionID
	Type           sk.DiscountType
	Value          int64
	DurationCycles int
}

// PlanInput は申込のプラン（catalog の PlanSnapshot から作る）。
type PlanInput struct {
	PlanID         sk.PlanID
	ProductID      sk.ProductID
	PriceVersionID sk.PriceVersionID
	Interval       sk.BillingInterval
	TrialDays      int
}

// CreateSignupCmd は申込で契約を作るコマンド。
type CreateSignupCmd struct {
	SubscriptionID     sk.SubscriptionID // Orchestration がサガの手順 0 で採番する（クーポンの予約に先に使うため）。空なら採番する
	TenantID           sk.TenantID
	CustomerAccountID  sk.CustomerAccountID
	TenantCustomerID   string // customer の EnsureTenantCustomerID の値（cus_…。P-SUB-1）
	SignupRequestID    string
	Plan               PlanInput
	PaymentMethodID    sk.PaymentMethodID
	Discount           *DiscountInput
	SignupDate         sk.Date
	AllowTrialFallback bool
}

// Terms は 1 回の請求の条件。金額は持たず、price_version_id から catalog の QuotePrice で算出する（§10）。
type Terms struct {
	Period          sk.BillingPeriod
	PlanID          sk.PlanID
	PriceVersionID  sk.PriceVersionID
	Interval        sk.BillingInterval
	Quantity        int
	Discount        *AppliedDiscount // この請求に適用する割引。適用しないなら nil
	PaymentMethodID sk.PaymentMethodID
	FirstCharge     bool // トライアル後の初回課金
	PlanChanged     bool
}

// Cancellation は解約の予約。
type Cancellation struct {
	Source        CancellationSource
	Reason        CancelReason
	EffectiveDate sk.Date
}

// PendingPlanChange はプラン変更の予約（予約時点の価格版を固定する）。
type PendingPlanChange struct {
	PlanID         sk.PlanID
	PriceVersionID sk.PriceVersionID
	Interval       sk.BillingInterval
	EffectiveDate  sk.Date
}

// SubscriptionView は契約の現在の姿。
type SubscriptionView struct {
	ID                sk.SubscriptionID
	TenantID          sk.TenantID
	CustomerAccountID sk.CustomerAccountID
	TenantCustomerID  string
	ProductID         sk.ProductID
	PlanID            sk.PlanID
	PriceVersionID    sk.PriceVersionID
	Interval          sk.BillingInterval
	Quantity          int
	Status            Status
	SignupDate        sk.Date
	CurrentPeriod     sk.BillingPeriod
	NextRenewalDate   *sk.Date
	TrialStart        *sk.Date
	TrialEnd          *sk.Date
	Cancellation      *Cancellation
	PendingPlanChange *PendingPlanChange
	RenewalInProgress bool
	Entitled          bool
	AppliedDiscount   *AppliedDiscount
	PaymentMethodID   sk.PaymentMethodID
	CancelReason      *CancelReason
	CanceledAt        *time.Time
	InitialTerms      *Terms // pending_initial のときだけ
	Version           int
	CreatedAt         time.Time
}

// RenewalKind は BeginRenewal の結果の種類。
type RenewalKind string

const (
	RenewalEnded RenewalKind = "ended"
	RenewalTerms RenewalKind = "terms"
)

// RenewalOutcome は BeginRenewal の結果。
type RenewalOutcome struct {
	Kind         RenewalKind
	Terms        *Terms
	Subscription SubscriptionView
}

// SystemCancellationKind は ScheduleSystemCancellation の結果の種類。
type SystemCancellationKind string

const (
	SystemCancellationScheduled SystemCancellationKind = "scheduled"
	SystemCanceledImmediately   SystemCancellationKind = "canceled_immediately"
	SystemCancellationNoop      SystemCancellationKind = "noop"
)

// SystemCancellationResult は事業者停止に伴う解約の結果。
type SystemCancellationResult struct {
	Kind          SystemCancellationKind
	EffectiveDate *sk.Date
	Subscription  SubscriptionView
}

// SubscriptionRef は一覧の 1 件（ジョブが使う）。
type SubscriptionRef struct {
	SubscriptionID  sk.SubscriptionID
	TenantID        sk.TenantID
	Status          Status
	NextRenewalDate *sk.Date
	TrialEnd        *sk.Date
}

// Page はキーセットの一覧。NextCursor が空なら終わり。
type Page[T any] struct {
	Items      []T
	NextCursor string
}

// Service は subscription モジュールの公開 API（呼び出し元は主に Orchestration）。
type Service interface {
	CreateForSignup(ctx context.Context, cmd CreateSignupCmd) (SubscriptionView, error)
	Activate(ctx context.Context, subID sk.SubscriptionID, invoiceID sk.InvoiceID) (SubscriptionView, error)
	MarkSignupFailed(ctx context.Context, subID sk.SubscriptionID, reason string) error
	BeginRenewal(ctx context.Context, subID sk.SubscriptionID, date sk.Date) (RenewalOutcome, error)
	ConfirmRenewal(ctx context.Context, subID sk.SubscriptionID, invoiceID sk.InvoiceID) (SubscriptionView, error)
	MarkPastDue(ctx context.Context, subID sk.SubscriptionID, invoiceID sk.InvoiceID) (SubscriptionView, error)
	Recover(ctx context.Context, subID sk.SubscriptionID, invoiceID sk.InvoiceID) (SubscriptionView, error)
	Cancel(ctx context.Context, subID sk.SubscriptionID, reason CancelReason) (SubscriptionView, error)
	ScheduleSystemCancellation(ctx context.Context, subID sk.SubscriptionID, reason CancelReason) (SystemCancellationResult, error)
	ChangePaymentMethod(ctx context.Context, subID sk.SubscriptionID, customerAccountID sk.CustomerAccountID, pmID sk.PaymentMethodID) (SubscriptionView, error)
	ListDueForRenewal(ctx context.Context, date sk.Date, cursor string, limit int) (Page[SubscriptionRef], error)
	ListTrialEndingOn(ctx context.Context, date sk.Date, cursor string, limit int) (Page[SubscriptionRef], error)
	ListOpenByTenant(ctx context.Context, tenantID sk.TenantID, cursor string, limit int) (Page[SubscriptionRef], error)
	HasOpenSubscriptions(ctx context.Context, customerAccountID sk.CustomerAccountID) (bool, error)
	IsPaymentMethodInUse(ctx context.Context, pmID sk.PaymentMethodID) (bool, error)
	Get(ctx context.Context, subID sk.SubscriptionID) (SubscriptionView, error)
}

var (
	ErrNotFound                 = errors.New("subscription: not found")
	ErrTerminal                 = errors.New("subscription: terminal state")
	ErrStateConflict            = errors.New("subscription: state conflict")
	ErrNotDue                   = errors.New("subscription: renewal not due")
	ErrNotRenewable             = errors.New("subscription: not renewable")
	ErrInvoiceMismatch          = errors.New("subscription: invoice mismatch")
	ErrCancellationScheduled    = errors.New("subscription: cancellation scheduled")
	ErrCancellationNotRevocable = errors.New("subscription: cancellation not revocable")
	ErrTrialNotEligible         = errors.New("subscription: trial not eligible")
	ErrVersionConflict          = errors.New("subscription: version conflict")
	// ErrInvalidTransitionKind は errors.Is で InvalidTransitionError を判定するための値。
	ErrInvalidTransitionKind = errors.New("subscription: invalid transition")
	// ErrPlanChangeInvalidKind は errors.Is で PlanChangeInvalidError を判定するための値。
	ErrPlanChangeInvalidKind = errors.New("subscription: plan change not allowed")
)

// InvalidTransitionError は Orchestration からの不正な遷移（バグまたは順序の乱れ）。
type InvalidTransitionError struct {
	From    Status
	Command string
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("subscription: invalid transition: %s on %s", e.Command, e.From)
}
func (e *InvalidTransitionError) Is(target error) bool { return target == ErrInvalidTransitionKind }

// PlanChangeInvalidError はプラン変更の予約ができない理由（different_product / plan_unavailable / same_plan）。
type PlanChangeInvalidError struct{ Reason string }

func (e *PlanChangeInvalidError) Error() string {
	return "subscription: plan change not allowed: " + e.Reason
}
func (e *PlanChangeInvalidError) Is(target error) bool { return target == ErrPlanChangeInvalidKind }

// イベントの種類（source = sp.subscription）。
const (
	EventStarted               = "subscription.started"
	EventSignupFailed          = "subscription.signup_failed"
	EventRenewed               = "subscription.renewed"
	EventRecovered             = "subscription.recovered"
	EventPlanChanged           = "subscription.plan_changed"
	EventPastDue               = "subscription.past_due"
	EventCanceled              = "subscription.canceled"
	EventCancellationScheduled = "subscription.cancellation_scheduled"
	EventCancellationRevoked   = "subscription.cancellation_revoked"
	EventPlanChangeScheduled   = "subscription.plan_change_scheduled"
	EventPlanChangeRevoked     = "subscription.plan_change_revoked"
	EventPaymentMethodChanged  = "subscription.payment_method_changed"
	EventTrialWillEnd          = "subscription.trial_will_end"
)
