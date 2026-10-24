// Package domain は catalog モジュールの集約（Product・Plan・Coupon・CouponRedemption。catalog.md §3）。
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/catalog/contracts"
	"subscription-platform/modules/catalog/internal/domain/pricing"
)

var (
	ErrInvalidPlanTransition = errors.New("catalog: invalid plan transition")
	ErrTenantNotActive       = errors.New("catalog: tenant not active")
	ErrPriceUnchanged        = errors.New("catalog: price unchanged")
)

// ValidationError は入力の誤り（HTTP では validation_failed）。
type ValidationError struct{ Field, Code, Detail string }

func (e *ValidationError) Error() string {
	return fmt.Sprintf("catalog: invalid %s (%s)", e.Field, e.Code)
}

func invalid(field, code string) error { return &ValidationError{Field: field, Code: code} }

func text(field, s string, min, max int) (string, error) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	switch {
	case n < min && n == 0:
		return "", invalid(field, "required")
	case n < min:
		return "", invalid(field, "too_short")
	case n > max:
		return "", invalid(field, "too_long")
	}
	return s, nil
}

// DefaultTaxRate は PoC の税率（D-09。API からは指定させない）。
var DefaultTaxRate, _ = sk.NewTaxRate(10, 100)

// ---- Product ----

// ProductState は Product の状態。
type ProductState struct {
	ID          contracts.ProductID
	TenantID    sk.TenantID
	Name        string
	Description string
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Product は商品（集約ルート）。
type Product struct{ s ProductState }

func RestoreProduct(s ProductState) *Product { return &Product{s: s} }

func NewProduct(id contracts.ProductID, tenantID sk.TenantID, name, desc string, now time.Time) (*Product, error) {
	n, err := text("name", name, 1, 100)
	if err != nil {
		return nil, err
	}
	d, err := text("description", desc, 0, 1000)
	if err != nil {
		return nil, err
	}
	return &Product{s: ProductState{ID: id, TenantID: tenantID, Name: n, Description: d, Version: 1, CreatedAt: now, UpdatedAt: now}}, nil
}

func (p *Product) State() ProductState { return p.s }

// Rename は指定された項目だけを変える（nil は変えない）。変わったら true。
func (p *Product) Rename(name, desc *string) (bool, error) {
	n, d := p.s.Name, p.s.Description
	var err error
	if name != nil {
		if n, err = text("name", *name, 1, 100); err != nil {
			return false, err
		}
	}
	if desc != nil {
		if d, err = text("description", *desc, 0, 1000); err != nil {
			return false, err
		}
	}
	changed := n != p.s.Name || d != p.s.Description
	p.s.Name, p.s.Description = n, d
	return changed, nil
}

// ---- Plan・PriceVersion ----

// PriceVersion はプランの価格の 1 版（不変）。
type PriceVersion struct {
	ID            contracts.PriceVersionID
	VersionNo     int
	Scheme        pricing.Kind
	Amount        *sk.Money
	Params        pricing.Params
	TaxRate       sk.TaxRate
	EffectiveFrom time.Time
}

// Price は算出に使う部分。
func (v PriceVersion) Price() pricing.Price {
	return pricing.Price{Kind: v.Scheme, Amount: v.Amount, Params: v.Params}
}

// AmountYen は fixed の金額（ない場合は 0）。
func (v PriceVersion) AmountYen() int64 {
	if v.Amount == nil {
		return 0
	}
	return v.Amount.Int64()
}

// PlanState は Plan の状態。
type PlanState struct {
	ID           contracts.PlanID
	TenantID     sk.TenantID
	ProductID    contracts.ProductID
	Name         string
	Description  string
	Interval     sk.BillingInterval
	TrialDays    int
	Scheme       pricing.Kind
	Status       contracts.PlanStatus
	CurrentPrice PriceVersion
	PublishedAt  *time.Time
	Version      int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Plan はプラン（集約ルート。PriceVersion を含む）。
type Plan struct{ s PlanState }

func RestorePlan(s PlanState) *Plan { return &Plan{s: s} }

// PlanInput はプランの作成の入力。
type PlanInput struct {
	ID          contracts.PlanID
	TenantID    sk.TenantID
	ProductID   contracts.ProductID
	Name        string
	Description string
	Interval    sk.BillingInterval
	TrialDays   int
	Scheme      pricing.Kind
	Amount      int64
	PriceID     contracts.PriceVersionID
}

func trialDays(n int) error {
	if n < 0 || n > 365 {
		return invalid("trial_days", "out_of_range")
	}
	return nil
}

// amountError は金額の誤り（許容範囲を detail に示す。§7.3）。
func amountError() error {
	return &ValidationError{Field: "amount", Code: "out_of_range", Detail: fmt.Sprintf("amount must be between %d and %d", pricing.MinAmount, pricing.MaxAmount)}
}

func newPrice(reg pricing.Registry, kind pricing.Kind, id contracts.PriceVersionID, no int, amount int64, now time.Time) (PriceVersion, error) {
	scheme, err := reg.Get(kind)
	if err != nil {
		return PriceVersion{}, invalid("pricing_scheme", "unsupported")
	}
	m, err := sk.NewMoney(amount)
	if err != nil {
		return PriceVersion{}, amountError()
	}
	if err := scheme.ValidateParams(&m, pricing.Params{}); err != nil {
		if errors.Is(err, pricing.ErrAmountOutOfRange) {
			return PriceVersion{}, amountError()
		}
		return PriceVersion{}, invalid("scheme_params", "invalid")
	}
	return PriceVersion{ID: id, VersionNo: no, Scheme: kind, Amount: &m, Params: pricing.Params{}, TaxRate: DefaultTaxRate, EffectiveFrom: now}, nil
}

// NewPlan は draft のプランと v1 の価格版を作る（`catalog.price_version_added` を出す）。
func NewPlan(reg pricing.Registry, in PlanInput, now time.Time) (*Plan, error) {
	n, err := text("name", in.Name, 1, 100)
	if err != nil {
		return nil, err
	}
	d, err := text("description", in.Description, 0, 1000)
	if err != nil {
		return nil, err
	}
	if !in.Interval.Valid() {
		return nil, invalid("billing_interval", "invalid")
	}
	if err := trialDays(in.TrialDays); err != nil {
		return nil, err
	}
	if in.Scheme == "" {
		in.Scheme = pricing.KindFixed
	}
	pv, err := newPrice(reg, in.Scheme, in.PriceID, 1, in.Amount, now)
	if err != nil {
		return nil, err
	}
	return &Plan{s: PlanState{
		ID: in.ID, TenantID: in.TenantID, ProductID: in.ProductID, Name: n, Description: d, Interval: in.Interval,
		TrialDays: in.TrialDays, Scheme: in.Scheme, Status: contracts.PlanDraft, CurrentPrice: pv, Version: 1, CreatedAt: now, UpdatedAt: now,
	}}, nil
}

func (p *Plan) State() PlanState { return p.s }

// Purchasable は published か（事業者の状態は呼び出し元が見る）。
func (p *Plan) Purchasable() bool { return p.s.Status == contracts.PlanPublished }

// Publish は draft/unpublished → published。published なら false（冪等）。事業者が active でなければ ErrTenantNotActive。
func (p *Plan) Publish(tenantActive bool, now time.Time) (bool, error) {
	if !tenantActive {
		return false, ErrTenantNotActive
	}
	switch p.s.Status {
	case contracts.PlanPublished:
		return false, nil
	case contracts.PlanDraft, contracts.PlanUnpublished:
		p.s.Status, p.s.PublishedAt = contracts.PlanPublished, &now
		return true, nil
	}
	return false, ErrInvalidPlanTransition
}

// Unpublish は published → unpublished。unpublished なら false（冪等）。draft は ErrInvalidPlanTransition。
func (p *Plan) Unpublish() (bool, error) {
	switch p.s.Status {
	case contracts.PlanPublished:
		p.s.Status = contracts.PlanUnpublished
		return true, nil
	case contracts.PlanUnpublished:
		return false, nil
	case contracts.PlanDraft:
		return false, ErrInvalidPlanTransition
	}
	return false, ErrInvalidPlanTransition
}

// AddPriceVersion は価格を改定する（即時適用）。現在の金額と同じなら ErrPriceUnchanged。
func (p *Plan) AddPriceVersion(reg pricing.Registry, id contracts.PriceVersionID, amount int64, now time.Time) (PriceVersion, error) {
	pv, err := newPrice(reg, p.s.Scheme, id, p.s.CurrentPrice.VersionNo+1, amount, now)
	if err != nil {
		return PriceVersion{}, err
	}
	if p.s.CurrentPrice.AmountYen() == amount {
		return PriceVersion{}, ErrPriceUnchanged
	}
	p.s.CurrentPrice = pv
	return pv, nil
}

// UpdateAttributes は名前・説明・トライアル日数を変える（nil は変えない）。変わったら true。
func (p *Plan) UpdateAttributes(name, desc *string, trial *int) (bool, error) {
	n, d, t := p.s.Name, p.s.Description, p.s.TrialDays
	var err error
	if name != nil {
		if n, err = text("name", *name, 1, 100); err != nil {
			return false, err
		}
	}
	if desc != nil {
		if d, err = text("description", *desc, 0, 1000); err != nil {
			return false, err
		}
	}
	if trial != nil {
		if err := trialDays(*trial); err != nil {
			return false, err
		}
		t = *trial
	}
	changed := n != p.s.Name || d != p.s.Description || t != p.s.TrialDays
	p.s.Name, p.s.Description, p.s.TrialDays = n, d, t
	return changed, nil
}

// Snapshot は contracts の形。
func (p *Plan) Snapshot() contracts.PlanSnapshot {
	pv := p.s.CurrentPrice
	var amount sk.Money
	if pv.Amount != nil {
		amount = *pv.Amount
	}
	return contracts.PlanSnapshot{
		PlanID: p.s.ID, ProductID: p.s.ProductID, PriceVersionID: pv.ID, Amount: amount, TaxRate: pv.TaxRate,
		Interval: p.s.Interval, TrialDays: p.s.TrialDays, PricingScheme: string(p.s.Scheme),
	}
}
