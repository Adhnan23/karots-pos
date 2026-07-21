package stock

import (
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// The ordinary case: cost comes from the batches the units were drawn from.
func TestCostOfConsumedUsesBatchCost(t *testing.T) {
	got := costOfConsumed([]consumedLot{{Qty: d("2"), Cost: d("10")}}, d("2"), d("999"))
	if !got.Equal(d("10")) {
		t.Errorf("cost = %s, want 10 (the batch cost, not the product cost)", got)
	}
}

// The trap this exists to close. A batch created before its cost was known
// carries 0. Charging Rs 0 for a Rs 1100 bag of premix silently overstates
// profit on every cup sold from it.
func TestCostOfConsumedFallsBackToProductCostForAZeroCostBatch(t *testing.T) {
	got := costOfConsumed([]consumedLot{{Qty: d("0.02"), Cost: decimal.Zero}}, d("0.02"), d("1100"))
	if !got.Equal(d("1100")) {
		t.Errorf("cost = %s, want 1100 (fell back to the product's cost)", got)
	}
}

// A mix must be rescued lot by lot, not by testing the final average: one good
// batch would make the average non-zero and hide the free one.
func TestCostOfConsumedRescuesOnlyTheZeroCostLots(t *testing.T) {
	// 1 unit @ 10 (real) + 1 unit @ 0 (should become 20) = 30 over 2 units = 15
	got := costOfConsumed([]consumedLot{
		{Qty: d("1"), Cost: d("10")},
		{Qty: d("1"), Cost: decimal.Zero},
	}, d("2"), d("20"))
	if !got.Equal(d("15")) {
		t.Errorf("cost = %s, want 15", got)
	}
}

// A genuinely free item — no batch cost and no product cost — must stay free
// rather than being invented.
func TestCostOfConsumedLeavesATrulyFreeItemAtZero(t *testing.T) {
	got := costOfConsumed([]consumedLot{{Qty: d("5"), Cost: decimal.Zero}}, d("5"), decimal.Zero)
	if !got.IsZero() {
		t.Errorf("cost = %s, want 0", got)
	}
}

// Weighted, not a plain mean: 9 units at 1 and 1 unit at 11 averages 2, not 6.
func TestCostOfConsumedIsWeightedByQuantity(t *testing.T) {
	got := costOfConsumed([]consumedLot{
		{Qty: d("9"), Cost: d("1")},
		{Qty: d("1"), Cost: d("11")},
	}, d("10"), decimal.Zero)
	if !got.Equal(d("2")) {
		t.Errorf("cost = %s, want 2", got)
	}
}

func TestCostOfConsumedHandlesNoQuantity(t *testing.T) {
	if got := costOfConsumed(nil, decimal.Zero, d("50")); !got.IsZero() {
		t.Errorf("cost = %s, want 0", got)
	}
}
