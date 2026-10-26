// Package contracts は catalog モジュールが他のモジュールと cmd に公開する部分（catalog.md §4）。
// 標準ライブラリと sharedkernel だけに依存する。
package contracts

import (
	"context"
	"errors"

	sk "subscription-platform/internal/sharedkernel"
)

// ID の型は sharedkernel のもの（subscription・billing と型を合わせる。P-IMP-14）。
type (
	ProductTag      = sk.ProductTag
	PlanTag         = sk.PlanTag
	PriceVersionTag = sk.PriceVersionTag
	CouponTag       = sk.CouponTag
	RedemptionTag   = sk.RedemptionTag
)

type (
	ProductID      = sk.ProductID
	PlanID         = sk.PlanID
	PriceVersionID = sk.PriceVersionID
	CouponID       = sk.CouponID
	RedemptionID   = sk.RedemptionID
)

// PlanStatus はプランの状態。
type PlanStatus string

const (
	PlanDraft       PlanStatus = "draft"
	PlanPublished   PlanStatus = "published"
	PlanUnpublished PlanStatus = "unpublished"
)

// PlanSnapshot は申込に使うプランと最新の価格版。
type PlanSnapshot struct {
	PlanID         PlanID
	ProductID      ProductID
	PriceVersionID PriceVersionID
	Amount         sk.Money // fixed の表示用。申込の金額は QuotePrice の結果を使う（§4・§10）
	TaxRate        sk.TaxRate
	Interval       sk.BillingInterval
	TrialDays      int
	PricingScheme  string
}

// PlanDetail は状態に関係なく返すプランの情報。
type PlanDetail struct {
	PlanSnapshot
	Status    PlanStatus
	Published bool
}

// PlanDescription は契約者に見せるプランの説明（状態に関係なく返す。consumer-mypage ADR-M01）。
type PlanDescription struct {
	PlanID      PlanID
	PlanName    string
	ProductID   ProductID
	ProductName string
	TenantName  string
	Interval    sk.BillingInterval
	Status      PlanStatus
}

// PurchasablePlan は申込・プラン変更の候補にできる公開中のプラン。
type PurchasablePlan struct {
	PlanSnapshot
	Name string
}

// UsageInput は従量課金などの使用量（PoC では常に nil。将来の項目は追加のみ。§10）。
type UsageInput struct{}

// Quote は価格の算出結果。
type Quote struct {
	Amount  sk.Money
	TaxRate sk.TaxRate
}

// CouponReservation はクーポンの予約の結果。
type CouponReservation struct {
	RedemptionID   RedemptionID
	CouponID       CouponID
	Discount       sk.Discount // 割引額は Discount.Apply で算出する（P-CAT-5）
	DurationCycles int         // 割引を適用する課金の回数（1〜24）
}

// Service は catalog モジュールの公開 API。
type Service interface {
	// GetPurchasablePlan は申込用。published でなければ ErrPlanUnavailable。事業者の状態は見ない（呼び出し元が GetTenant で確認）。
	GetPurchasablePlan(ctx context.Context, tenantID sk.TenantID, planID PlanID) (PlanSnapshot, error)
	// GetPlanSnapshot はプラン変更先の検証・ID の検証用。状態に関係なく返す。
	GetPlanSnapshot(ctx context.Context, tenantID sk.TenantID, planID PlanID) (PlanDetail, error)
	// DescribePlan は契約者に見せるプランの説明（事業者名・商品名・プラン名）。状態に関係なく返す。なければ ErrPlanNotFound。
	DescribePlan(ctx context.Context, tenantID sk.TenantID, planID PlanID) (PlanDescription, error)
	// ListPurchasablePlans は商品の公開中のプラン（作成順）。
	ListPurchasablePlans(ctx context.Context, tenantID sk.TenantID, productID ProductID) ([]PurchasablePlan, error)
	// QuotePrice は価格の算出。PoC の usage は常に nil。
	QuotePrice(ctx context.Context, tenantID sk.TenantID, priceVersionID PriceVersionID, usage *UsageInput) (Quote, error)
	ReserveCoupon(ctx context.Context, tenantID sk.TenantID, code string, planID PlanID, customerAccountID sk.CustomerAccountID, subscriptionID sk.SubscriptionID) (CouponReservation, error)
	ConfirmCouponRedemption(ctx context.Context, tenantID sk.TenantID, redemptionID RedemptionID) error
	ReleaseCouponRedemption(ctx context.Context, tenantID sk.TenantID, redemptionID RedemptionID) error
}

var (
	ErrPlanNotFound               = errors.New("catalog: plan not found")
	ErrPlanUnavailable            = errors.New("catalog: plan unavailable")
	ErrPriceVersionNotFound       = errors.New("catalog: price version not found")
	ErrUsageNotSupported          = errors.New("catalog: usage not supported by pricing scheme")
	ErrCouponNotFound             = errors.New("catalog: coupon not found")
	ErrCouponExpired              = errors.New("catalog: coupon expired")
	ErrCouponNotApplicable        = errors.New("catalog: coupon not applicable")
	ErrCouponAlreadyUsed          = errors.New("catalog: coupon already used")
	ErrCouponExhausted            = errors.New("catalog: coupon exhausted")
	ErrRedemptionNotFound         = errors.New("catalog: redemption not found")
	ErrRedemptionAlreadyConfirmed = errors.New("catalog: redemption already confirmed")
	ErrRedemptionReleased         = errors.New("catalog: redemption released")
)

// イベントの種類（source = sp.catalog）。
const (
	EventPlanPublished            = "catalog.plan_published"
	EventPlanUnpublished          = "catalog.plan_unpublished"
	EventPriceVersionAdded        = "catalog.price_version_added"
	EventCouponRedeemed           = "catalog.coupon_redeemed"
	EventCouponRedemptionReleased = "catalog.coupon_redemption_released"
)

type PlanPublishedPayload struct {
	PlanID         PlanID         `json:"plan_id"`
	ProductID      ProductID      `json:"product_id"`
	PriceVersionID PriceVersionID `json:"price_version_id"`
}

type PlanUnpublishedPayload struct {
	PlanID    PlanID    `json:"plan_id"`
	ProductID ProductID `json:"product_id"`
}

type PriceVersionAddedPayload struct {
	PlanID         PlanID         `json:"plan_id"`
	PriceVersionID PriceVersionID `json:"price_version_id"`
	VersionNo      int            `json:"version_no"`
	Amount         int64          `json:"amount"`
	TaxRate        string         `json:"tax_rate"` // 小数 3 桁（例 "0.100"）
}

type CouponRedeemedPayload struct {
	CouponID          CouponID             `json:"coupon_id"`
	RedemptionID      RedemptionID         `json:"redemption_id"`
	CustomerAccountID sk.CustomerAccountID `json:"customer_account_id"`
	SubscriptionID    sk.SubscriptionID    `json:"subscription_id"`
	PlanID            PlanID               `json:"plan_id"`
}

type CouponRedemptionReleasedPayload struct {
	CouponID          CouponID             `json:"coupon_id"`
	RedemptionID      RedemptionID         `json:"redemption_id"`
	CustomerAccountID sk.CustomerAccountID `json:"customer_account_id"`
	SubscriptionID    sk.SubscriptionID    `json:"subscription_id"`
	Reason            string               `json:"reason"` // caller | expired
}
