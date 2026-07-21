package products

import (
	"testing"

	"github.com/shopspring/decimal"
)

func tdec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// The normal case for this shop: one unit, so no unit name is needed.
func TestUnitTallyFormatsASingleUnitAsAPlainNumber(t *testing.T) {
	tl := UnitTally{{Abbr: "pcs", Qty: tdec("1755")}}
	if got := tl.Format(); got != "1,755" {
		t.Errorf("Format = %q, want %q", got, "1,755")
	}
}

// Mixed units must never be summed into one meaningless figure.
func TestUnitTallyFormatsMixedUnitsSeparately(t *testing.T) {
	tl := UnitTally{{Abbr: "btl", Qty: tdec("12")}, {Abbr: "pcs", Qty: tdec("412")}}
	if got := tl.Format(); got != "412 pcs · 12 btl" {
		t.Errorf("Format = %q, want %q", got, "412 pcs · 12 btl")
	}
}

func TestUnitTallyFormatsEmptyAsZero(t *testing.T) {
	if got := (UnitTally{}).Format(); got != "0" {
		t.Errorf("Format = %q, want %q", got, "0")
	}
}

// Total is only meaningful for reconciliation, but it must still be right.
func TestUnitTallyTotal(t *testing.T) {
	tl := UnitTally{{Abbr: "pcs", Qty: tdec("412")}, {Abbr: "btl", Qty: tdec("12")}}
	if got := tl.Total(); !got.Equal(tdec("424")) {
		t.Errorf("Total = %s, want 424", got)
	}
}
