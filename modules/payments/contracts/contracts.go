// Package contracts は payments モジュールが他のモジュールと cmd に公開する部分（payments.md §4.1・付録 A）。
// 標準ライブラリと sharedkernel だけに依存する。
package contracts

import (
	"context"
	"errors"
	"fmt"
	"time"

	sk "subscription-platform/internal/sharedkernel"
)

// PayableType は支払い対象の種類。Payments は中身を知らない。
type PayableType string

const PayableInvoice PayableType = "invoice"

// PayableRef は支払い対象への不透明参照。
type PayableRef struct {
	Type PayableType
	ID   string // UUID 文字列
}

// MethodType は決済手段の種類（手段を足すときは定数と CHECK 制約を足す。§10.1）。
type MethodType string

const MethodCard MethodType = "card"

// AttemptStatus は決済の試行の状態。
type AttemptStatus string

const (
	AttemptProcessing   AttemptStatus = "processing"
	AttemptPendingAsync AttemptStatus = "pending_async"
	AttemptSucceeded    AttemptStatus = "succeeded"
	AttemptFailed       AttemptStatus = "failed"
	AttemptUnknown      AttemptStatus = "unknown"
)

// Final は終端か。
func (s AttemptStatus) Final() bool { return s == AttemptSucceeded || s == AttemptFailed }

// Service は payments モジュールの公開 API（呼び出し元は主に Orchestration）。
type Service interface {
	// Collect は 1 回の決済。拒否・結果不明は error ではなく AttemptResult.Status で返す。error は試行を作れなかったときだけ。
	Collect(ctx context.Context, cmd CollectCmd) (AttemptResult, error)
	RegisterPaymentMethod(ctx context.Context, cmd RegisterPaymentMethodCmd) (PaymentMethodView, error)
	GetPaymentMethod(ctx context.Context, customerID sk.CustomerAccountID, id sk.PaymentMethodID) (PaymentMethodView, error)
	ListPaymentMethods(ctx context.Context, customerID sk.CustomerAccountID, cursor string, limit int) (Page[PaymentMethodView], error)
	// DeletePaymentMethod は削除する（使用中かの判定は Orchestration。C-04）。削除済みは nil。
	DeletePaymentMethod(ctx context.Context, customerID sk.CustomerAccountID, id sk.PaymentMethodID) error
	EnsureProviderRef(ctx context.Context, cmd EnsureProviderRefCmd) error
	ResolveUnknownAttempts(ctx context.Context, olderThan time.Duration) (ResolveSummary, error)
}

// CollectCmd は決済の依頼。
type CollectCmd struct {
	TenantID          sk.TenantID
	TenantShopID      *string // Orchestration が tenant の contracts で得て渡す（P-PAY-1）
	CustomerAccountID sk.CustomerAccountID
	Payable           PayableRef
	Amount            sk.Money
	PaymentMethodID   sk.PaymentMethodID
	IdempotencyKey    string // invoice:{invoice_id}:{collection_seq}（§3.3、P-X-1）
}

// IdempotencyKeyFor は回収要求の業務の冪等キー（§3.3）。
func IdempotencyKeyFor(invoiceID sk.InvoiceID, collectionSeq int) string {
	return fmt.Sprintf("invoice:%s:%d", invoiceID, collectionSeq)
}

// AttemptResult は Collect の結果。
type AttemptResult struct {
	AttemptID        sk.PaymentAttemptID
	Status           AttemptStatus
	FailureCode      *string
	Retryable        *bool
	ConsumerCategory *string // card_expired | insufficient_funds | generic（failed のとき）
	Replayed         bool    // 既存の試行を返した（冪等の再実行）
}

// RegisterPaymentMethodCmd は支払方法の登録。
type RegisterPaymentMethodCmd struct {
	CustomerAccountID sk.CustomerAccountID
	MethodType        MethodType
	Provider          string // "stub" | "fincode"
	Token             string
	IdempotencyKey    string // HTTP の Idempotency-Key。同じキーの再送は同じ支払方法を返す
}

// PaymentMethodView は支払方法の表示用の形。
type PaymentMethodView struct {
	ID         sk.PaymentMethodID
	MethodType MethodType
	Status     string // active | deleted
	Display    map[string]any
	Expired    bool // 業務時刻を基準に算出
	CreatedAt  time.Time
}

// Page はキーセットの一覧。NextCursor が空なら終わり。
type Page[T any] struct {
	Items      []T
	NextCursor string
}

// EnsureProviderRefCmd は事業者ショップ向けの代行側の参照を用意する（共有できない代行のとき）。
type EnsureProviderRefCmd struct {
	PaymentMethodID sk.PaymentMethodID
	TenantID        sk.TenantID
	TenantShopID    *string
}

// ResolveSummary は結果不明の照合の集計。
type ResolveSummary struct{ Checked, Succeeded, Failed, StillUnknown, Pending int }

var (
	ErrPaymentMethodNotFound      = errors.New("payments: payment method not found")
	ErrPaymentMethodLimitExceeded = errors.New("payments: payment method limit exceeded")
	ErrInvalidToken               = errors.New("payments: invalid payment token")
	ErrUnsupportedMethod          = errors.New("payments: unsupported payment method")
	ErrIdempotencyKeyReused       = errors.New("payments: idempotency key reused with different request")
	ErrInvalidIdempotencyKey      = errors.New("payments: invalid idempotency key")
	ErrInvalidAmount              = errors.New("payments: invalid amount")
	ErrProviderUnavailable        = errors.New("payments: provider unavailable")
	ErrAttemptFinalized           = errors.New("payments: attempt already finalized")
	ErrAttemptNotResolvable       = errors.New("payments: attempt not resolvable")
	ErrAttemptNotFound            = errors.New("payments: attempt not found")
	ErrOutcomeMismatch            = errors.New("payments: outcome mismatch with provider")
	// ErrAttemptInProgressKind・ErrPayableAlreadyPaidKind は errors.Is での判定用。
	ErrAttemptInProgressKind  = errors.New("payments: attempt in progress")
	ErrPayableAlreadyPaidKind = errors.New("payments: payable already paid")
)

// AttemptInProgressError は別キーの進行中の試行がある（E-03・E-05）。
type AttemptInProgressError struct{ AttemptID sk.PaymentAttemptID }

func (e *AttemptInProgressError) Error() string {
	return "payments: attempt in progress: " + e.AttemptID.String()
}
func (e *AttemptInProgressError) Is(t error) bool { return t == ErrAttemptInProgressKind }

// PayableAlreadyPaidError は成功済みの試行がある（E-04）。
type PayableAlreadyPaidError struct{ AttemptID sk.PaymentAttemptID }

func (e *PayableAlreadyPaidError) Error() string {
	return "payments: payable already paid: " + e.AttemptID.String()
}
func (e *PayableAlreadyPaidError) Is(t error) bool { return t == ErrPayableAlreadyPaidKind }

// イベントの種類（source = sp.payments、aggregate_type = payment_attempt）。
const (
	EventPaymentSucceeded      = "payments.payment_succeeded"
	EventPaymentFailed         = "payments.payment_failed"
	EventPaymentPending        = "payments.payment_pending"
	EventPaymentOutcomeUnknown = "payments.payment_outcome_unknown"
)

// PaymentEventPayload は 4 種類のイベントの payload（項目は種類ごとに §5.3 のとおり）。
type PaymentEventPayload struct {
	AttemptID         sk.PaymentAttemptID   `json:"attempt_id"`
	Payable           PayableJSON           `json:"payable"`
	Amount            int64                 `json:"amount"`
	PaymentMethodID   sk.PaymentMethodID    `json:"payment_method_id"`
	CustomerAccountID *sk.CustomerAccountID `json:"customer_account_id,omitempty"`
	Resolution        *string               `json:"resolution,omitempty"`
	ResolvedBy        *string               `json:"resolved_by,omitempty"`
	FailureCode       *string               `json:"failure_code,omitempty"`
	Retryable         *bool                 `json:"retryable,omitempty"`
	NextAction        any                   `json:"next_action,omitempty"`
}

// PayableJSON はイベントの payable。
type PayableJSON struct {
	Type PayableType `json:"type"`
	ID   string      `json:"id"`
}
