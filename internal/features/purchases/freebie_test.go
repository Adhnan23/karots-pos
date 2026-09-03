package purchases

import (
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { d, _ := decimal.NewFromString(s); return d }

// TestFreeQtyStaysOutOfPayable is the money-safety guard the owner insisted on:
// bonus units must never reach the supplier balance. The subtotal parseLines
// returns is what the shop owes, so free qty must not inflate it.
func TestFreeQtyStaysOutOfPayable(t *testing.T) {
	lines, subtotal, err := parseLines([]ItemInput{
		{ProductID: 1, Quantity: "10", FreeQty: "2", CostPrice: "100", SellingPrice: "150"},
	})
	if err != nil {
		t.Fatalf("parseLines: %v", err)
	}
	if !subtotal.Equal(dec("1000")) {
		t.Errorf("payable subtotal = %s, want 1000 (free units must not be billed)", subtotal)
	}
	ln := lines[0]
	if !ln.Quantity.Equal(dec("10")) || !ln.FreeQty.Equal(dec("2")) {
		t.Errorf("line qty/free = %s/%s, want 10/2", ln.Quantity, ln.FreeQty)
	}
	if !ln.Subtotal.Equal(dec("1000")) {
		t.Errorf("line subtotal = %s, want 1000", ln.Subtotal)
	}
}

func TestLotCost(t *testing.T) {
	cases := []struct {
		name                          string
		paidSubtotal, got, free, list string
		want                          string
	}{
		// buy 10 @ 100 + 2 free: 1000 spread over 12 units.
		{"bonus units blend down", "1000", "12", "2", "100", "83.3333"},
		// no freebies: exact list cost, no divide, no drift.
		{"no free units keeps list cost", "1000", "10", "0", "100", "100"},
		// a free (unpriced) line: zero lets the batch inherit the product cost.
		{"free line stays zero", "0", "5", "0", "0", "0"},
		// bonus units on a zero-cost line still blend to zero (inherit).
		{"zero cost with free blends to zero", "0", "7", "2", "0", "0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := lotCost(dec(c.paidSubtotal), dec(c.got), dec(c.free), dec(c.list))
			if !got.Equal(dec(c.want)) {
				t.Errorf("lotCost = %s, want %s", got, c.want)
			}
		})
	}
}
