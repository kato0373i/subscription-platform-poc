// Package provider は決済代行のポート（PaymentProvider）とレジストリ（payments.md §4.3・§4.4・付録 B）。
// 必須の能力と任意の能力を分け、任意の能力は型アサーションで検出する。アプリ層はこのパッケージだけを見て、
// 具体的なアダプター（stub・fincode）を import しない（AC-PAY-031）。
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	sk "subscription-platform/internal/sharedkernel"
	c "subscription-platform/modules/payments/contracts"
	"subscription-platform/modules/payments/internal/domain"
)

// ProviderID は決済代行の識別子（stub / fincode）。
type ProviderID string

// Key はレジストリのキー（MethodType × Provider）。
type Key struct {
	Method   c.MethodType
	Provider ProviderID
}

func (k Key) String() string { return string(k.Method) + ":" + string(k.Provider) }

// RefSharing は代行側の参照を事業者ショップ間で共有できるか。
type RefSharing string

const (
	RefShared    RefSharing = "shared"
	RefPerTenant RefSharing = "per_tenant"
)

// Capabilities は代行の性質。
type Capabilities struct {
	AsyncSettlement      bool // pending_async を返しうる
	RefSharing           RefSharing
	RequiresStoredMethod bool
}

// OutcomeStatus はアダプターが返す結果。
type OutcomeStatus string

const (
	OutcomeSucceeded    OutcomeStatus = "succeeded"
	OutcomeFailed       OutcomeStatus = "failed"
	OutcomePendingAsync OutcomeStatus = "pending_async"
	OutcomeUnknown      OutcomeStatus = "unknown"
	OutcomeNotFound     OutcomeStatus = "not_found" // GetStatus だけ
)

// Outcome は決済の結果。
type Outcome struct {
	Status      OutcomeStatus
	FailureCode domain.FailureCode
	Retryable   bool
	RawCode     string
}

// CollectInput は決済の依頼。IdempotencyKey は attempt_id（代行への冪等キー）。
type CollectInput struct {
	AttemptID        uuid.UUID
	ProviderOrderRef string
	IdempotencyKey   string
	Amount           sk.Money
	Ref              domain.ProviderRef
	TenantShopID     *string
}

// StatusInput は結果の照会。
type StatusInput struct {
	ProviderOrderRef string
	TenantShopID     *string
}

var (
	// ErrNotSent は送信前に失敗した（試行は failed(processing_error, retryable)）。
	ErrNotSent = errors.New("provider: not sent")
	// ErrUnavailable は代行が使えない（試行を作らない。ErrProviderUnavailable）。
	ErrUnavailable = errors.New("provider: unavailable")
	// ErrNotImplemented は未実装（fincode の骨格）。
	ErrNotImplemented = errors.New("provider: not implemented")
	// ErrInvalidSignature は Webhook の署名が正しくない。
	ErrInvalidSignature = errors.New("provider: invalid webhook signature")
)

// PaymentProvider は必須の能力。
type PaymentProvider interface {
	Key() Key
	Capabilities() Capabilities
	OrderRefFor(attemptID uuid.UUID) string
	Collect(ctx context.Context, in CollectInput) (Outcome, error)
	GetStatus(ctx context.Context, in StatusInput) (Outcome, error)
}

// RegisterInput は支払方法の登録。
type RegisterInput struct {
	CustomerAccountID sk.CustomerAccountID
	Token             string
	IdempotencyKey    string
}

// RegisteredMethod は登録の結果。
type RegisteredMethod struct {
	Ref     domain.ProviderRef
	Display domain.CardDisplay
}

// MethodRegistrar はトークンから支払方法を登録する（任意）。
type MethodRegistrar interface {
	RegisterMethod(ctx context.Context, in RegisterInput) (RegisteredMethod, error)
}

// EnsureTenantRefInput は事業者ショップ向けの参照の作成。
type EnsureTenantRefInput struct {
	Shared       domain.ProviderRef
	TenantShopID string
}

// TenantRefProvisioner は共有できない代行で、事業者ショップ向けの参照を作る（任意）。
type TenantRefProvisioner interface {
	EnsureTenantRef(ctx context.Context, in EnsureTenantRefInput) (domain.ProviderRef, error)
}

// ProviderEvent は代行からの通知（代行に依存しない形に正規化したもの）。
type ProviderEvent struct {
	ProviderEventID  string
	ProviderOrderRef string
	Outcome          Outcome
	OccurredAt       time.Time
	Raw              json.RawMessage
}

// WebhookParser は署名を検証して通知を読む（任意）。
type WebhookParser interface {
	ParseWebhook(ctx context.Context, h http.Header, body []byte) ([]ProviderEvent, error)
}

// WebhookAcker は受信成功のときに返す本文を決める（任意。fincode は {"receive":"0"} を期待し、それ以外は再送する）。
type WebhookAcker interface {
	WebhookAck() []byte
}

// Settlement は代行側の 1 件の決済（日次照合）。
type Settlement struct {
	ProviderOrderRef string
	Amount           sk.Money
	Status           OutcomeStatus
}

// SettlementLister は期間の決済一覧を返す（任意。日次照合）。cursor が空なら先頭から、返りの cursor が空なら終わり。
type SettlementLister interface {
	ListSettlements(ctx context.Context, from, to time.Time, cursor string, pageSize int) ([]Settlement, string, error)
}

// MethodDisabler は代行側の支払方法を無効にする（任意。退会時にベストエフォート）。
type MethodDisabler interface {
	DisableMethod(ctx context.Context, ref domain.ProviderRef) error
}
