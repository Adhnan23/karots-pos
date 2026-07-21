package products

import (
	"sort"
	"strings"

	"karots-pos/internal/money"

	"github.com/shopspring/decimal"
)

// UnitQty is how much of one unit a branch of the catalogue holds.
type UnitQty struct {
	Abbr string
	Qty  decimal.Decimal
}

// UnitTally is a branch's stock broken down by unit.
//
// It exists because "how many do I have" has no single answer once a branch
// mixes units: 500 pcs plus 12 btl is not 512 of anything. Almost every branch
// in a stationery shop is one unit, so Format collapses to a plain number in
// that case and only names units when it genuinely must.
type UnitTally []UnitQty

// Total sums across units. Only meaningful when the tally has one unit; used
// for ordering and reconciliation, not for display.
func (t UnitTally) Total() decimal.Decimal {
	sum := decimal.Zero
	for _, u := range t {
		sum = sum.Add(u.Qty)
	}
	return sum
}

// Format renders the tally: a bare number for one unit, otherwise the units
// spelled out, largest first.
func (t UnitTally) Format() string {
	if len(t) == 0 {
		return "0"
	}
	if len(t) == 1 {
		return money.Qty(t[0].Qty)
	}
	sorted := make(UnitTally, len(t))
	copy(sorted, t)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Qty.GreaterThan(sorted[j].Qty) })
	parts := make([]string, 0, len(sorted))
	for _, u := range sorted {
		parts = append(parts, money.Qty(u.Qty)+" "+u.Abbr)
	}
	return strings.Join(parts, " · ")
}
