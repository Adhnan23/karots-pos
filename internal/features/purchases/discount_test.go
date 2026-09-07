package purchases

import "testing"

// TestResolveItemDiscount: fixed is per-unit (× qty), percent is off the line,
// and either is clamped so a fat-fingered discount never exceeds the line.
func TestResolveItemDiscount(t *testing.T) {
	// 5 off each of 10 units = 50 off a 1000 line.
	if got := resolveItemDiscount("fixed", dec("5"), dec("1000"), dec("10")); !got.Equal(dec("50")) {
		t.Errorf("fixed per-unit = %s, want 50", got)
	}
	// 10% off a 1000 line = 100.
	if got := resolveItemDiscount("percent", dec("10"), dec("1000"), dec("10")); !got.Equal(dec("100")) {
		t.Errorf("percent = %s, want 100", got)
	}
	// 200/unit × 10 = 2000, but the line is only 1000 — clamp.
	if got := resolveItemDiscount("fixed", dec("200"), dec("1000"), dec("10")); !got.Equal(dec("1000")) {
		t.Errorf("clamp = %s, want 1000", got)
	}
}

// TestResolveBillDiscount: percent off the net base, fixed flat, both clamped.
func TestResolveBillDiscount(t *testing.T) {
	if got := resolveBillDiscount("percent", dec("10"), dec("900")); !got.Equal(dec("90")) {
		t.Errorf("percent = %s, want 90", got)
	}
	if got := resolveBillDiscount("fixed", dec("100"), dec("900")); !got.Equal(dec("100")) {
		t.Errorf("fixed = %s, want 100", got)
	}
	if got := resolveBillDiscount("fixed", dec("5000"), dec("900")); !got.Equal(dec("900")) {
		t.Errorf("clamp = %s, want 900", got)
	}
}

// TestParseLinesItemDiscount: a per-item discount comes off the line, so the
// payable subtotal and the stored line net both drop, and the resolved amount
// is recorded on the line.
func TestParseLinesItemDiscount(t *testing.T) {
	lines, subtotal, err := parseLines([]ItemInput{
		{ProductID: 1, Quantity: "10", CostPrice: "100", Discount: "10", DiscountType: "percent"},
	})
	if err != nil {
		t.Fatalf("parseLines: %v", err)
	}
	if !subtotal.Equal(dec("900")) {
		t.Errorf("payable subtotal = %s, want 900 (10%% off 1000)", subtotal)
	}
	ln := lines[0]
	if !ln.Discount.Equal(dec("100")) || !ln.Subtotal.Equal(dec("900")) {
		t.Errorf("line discount/net = %s/%s, want 100/900", ln.Discount, ln.Subtotal)
	}
}

// TestLotCostWithDiscount: the whole point — a discount (even with no free
// units) lowers the received per-unit cost, so the saving shows as margin.
func TestLotCostWithDiscount(t *testing.T) {
	// 10 units, 900 paid after a 100 discount (was 1000) → 90/unit, not 100.
	if got := lotCost(dec("900"), dec("10"), dec("0"), dec("100")); !got.Equal(dec("90")) {
		t.Errorf("discounted lot cost = %s, want 90", got)
	}
	// No discount and no free units still lands exactly on the list cost.
	if got := lotCost(dec("1000"), dec("10"), dec("0"), dec("100")); !got.Equal(dec("100")) {
		t.Errorf("undiscounted lot cost = %s, want 100", got)
	}
}
