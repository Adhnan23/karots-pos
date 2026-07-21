package purchases

import (
	"reflect"
	"testing"

	"github.com/shopspring/decimal"
)

// TestInputsCarryNoPaidAmount is a compile-time guard with teeth: it fails if
// anyone reintroduces a paid-amount field on the purchase inputs.
//
// That field used to mark an invoice paid and clear the supplier's balance
// while moving no money at all — no payment row, no receipt, no cash out of any
// drawer. Paying now goes through supplierpay + cashflow in the web layer,
// which this package cannot reach (supplierpay imports purchases).
func TestInputsCarryNoPaidAmount(t *testing.T) {
	assertNoField(t, CreateInput{}, "PaidAmount")
	assertNoField(t, ReceiveInput{}, "PaidAmount")
}

func assertNoField(t *testing.T, v any, field string) {
	t.Helper()
	if _, ok := reflect.TypeOf(v).FieldByName(field); ok {
		t.Fatalf("%T still has a %s field — paying must go through supplierpay so the cash actually moves",
			v, field)
	}
}

// TestRemainderLines pins what stays on order after a short delivery.
//
// The bug this guards: the remainder used to be worked out from the lines that
// actually arrived, so an ordered item that turned up in no quantity at all was
// simply forgotten — the shop believed it was still coming when nothing in the
// system said so. The truth about what was ordered is the draft's own lines.
func TestRemainderLines(t *testing.T) {
	dec := func(s string) decimal.Decimal { d, _ := decimal.NewFromString(s); return d }
	ordered := []PurchaseItem{
		{ProductID: 1, Quantity: dec("12"), CostPrice: dec("100"), SellingPrice: dec("150")},
		{ProductID: 2, Quantity: dec("5"), CostPrice: dec("200"), SellingPrice: dec("280")},
		{ProductID: 3, Quantity: dec("3"), CostPrice: dec("50"), SellingPrice: dec("80")},
	}

	t.Run("a line that arrived short and one that never arrived", func(t *testing.T) {
		got := remainderLines(ordered, []PurchaseItem{
			{ProductID: 1, Quantity: dec("10")},
			{ProductID: 2, Quantity: dec("5")},
		})
		want := map[int64]string{1: "2", 3: "3"}
		assertRemainder(t, got, want)
	})

	t.Run("everything arrived", func(t *testing.T) {
		got := remainderLines(ordered, []PurchaseItem{
			{ProductID: 1, Quantity: dec("12")},
			{ProductID: 2, Quantity: dec("5")},
			{ProductID: 3, Quantity: dec("3")},
		})
		assertRemainder(t, got, map[int64]string{})
	})

	t.Run("nothing arrived", func(t *testing.T) {
		got := remainderLines(ordered, nil)
		assertRemainder(t, got, map[int64]string{1: "12", 2: "5", 3: "3"})
	})

	t.Run("overstock leaves nothing owing", func(t *testing.T) {
		got := remainderLines(ordered, []PurchaseItem{
			{ProductID: 1, Quantity: dec("20")},
			{ProductID: 2, Quantity: dec("5")},
			{ProductID: 3, Quantity: dec("3")},
		})
		assertRemainder(t, got, map[int64]string{})
	})

	t.Run("an extra item nobody ordered is not owed back", func(t *testing.T) {
		got := remainderLines(ordered, []PurchaseItem{
			{ProductID: 1, Quantity: dec("12")},
			{ProductID: 2, Quantity: dec("5")},
			{ProductID: 3, Quantity: dec("3")},
			{ProductID: 99, Quantity: dec("7")},
		})
		assertRemainder(t, got, map[int64]string{})
	})

	t.Run("the same product on two ordered lines is totalled", func(t *testing.T) {
		got := remainderLines([]PurchaseItem{
			{ProductID: 1, Quantity: dec("4"), CostPrice: dec("100"), SellingPrice: dec("150")},
			{ProductID: 1, Quantity: dec("6"), CostPrice: dec("100"), SellingPrice: dec("150")},
		}, []PurchaseItem{{ProductID: 1, Quantity: dec("7")}})
		assertRemainder(t, got, map[int64]string{1: "3"})
	})

	t.Run("carries cost and price so the new order is complete", func(t *testing.T) {
		got := remainderLines(ordered, []PurchaseItem{{ProductID: 1, Quantity: dec("10")}})
		for _, l := range got {
			if l.CostPrice == "" || l.SellingPrice == "" {
				t.Fatalf("line for product %d lost its prices: %+v", l.ProductID, l)
			}
		}
	})
}

func assertRemainder(t *testing.T, got []ItemInput, want map[int64]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d remainder lines %+v, want %d", len(got), got, len(want))
	}
	for _, l := range got {
		w, ok := want[l.ProductID]
		if !ok {
			t.Fatalf("product %d should not be on the remainder order", l.ProductID)
		}
		if l.Quantity != w {
			t.Errorf("product %d remainder = %s, want %s", l.ProductID, l.Quantity, w)
		}
	}
}
