package sharedkernel

import (
	"fmt"
	"math"
	"math/big"
)

// Money は日本円の金額。不変条件: yen >= 0。
type Money struct{ yen int64 }

func NewMoney(yen int64) (Money, error) {
	if yen < 0 {
		return Money{}, fmt.Errorf("%w: %d", ErrNegativeMoney, yen)
	}
	return Money{yen: yen}, nil
}

// MustMoney はテストと定数用。負なら panic。
func MustMoney(yen int64) Money {
	m, err := NewMoney(yen)
	if err != nil {
		panic(err)
	}
	return m
}

func (m Money) Int64() int64 { return m.yen }
func (m Money) IsZero() bool { return m.yen == 0 }

func (m Money) Cmp(o Money) int {
	switch {
	case m.yen < o.yen:
		return -1
	case m.yen > o.yen:
		return 1
	default:
		return 0
	}
}

func (m Money) Add(o Money) (Money, error) {
	if m.yen > math.MaxInt64-o.yen {
		return Money{}, ErrMoneyOverflow
	}
	return Money{yen: m.yen + o.yen}, nil
}

func (m Money) Sub(o Money) (Money, error) {
	if o.yen > m.yen {
		return Money{}, fmt.Errorf("%w: %d - %d", ErrNegativeMoney, m.yen, o.yen)
	}
	return Money{yen: m.yen - o.yen}, nil
}

// SubClamped は結果が 0 未満なら 0 にする（D-10）。
func (m Money) SubClamped(o Money) Money {
	if o.yen >= m.yen {
		return Money{}
	}
	return Money{yen: m.yen - o.yen}
}

// MulRatFloor は floor(m*num/den)。前提 num>=0, den>0。前提違反と桁あふれは panic（プログラムの誤り）。
func (m Money) MulRatFloor(num, den int64) Money {
	if num < 0 || den <= 0 {
		panic(fmt.Sprintf("sharedkernel: MulRatFloor precondition violated num=%d den=%d", num, den))
	}
	r := new(big.Int).Mul(big.NewInt(m.yen), big.NewInt(num))
	r.Quo(r, big.NewInt(den)) // 非負同士なので切り捨て = floor
	if !r.IsInt64() {
		panic("sharedkernel: MulRatFloor overflow")
	}
	return Money{yen: r.Int64()}
}

func (m Money) String() string { return fmt.Sprintf("%d JPY", m.yen) }
