package recipes

import (
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { d, _ := decimal.NewFromString(s); return d }

func qty(s string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: dec(s), Valid: true}
}

// A whole-unit component always rounds UP: a single copy uses a whole sheet,
// and three double-sided impressions still use two sheets.
func TestConsumedWholeUnitsRoundsUp(t *testing.T) {
	sheet := Component{ComponentProductID: 1, QtyPerUnit: qty("1"), WholeUnits: true}
	cases := map[string]string{"1": "1", "3": "3", "10": "10"}
	for in, want := range cases {
		if got := sheet.Consumed(dec(in)); !got.Equal(dec(want)) {
			t.Errorf("Consumed(%s) = %s, want %s", in, got, want)
		}
	}
	half := Component{ComponentProductID: 1, QtyPerUnit: qty("0.5"), WholeUnits: true}
	if got := half.Consumed(dec("3")); !got.Equal(dec("2")) {
		t.Errorf("half-sheet x3 = %s, want 2 (rounded up)", got)
	}
}

// The bug this feature exists to fix: a yield component must NOT round up, or a
// one-copy job consumes an entire toner cartridge.
func TestConsumedYieldStaysFractional(t *testing.T) {
	toner := Component{ComponentProductID: 2, YieldUnits: qty("5000")}
	got := toner.Consumed(dec("1"))
	if !got.Equal(dec("0.0002")) {
		t.Fatalf("1 copy of a 5000-yield toner consumed %s, want 0.0002", got)
	}
	if got.Equal(dec("1")) {
		t.Fatal("yield component rounded up to a whole unit — the Ceil bug is back")
	}
	if got := toner.Consumed(dec("2500")); !got.Equal(dec("0.5")) {
		t.Errorf("2500 copies = %s, want 0.5 of a cartridge", got)
	}
}

// Grams of coffee: fractional and stated per unit, no yield involved.
func TestConsumedFractionalPerUnit(t *testing.T) {
	powder := Component{ComponentProductID: 3, QtyPerUnit: qty("18")}
	if got := powder.Consumed(dec("3")); !got.Equal(dec("54")) {
		t.Errorf("3 cups x 18g = %s, want 54", got)
	}
}

// A yield that does not divide evenly must not be silently truncated to zero.
func TestConsumedAwkwardYieldKeepsPrecision(t *testing.T) {
	bag := Component{ComponentProductID: 4, YieldUnits: qty("3000")}
	got := bag.Consumed(dec("1"))
	if got.IsZero() {
		t.Fatal("1/3000 truncated to zero — precision lost")
	}
	// Six decimal places is what the stock columns can store (migration 0045).
	if got.Exponent() < -6 {
		t.Errorf("Consumed returned %s, finer than stock can store (6dp)", got)
	}
}

func TestExpandSkipsNothingAndSumsPerComponent(t *testing.T) {
	cs := []Component{
		{ComponentProductID: 1, QtyPerUnit: qty("1"), WholeUnits: true},
		{ComponentProductID: 2, YieldUnits: qty("5000")},
		{ComponentProductID: 3, QtyPerUnit: qty("18")},
	}
	out := Expand(cs, dec("10"))
	if len(out) != 3 {
		t.Fatalf("Expand returned %d consumptions, want 3", len(out))
	}
	want := map[int64]string{1: "10", 2: "0.002", 3: "180"}
	for _, c := range out {
		if !c.Qty.Equal(dec(want[c.ProductID])) {
			t.Errorf("product %d consumed %s, want %s", c.ProductID, c.Qty, want[c.ProductID])
		}
	}
}

// A zero or negative sale quantity must consume nothing rather than produce a
// negative stock movement.
func TestExpandIgnoresNonPositiveQuantity(t *testing.T) {
	cs := []Component{{ComponentProductID: 1, QtyPerUnit: qty("1")}}
	if out := Expand(cs, dec("0")); len(out) != 0 {
		t.Errorf("qty 0 produced %d consumptions, want 0", len(out))
	}
	if out := Expand(cs, dec("-5")); len(out) != 0 {
		t.Errorf("negative qty produced %d consumptions, want 0", len(out))
	}
}

// The coffee example the owner described: milk powder by weight, a whole paper
// cup, and electricity as a non-stock cost line. Stock cost and true cost must
// be reported separately so only the former can ever reach COGS.
func TestCostPerUnitSeparatesStockFromOtherCosts(t *testing.T) {
	cs := []Component{
		{ComponentProductID: 1, QtyPerUnit: qty("18"), CostPrice: dec("1.20")},               // 18 g @ 1.20 = 21.60
		{ComponentProductID: 2, QtyPerUnit: qty("1"), WholeUnits: true, CostPrice: dec("8")}, // 8.00
	}
	costs := []CostLine{{Label: "Electricity", CostPerUnit: dec("3")}}

	got := CostPerUnit(cs, costs)
	if !got.Stock.Equal(dec("29.60")) {
		t.Errorf("stock cost = %s, want 29.60", got.Stock)
	}
	if !got.Other.Equal(dec("3")) {
		t.Errorf("other cost = %s, want 3", got.Other)
	}
	if !got.True().Equal(dec("32.60")) {
		t.Errorf("true cost = %s, want 32.60", got.True())
	}
}

// A yield component spreads one unit across many sales: a bag making 50 cups at
// Rs 1000 costs Rs 20 a cup, not Rs 1000.
func TestCostPerUnitUsesYieldNotWholeUnit(t *testing.T) {
	cs := []Component{{ComponentProductID: 1, YieldUnits: qty("50"), CostPrice: dec("1000")}}
	if got := CostPerUnit(cs, nil); !got.Stock.Equal(dec("20")) {
		t.Errorf("stock cost = %s, want 20", got.Stock)
	}
}

// With no cost lines the true cost is exactly the stock cost — the feature must
// not shift the numbers of every existing recipe.
func TestCostPerUnitWithoutCostLines(t *testing.T) {
	cs := []Component{{ComponentProductID: 1, QtyPerUnit: qty("2"), CostPrice: dec("5")}}
	got := CostPerUnit(cs, nil)
	if !got.True().Equal(got.Stock) || !got.Stock.Equal(dec("10")) {
		t.Errorf("stock=%s true=%s, want both 10", got.Stock, got.True())
	}
}
