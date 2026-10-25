// Package contracts は billing モジュールが他のモジュールと cmd に公開する部分（billing.md §4）。
// 標準ライブラリと sharedkernel だけに依存する。決済手段は知らない（ADR-003）。
package contracts

import (
	"context"
	"errors"
	"time"

	sk "subscription-platform/internal/sharedkernel"
)

// InvoiceKind は請求の種類。
type InvoiceKind string

const (
	KindInitial InvoiceKind = "initial"
	KindRenewal InvoiceKind = "renewal"
)

// CollectionMode は回収の仕方。caller_managed は申込の同期処理（呼び出し元が直接決済し、Billing はダニングしない）。
type CollectionMode string

const (
	ModeScheduled     CollectionMode = "scheduled"
	ModeCallerManaged CollectionMode = "caller_managed"
)

// InvoiceStatus は請求の状態。
type InvoiceStatus string

const (
	StatusOpen           InvoiceStatus = "open"
	StatusProcessing     InvoiceStatus = "processing"
	StatusPaymentUnknown InvoiceStatus = "payment_unknown"
	StatusPaid           InvoiceStatus = "paid"
	StatusUncollectible  InvoiceStatus = "uncollectible"
	StatusVoid           InvoiceStatus = "void"
)

// Terminal は終端か（paid / uncollectible / void）。
func (s InvoiceStatus) Terminal() bool {
	return s == StatusPaid || s == StatusUncollectible || s == StatusVoid
}

// PlanLine はプランの明細の元（金額は catalog の QuotePrice の結果）。
type PlanLine struct {
	PriceVersionID sk.PriceVersionID
	Amount         sk.Money
	TaxRate        sk.TaxRate
	Description    string
}

// AppliedDiscount はこの請求に適用する割引。
type AppliedDiscount struct {
	Type               sk.DiscountType
	Value              int64
	CouponRedemptionID *sk.RedemptionID
}

// IssueInvoiceCmd は請求の作成。
type IssueInvoiceCmd struct {
	TenantID          sk.TenantID
	SubscriptionID    sk.SubscriptionID
	CustomerAccountID sk.CustomerAccountID
	Period            sk.BillingPeriod
	PlanLine          PlanLine
	Discount          *AppliedDiscount
	Kind              InvoiceKind
	CollectionMode    CollectionMode
}

// OutcomeStatus は決済の結果。
type OutcomeStatus string

const (
	OutcomeSucceeded OutcomeStatus = "succeeded"
	OutcomeFailed    OutcomeStatus = "failed"
	OutcomePending   OutcomeStatus = "pending"
	OutcomeUnknown   OutcomeStatus = "unknown"
)

// OutcomeSource は結果がどこから来たか。
type OutcomeSource string

const (
	SourceSync  OutcomeSource = "sync"
	SourceEvent OutcomeSource = "event"
)

// PaymentOutcome は決済の結果。
type PaymentOutcome struct {
	Status      OutcomeStatus
	FailureCode string
	Retryable   *bool // failed のとき必須
	Amount      sk.Money
	Source      OutcomeSource
}

// CollectionReason は回収要求の理由。
type CollectionReason string

const (
	ReasonInitial   CollectionReason = "initial"
	ReasonRetry     CollectionReason = "retry"
	ReasonImmediate CollectionReason = "immediate"
	ReasonOps       CollectionReason = "ops"
)

// Scheduled は予定内の試行か（失敗するとダニングの段数を進める）。
func (r CollectionReason) Scheduled() bool { return r == ReasonInitial || r == ReasonRetry }

// CollectionRequest は予定外の回収要求（即時の再決済・ops）。
type CollectionRequest struct {
	Reason      CollectionReason // immediate | ops
	RequestedBy string           // "system" または operator の subject
}

// VoidReason は無効化の理由。
type VoidReason string

const (
	VoidSignupFailed         VoidReason = "signup_failed"
	VoidSubscriptionCanceled VoidReason = "subscription_canceled"
	VoidTenantSuspended      VoidReason = "tenant_suspended"
)

// TaxLine は税率ごとの内訳。
type TaxLine struct {
	Rate          sk.TaxRate
	TaxableAmount sk.Money
	TaxAmount     sk.Money
}

// LineView は明細。
type LineView struct {
	LineNo      int
	Kind        string // plan | discount
	Description string
	Amount      sk.Money
}

// InvoiceView は請求の現在の姿。
type InvoiceView struct {
	ID                sk.InvoiceID
	TenantID          sk.TenantID
	SubscriptionID    sk.SubscriptionID
	CustomerAccountID sk.CustomerAccountID
	Kind              InvoiceKind
	CollectionMode    CollectionMode
	Status            InvoiceStatus
	Period            sk.BillingPeriod
	Subtotal          sk.Money
	DiscountTotal     sk.Money
	Total             sk.Money
	Tax               []TaxLine
	Lines             []LineView
	AttemptCount      int
	DunningStage      int
	NextCollectionAt  *time.Time
	// CollectionSeq は最後に払い出した回収要求の番号。呼び出し元は invoice:{ID}:{CollectionSeq} を決済の冪等キーにする（P-X-3）
	CollectionSeq int
	IssuedAt      time.Time
	PaidAt        *time.Time
	Version       int
}

// Service は billing モジュールの公開 API（呼び出し元は主に Orchestration）。
type Service interface {
	IssueInvoice(ctx context.Context, cmd IssueInvoiceCmd) (InvoiceView, error)
	RecordPaymentOutcome(ctx context.Context, invoiceID sk.InvoiceID, attemptID sk.PaymentAttemptID, outcome PaymentOutcome) (InvoiceView, error)
	RequestImmediateCollection(ctx context.Context, invoiceID sk.InvoiceID, req CollectionRequest) (InvoiceView, error)
	VoidOpenInvoices(ctx context.Context, subscriptionID sk.SubscriptionID, reason VoidReason) ([]InvoiceView, error)
	// GetOpenInvoice は非終端の請求。なければ nil, nil。
	GetOpenInvoice(ctx context.Context, subscriptionID sk.SubscriptionID) (*InvoiceView, error)
	// GetInvoice は ID で読む（状態に関係なく）。なければ ErrInvoiceNotFound。申込の再実行の応答と sweeper が使う。
	GetInvoice(ctx context.Context, invoiceID sk.InvoiceID) (InvoiceView, error)
}

var (
	ErrValidation             = errors.New("billing: invalid command")
	ErrIdempotencyConflict    = errors.New("billing: same invoice key with different content")
	ErrUnsettledInvoiceExists = errors.New("billing: unsettled invoice exists for subscription")
	ErrMixedTaxRates          = errors.New("billing: mixed tax rates")
	ErrInvoiceNotFound        = errors.New("billing: invoice not found")
	ErrCollectionInProgress   = errors.New("billing: collection in progress")
	ErrInvoiceNotCollectible  = errors.New("billing: invoice not collectible")
)

// InvalidDunningPolicyError は DP-1〜DP-3 の違反（Code は too_many / out_of_range / not_increasing）。
type InvalidDunningPolicyError struct{ Code string }

func (e *InvalidDunningPolicyError) Error() string {
	return "billing: invalid dunning policy: " + e.Code
}

// イベントの種類（source = sp.billing、aggregate_type = invoice）。
const (
	EventInvoiceIssued              = "billing.invoice_issued"
	EventInvoiceCollectionDue       = "billing.invoice_collection_due"
	EventInvoicePaid                = "billing.invoice_paid"
	EventInvoicePaymentFailed       = "billing.invoice_payment_failed"
	EventInvoiceMarkedUncollectible = "billing.invoice_marked_uncollectible"
	EventInvoiceVoided              = "billing.invoice_voided"
)
