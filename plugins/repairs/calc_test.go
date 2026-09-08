package repairs

import (
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestJobTotals(t *testing.T) {
	det := &Detail{
		Parts: []Part{
			{Qty: d("2"), UnitCharge: d("500"), DiscountType: "percent", DiscountValue: d("10")}, // 1000 - 100 = 900
			{Qty: d("1"), UnitCharge: d("300"), DiscountType: "fixed", DiscountValue: d("0")},    // 300
		},
		Charges:  []Charge{{Label: "Labour", Amount: d("700")}},
		Payments: []Payment{{Amount: d("400"), Kind: "deposit"}},
	}
	total, dep, bal := JobTotals(det)
	if !total.Equal(d("1900")) {
		t.Fatalf("total = %s, want 1900", total)
	}
	if !dep.Equal(d("400")) || !bal.Equal(d("1500")) {
		t.Fatalf("dep/bal = %s/%s, want 400/1500", dep, bal)
	}
}

func TestJobTotalsRefundAndOverpay(t *testing.T) {
	det := &Detail{
		Charges: []Charge{{Amount: d("100"), Label: "Labour"}},
		Payments: []Payment{
			{Amount: d("150"), Kind: "deposit"},
			{Amount: d("50"), Kind: "refund"},
		},
	}
	total, dep, bal := JobTotals(det)
	// deposit 150 - refund 50 = 100; total 100; balance 0 (fully covered).
	if !total.Equal(d("100")) || !dep.Equal(d("100")) || !bal.Equal(d("0")) {
		t.Fatalf("total/dep/bal = %s/%s/%s, want 100/100/0", total, dep, bal)
	}
}

func TestSaleItemsLines(t *testing.T) {
	det := &Detail{
		Parts:   []Part{{ProductID: 5, Qty: d("1"), UnitCharge: d("1000"), DiscountType: "percent", DiscountValue: d("10")}},
		Charges: []Charge{{Amount: d("500"), Label: "Labour"}},
	}
	items := saleItems(det, 99)
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].ProductID != 5 || items[0].DiscountType != "percent" || items[0].Discount != "10" {
		t.Fatalf("bad part line: %+v", items[0])
	}
	if items[1].ProductID != 99 || items[1].PriceOverride != "500" {
		t.Fatalf("charge line must ring on labour product with PriceOverride: %+v", items[1])
	}
}

// TestCollectionTenders: deposit -> wallet, pay-now -> method, remainder -> credit.
func TestCollectionTenders(t *testing.T) {
	// deposit 400, pay 700 cash now, 400 left on account (total 1500).
	ps := collectionTenders(d("400"), d("700"), d("400"), "cash")
	got := map[string]string{}
	for _, p := range ps {
		got[p.Method] = p.Amount
	}
	if got["wallet"] != "400.00" || got["cash"] != "700.00" || got["credit"] != "400.00" {
		t.Fatalf("tenders = %+v, want wallet 400 / cash 700 / credit 400", got)
	}

	// pay everything now: single cash tender, no wallet/credit.
	ps = collectionTenders(d("0"), d("500"), d("0"), "card")
	if len(ps) != 1 || ps[0].Method != "card" || ps[0].Amount != "500.00" {
		t.Fatalf("want single card 500, got %+v", ps)
	}

	// whole thing on account: single credit tender.
	ps = collectionTenders(d("0"), d("0"), d("500"), "credit")
	if len(ps) != 1 || ps[0].Method != "credit" || ps[0].Amount != "500.00" {
		t.Fatalf("want single credit 500, got %+v", ps)
	}
}
