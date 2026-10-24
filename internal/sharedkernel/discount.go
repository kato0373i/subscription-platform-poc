package sharedkernel

import "fmt"

type DiscountType string

const (
	DiscountAmount  DiscountType = "amount"
	DiscountPercent DiscountType = "percent"
)

// Discount は D-10 の式の唯一の実装（P-CAT-5）。amount は円（>=1）、percent は 1〜100。
type Discount struct {
	Type  DiscountType
	Value int64
}

func (d Discount) Validate() error {
	switch d.Type {
	case DiscountAmount:
		if d.Value >= 1 {
			return nil
		}
	case DiscountPercent:
		if d.Value >= 1 && d.Value <= 100 {
			return nil
		}
	}
	return fmt.Errorf("%w: %s %d", ErrInvalidDiscount, d.Type, d.Value)
}

// Apply は割引額と割引後の金額を返す。percent: floor(gross×Value/100)、amount: min(Value, gross)。
func (d Discount) Apply(gross Money) (discount, net Money) {
	switch d.Type {
	case DiscountPercent:
		discount = gross.MulRatFloor(d.Value, 100)
	case DiscountAmount:
		discount = Money{yen: min(d.Value, gross.yen)}
	}
	if discount.yen > gross.yen {
		discount = gross
	}
	return discount, gross.SubClamped(discount)
}
