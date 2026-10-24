package sharedkernel_test

import (
	"errors"
	"testing"

	sk "subscription-platform/internal/sharedkernel"
)

// AC-PLT-003
func TestMoneyInvariants(t *testing.T) {
	if _, err := sk.NewMoney(-1); !errors.Is(err, sk.ErrNegativeMoney) {
		t.Fatalf("NewMoney(-1) err = %v", err)
	}
	if _, err := sk.MustMoney(100).Sub(sk.MustMoney(101)); !errors.Is(err, sk.ErrNegativeMoney) {
		t.Fatalf("Sub err = %v", err)
	}
	if got := sk.MustMoney(100).SubClamped(sk.MustMoney(101)); !got.IsZero() {
		t.Fatalf("SubClamped = %v", got)
	}
	if _, err := sk.MustMoney(1 << 62).Add(sk.MustMoney(1 << 62)); !errors.Is(err, sk.ErrMoneyOverflow) {
		t.Fatalf("Add overflow err = %v", err)
	}
}

func TestMulRatFloorPanicsOnInvalidInput(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("panic しなかった")
		}
	}()
	sk.MustMoney(100).MulRatFloor(1, 0)
}

// AC-PLT-004, AC-PLT-005
func TestTaxRate(t *testing.T) {
	r, err := sk.ParseTaxRate("0.100")
	if err != nil || r.Num() != 1 || r.Den() != 10 {
		t.Fatalf("ParseTaxRate(0.100) = %v %v", r, err)
	}
	if r2, _ := sk.ParseTaxRate("0.10"); r2 != r {
		t.Fatalf("0.10 と 0.100 が違う: %v", r2)
	}
	for _, bad := range []string{"1.000", "-0.1", "abc", "0.1000", ""} {
		if _, err := sk.ParseTaxRate(bad); !errors.Is(err, sk.ErrInvalidTaxRate) {
			t.Errorf("ParseTaxRate(%q) err = %v", bad, err)
		}
	}
	if r.String() != "0.100" {
		t.Fatalf("String = %q", r.String())
	}
	tests := []struct{ gross, tax int64 }{
		{1100, 100}, {1000, 90}, {1, 0}, {1_000_000, 90_909},
	}
	for _, tt := range tests {
		if got := r.InclusiveTax(sk.MustMoney(tt.gross)).Int64(); got != tt.tax {
			t.Errorf("InclusiveTax(%d) = %d, want %d", tt.gross, got, tt.tax)
		}
	}
}

// AC-PLT-006
func TestDiscountApply(t *testing.T) {
	tests := []struct {
		d             sk.Discount
		gross         int64
		discount, net int64
	}{
		{sk.Discount{Type: sk.DiscountPercent, Value: 15}, 980, 147, 833},
		{sk.Discount{Type: sk.DiscountAmount, Value: 500}, 300, 300, 0},
		{sk.Discount{Type: sk.DiscountAmount, Value: 500}, 980, 500, 480},
		{sk.Discount{Type: sk.DiscountPercent, Value: 100}, 980, 980, 0},
	}
	for _, tt := range tests {
		d, n := tt.d.Apply(sk.MustMoney(tt.gross))
		if d.Int64() != tt.discount || n.Int64() != tt.net {
			t.Errorf("%+v.Apply(%d) = %d/%d, want %d/%d", tt.d, tt.gross, d.Int64(), n.Int64(), tt.discount, tt.net)
		}
	}
	if err := (sk.Discount{Type: sk.DiscountPercent, Value: 101}).Validate(); err == nil {
		t.Fatal("percent 101 が通った")
	}
}
