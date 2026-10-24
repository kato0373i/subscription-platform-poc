package sharedkernel

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// TaxRate は既約分数の税率。不変条件: 0 <= num < den。
type TaxRate struct{ num, den int64 }

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func NewTaxRate(num, den int64) (TaxRate, error) {
	if den <= 0 || num < 0 || num >= den {
		return TaxRate{}, fmt.Errorf("%w: %d/%d", ErrInvalidTaxRate, num, den)
	}
	if num == 0 {
		return TaxRate{num: 0, den: 1}, nil
	}
	g := gcd(num, den)
	return TaxRate{num: num / g, den: den / g}, nil
}

var taxRatePattern = regexp.MustCompile(`^[0-9](\.[0-9]{1,3})?$`)

// ParseTaxRate は "0.10" や "0.100"（DB の numeric(4,3)）を分数にする。浮動小数点を経由しない。
func ParseTaxRate(s string) (TaxRate, error) {
	if !taxRatePattern.MatchString(s) {
		return TaxRate{}, fmt.Errorf("%w: %q", ErrInvalidTaxRate, s)
	}
	intPart, frac, _ := strings.Cut(s, ".")
	den := int64(1)
	for range frac {
		den *= 10
	}
	i, _ := strconv.ParseInt(intPart, 10, 64)
	var f int64
	if frac != "" {
		f, _ = strconv.ParseInt(frac, 10, 64)
	}
	return NewTaxRate(i*den+f, den)
}

func (r TaxRate) Num() int64 { return r.num }
func (r TaxRate) Den() int64 { return r.den }

// String は小数 3 桁で返す（例 "0.100"）。
func (r TaxRate) String() string {
	den := r.den
	if den == 0 {
		den = 1
	}
	milli := r.num * 1000 / den
	return fmt.Sprintf("%d.%03d", milli/1000, milli%1000)
}

// InclusiveTax は内税の消費税額 floor(gross*num/(den+num))（D-09）。
func (r TaxRate) InclusiveTax(gross Money) Money {
	if r.num == 0 {
		return Money{}
	}
	return gross.MulRatFloor(r.num, r.den+r.num)
}
