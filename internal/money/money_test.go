package money

import (
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// A whole count must read as a whole number: "1,755", never "1,755.00".
func TestQtyDropsTrailingZeros(t *testing.T) {
	if got := Qty(dec("1755.000000")); got != "1,755" {
		t.Errorf("Qty = %q, want %q", got, "1,755")
	}
}

// A genuine fraction survives — 3.6 bags of premix is the whole point.
func TestQtyKeepsRealFraction(t *testing.T) {
	if got := Qty(dec("3.600000")); got != "3.6" {
		t.Errorf("Qty = %q, want %q", got, "3.6")
	}
}

func TestQtyRoundsToThreePlaces(t *testing.T) {
	if got := Qty(dec("0.0204")); got != "0.02" {
		t.Errorf("Qty = %q, want %q", got, "0.02")
	}
}

func TestQtyHandlesZeroAndNegative(t *testing.T) {
	if got := Qty(decimal.Zero); got != "0" {
		t.Errorf("Qty(0) = %q, want %q", got, "0")
	}
	if got := Qty(dec("-12.5")); got != "-12.5" {
		t.Errorf("Qty(-12.5) = %q, want %q", got, "-12.5")
	}
}
