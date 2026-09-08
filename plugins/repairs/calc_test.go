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

func TestBuildCollectionSaleTenders(t *testing.T) {
	det := &Detail{
		Parts:    []Part{{ProductID: 5, Qty: d("1"), UnitCharge: d("1000")}},
		Charges:  []Charge{{Amount: d("500"), Label: "Labour"}},
		Payments: []Payment{{Amount: d("400"), Kind: "deposit"}},
	}
	in := BuildCollectionSale(det, 99, nil, "cash")
	if len(in.Payments) != 2 {
		t.Fatalf("want 2 tenders, got %d", len(in.Payments))
	}
	var wallet, cash string
	for _, p := range in.Payments {
		if p.Method == "wallet" {
			wallet = p.Amount
		}
		if p.Method == "cash" {
			cash = p.Amount
		}
	}
	if wallet != "400" || cash != "1100" {
		t.Fatalf("tenders wallet/cash = %s/%s, want 400/1100", wallet, cash)
	}
	if len(in.Items) != 2 {
		t.Fatalf("want 2 items, got %d", len(in.Items))
	}
	// The charge line rings on the labour product with a price override.
	var chargeLine *struct{ ok bool }
	for _, it := range in.Items {
		if it.ProductID == 99 && it.PriceOverride == "500" {
			chargeLine = &struct{ ok bool }{true}
		}
	}
	if chargeLine == nil {
		t.Fatal("charge line missing (labour product + PriceOverride)")
	}
}

func TestBuildCollectionSaleNoDeposit(t *testing.T) {
	det := &Detail{Charges: []Charge{{Amount: d("500"), Label: "Labour"}}}
	in := BuildCollectionSale(det, 99, nil, "card")
	if len(in.Payments) != 1 || in.Payments[0].Method != "card" || in.Payments[0].Amount != "500" {
		t.Fatalf("want single card 500 tender, got %+v", in.Payments)
	}
}
