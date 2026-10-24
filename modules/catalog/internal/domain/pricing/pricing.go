// Package pricing は価格の算出方式（PricingScheme）とレジストリ（catalog.md §3.3・§10）。
// 従量課金・数量課金を足すときは、このパッケージに Scheme の実装を足して Registry に登録する。
package pricing

import (
	"errors"
	"fmt"

	sk "subscription-platform/internal/sharedkernel"
	"subscription-platform/modules/catalog/contracts"
)

// Kind は算出方式の種類。
type Kind string

const KindFixed Kind = "fixed"

// MinAmount・MaxAmount は 1 回の価格の範囲（税込、D-08）。
const (
	MinAmount int64 = 1
	MaxAmount int64 = 1_000_000
)

var (
	ErrAmountOutOfRange = errors.New("catalog: amount out of range")
	ErrInvalidParams    = errors.New("catalog: invalid scheme params")
	ErrUnknownScheme    = errors.New("catalog: unknown pricing scheme")
)

// Params はスキーム固有のパラメータ（fixed は空）。
type Params map[string]any

// Price は価格版のうち算出に使う部分。
type Price struct {
	Kind   Kind
	Amount *sk.Money
	Params Params
}

// Scheme は算出方式。
type Scheme interface {
	Kind() Kind
	ValidateParams(amount *sk.Money, params Params) error
	Quote(p Price, usage *contracts.UsageInput) (sk.Money, error)
}

// Registry は使える算出方式（module.go で組み立てる）。
type Registry map[Kind]Scheme

// Default は PoC のレジストリ（fixed だけ）。
func Default() Registry { return Registry{KindFixed: Fixed{}} }

// Get は kind の Scheme。
func (r Registry) Get(k Kind) (Scheme, error) {
	s, ok := r[k]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownScheme, k)
	}
	return s, nil
}

// Fixed は固定額。
type Fixed struct{}

func (Fixed) Kind() Kind { return KindFixed }

func (Fixed) ValidateParams(amount *sk.Money, params Params) error {
	if amount == nil || amount.Int64() < MinAmount || amount.Int64() > MaxAmount {
		return ErrAmountOutOfRange
	}
	if len(params) != 0 {
		return ErrInvalidParams
	}
	return nil
}

func (Fixed) Quote(p Price, usage *contracts.UsageInput) (sk.Money, error) {
	if usage != nil {
		return sk.Money{}, contracts.ErrUsageNotSupported
	}
	if p.Amount == nil {
		return sk.Money{}, ErrAmountOutOfRange
	}
	return *p.Amount, nil
}
