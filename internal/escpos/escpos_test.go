package escpos

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"karots-pos/internal/features/sales"
	"karots-pos/internal/features/settings"

	"github.com/shopspring/decimal"
)

func TestDrawerKick(t *testing.T) {
	if DrawerKick(settings.Settings{OpenCashDrawer: false}) != nil {
		t.Fatal("disabled must return nil")
	}
	pin2 := DrawerKick(settings.Settings{OpenCashDrawer: true, DrawerKickPin: 0})
	if !bytes.Equal(pin2, []byte{0x1B, 0x70, 0x00, 0x19, 0xFA}) {
		t.Fatalf("pin2 = % x", pin2)
	}
	pin5 := DrawerKick(settings.Settings{OpenCashDrawer: true, DrawerKickPin: 1})
	if !bytes.Equal(pin5, []byte{0x1B, 0x70, 0x01, 0x19, 0xFA}) {
		t.Fatalf("pin5 = % x", pin5)
	}
}

func sampleDetail() sales.Detail {
	d := decimal.RequireFromString
	cust := "Walk-in"
	return sales.Detail{
		Sale: sales.Sale{
			ReceiptNo:    "R-0001",
			Subtotal:     d("1500.00"),
			Discount:     d("100.00"),
			Total:        d("1400.00"),
			PaidAmount:   d("2000.00"),
			ChangeGiven:  d("600.00"),
			Status:       "paid",
			CashierName:  "Kamal",
			CustomerName: &cust,
			CreatedAt:    time.Date(2026, 6, 10, 14, 30, 0, 0, time.UTC),
		},
		Items: []sales.SaleItem{
			{ProductName: "Onion", Quantity: d("2.25"), UnitAbbr: "kg", UnitPrice: d("400.00"), Subtotal: d("900.00")},
			{ProductName: "Rice 5kg Bag Premium Long Grain White", Quantity: d("1"), UnitAbbr: "pc", UnitPrice: d("600.00"), Subtotal: d("600.00")},
		},
		Payments: []sales.Payment{{Method: "cash", Amount: d("2000.00")}},
	}
}

// A sale where the customer left surplus behind: paid 2000 on a 1400 bill but
// only took 500 change, so kept = 2000 - 500 - 1400 = 100. With no account
// attribution the whole 100 is a walk-in rounding gain ("our money"); with
// RoundingToAccount=100 it went onto the customer's balance instead.
func TestDocumentShowsRounding(t *testing.T) {
	d := decimal.RequireFromString

	walkIn := sampleDetail()
	walkIn.Sale.ChangeGiven = d("500.00") // gave back 500 of the 600 due
	s := string(Document(walkIn, cfg("80"), Options{}))
	if !strings.Contains(s, "CHANGE") || !strings.Contains(s, "Rs. 500.00") {
		t.Errorf("expected CHANGE 500.00, got:\n%s", s)
	}
	if !strings.Contains(s, "Change kept") || !strings.Contains(s, "Rs. 100.00") {
		t.Errorf("expected Change kept 100.00 line, got:\n%s", s)
	}
	if strings.Contains(s, "On account") {
		t.Errorf("walk-in kept must not be labelled On account")
	}

	onAcct := sampleDetail()
	onAcct.Sale.ChangeGiven = d("500.00")
	onAcct.Sale.RoundingToAccount = d("100.00")
	s = string(Document(onAcct, cfg("80"), Options{}))
	if !strings.Contains(s, "On account") || !strings.Contains(s, "Rs. 100.00") {
		t.Errorf("expected On account 100.00 line, got:\n%s", s)
	}
	if strings.Contains(s, "Change kept") {
		t.Errorf("fully-attributed kept must not also show a Change kept line")
	}
}

func cfg(width string) settings.Settings {
	footer := "Goods sold are not returnable"
	return settings.Settings{
		ShopName:       "Karots Store",
		CurrencySymbol: "Rs.",
		ReceiptWidth:   width,
		ReceiptFooter:  &footer,
	}
}

func TestDocumentIsASCIIAndCut(t *testing.T) {
	out := Document(sampleDetail(), cfg("80"), Options{})

	// init + cut markers present
	if out[0] != esc || out[1] != '@' {
		t.Fatalf("expected ESC @ init, got %x %x", out[0], out[1])
	}
	if !strings.HasSuffix(string(out), string([]byte{gs, 'V', 1})) {
		t.Fatalf("expected partial-cut at end")
	}

	// no stray high bytes that would render as CJK garbage (control bytes used
	// are all < 0x20 or are the explicit command bytes we emit)
	for i, b := range out {
		if b >= 0x80 {
			t.Fatalf("non-ASCII byte 0x%x at offset %d", b, i)
		}
	}

	text := string(out)
	for _, want := range []string{"Karots Store", "R-0001", "Onion", "2.25 kg x 400.00", "TOTAL", "Rs. 1,400.00", "CHANGE", "Thank you! Come again."} {
		if !strings.Contains(text, want) {
			t.Errorf("receipt missing %q", want)
		}
	}
	// The standalone "Paid" row was removed in favour of the per-tender lines.
	if strings.Contains(text, "Paid") {
		t.Errorf("receipt should no longer contain a 'Paid' row")
	}
}

func TestReturnDocumentIsASCIIAndCut(t *testing.T) {
	d := decimal.RequireFromString
	reason := "damaged item"
	rr := sales.ReturnReceipt{
		ReceiptNo:       "R-0001",
		CreatedAt:       time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC),
		Reason:          &reason,
		Refund:          d("400.00"),
		CreditReduction: d("100.00"),
		Items: []sales.ReturnReceiptItem{
			{ProductName: "Onion", UnitAbbr: "kg", Quantity: d("1.00"), Refund: d("400.00")},
		},
	}
	out := ReturnDocument(rr, cfg("80"), Options{})

	if out[0] != esc || out[1] != '@' {
		t.Fatalf("expected ESC @ init")
	}
	if !strings.HasSuffix(string(out), string([]byte{gs, 'V', 1})) {
		t.Fatalf("expected partial-cut at end")
	}
	for i, b := range out {
		if b >= 0x80 {
			t.Fatalf("non-ASCII byte 0x%x at offset %d", b, i)
		}
	}
	text := string(out)
	for _, want := range []string{"Karots Store", "*** REFUND ***", "R-0001", "Onion", "CASH REFUND", "Rs. 400.00", "Credit reduced", "Refund slip"} {
		if !strings.Contains(text, want) {
			t.Errorf("refund slip missing %q", want)
		}
	}
}

func TestReturnDocumentShowsRemainingBalance(t *testing.T) {
	d := decimal.RequireFromString
	name := "Nimal Perera"
	bal := d("1920.00")
	rr := sales.ReturnReceipt{
		ReceiptNo:        "S-0008",
		CreatedAt:        time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC),
		Refund:           d("0.00"),
		CreditReduction:  d("400.00"),
		CustomerName:     &name,
		RemainingBalance: &bal,
		Items: []sales.ReturnReceiptItem{
			{ProductName: "Onion", UnitAbbr: "kg", Quantity: d("1.00"), Refund: d("0.00")},
		},
	}
	s := string(ReturnDocument(rr, cfg("80"), Options{}))
	for _, want := range []string{"Customer:", "Nimal Perera", "Balance due", "1,920.00"} {
		if !strings.Contains(s, want) {
			t.Errorf("refund slip missing %q", want)
		}
	}
}

func TestReturnDocumentWalkInOmitsBalance(t *testing.T) {
	d := decimal.RequireFromString
	rr := sales.ReturnReceipt{
		ReceiptNo:       "S-0009",
		CreatedAt:       time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC),
		Refund:          d("400.00"),
		CreditReduction: d("0.00"),
		Items: []sales.ReturnReceiptItem{
			{ProductName: "Onion", UnitAbbr: "kg", Quantity: d("1.00"), Refund: d("400.00")},
		},
	}
	s := string(ReturnDocument(rr, cfg("80"), Options{}))
	if strings.Contains(s, "Customer:") || strings.Contains(s, "Balance due") {
		t.Errorf("walk-in refund slip should omit customer/balance: %q", s)
	}
}

func TestWarrantyDocumentShowsMonthsLeft(t *testing.T) {
	out := WarrantyDocument(WarrantySlip{
		ProductName:   "Power Bank",
		OldSerial:     "OLD-1",
		NewSerial:     "NEW-2",
		WarrantyUntil: "2027-06-13",
		WarrantyLeft:  "11 mo left",
		CustomerName:  "Nimal Perera",
	}, cfg("80"), Options{})
	s := string(out)
	for _, want := range []string{"WARRANTY REPLACEMENT", "2027-06-13", "(11 mo left)", "NEW-2"} {
		if !strings.Contains(s, want) {
			t.Errorf("warranty slip missing %q", want)
		}
	}
}

func TestWarrantyDocumentOmitsLeftWhenEmpty(t *testing.T) {
	out := WarrantyDocument(WarrantySlip{
		ProductName:   "Power Bank",
		NewSerial:     "NEW-2",
		WarrantyUntil: "2027-06-13",
	}, cfg("80"), Options{})
	if strings.Contains(string(out), "(") {
		t.Errorf("warranty slip should omit the months-left parenthetical when empty")
	}
}

func TestColumnsByWidth(t *testing.T) {
	if got := columns("58"); got != 32 {
		t.Errorf("58mm => %d, want 32", got)
	}
	if got := columns("80"); got != 48 {
		t.Errorf("80mm => %d, want 48", got)
	}
}

func TestLeftRightFitsWidth(t *testing.T) {
	line := leftRight("Subtotal", "Rs. 1,500.00", 48)
	if len([]rune(line)) != 48 {
		t.Errorf("line width = %d, want 48: %q", len([]rune(line)), line)
	}
}

func TestASCIIReplacesNonLatin(t *testing.T) {
	// Sinhala text must not leak raw multibyte bytes into the stream.
	got := ascii("කරොට්ස් Store")
	if strings.ContainsRune(got, 'ක') || strings.ContainsAny(got, "ÿ") {
		t.Errorf("ascii() leaked non-Latin runes: %q", got)
	}
	if !strings.Contains(got, "Store") {
		t.Errorf("ascii() dropped Latin text: %q", got)
	}
}

func TestDebtDocument(t *testing.T) {
	d := decimal.RequireFromString
	before, after := d("5000.00"), d("3000.00")
	out := DebtDocument(DebtSlip{
		ReceiptNo: "DP-000123", Date: "2026-06-28 14:05",
		CustomerName: "Nimal Perera", CustomerPhone: "0771239876",
		Method: "Cash", CashierName: "Kamal", Amount: d("2000.00"),
		BalanceBefore: &before, BalanceAfter: &after,
	}, cfg("80"), Options{})
	s := string(out)
	for _, want := range []string{"CREDIT PAYMENT", "DP-000123", "Nimal Perera", "0771239876", "2,000.00", "3,000.00"} {
		if !strings.Contains(s, want) {
			t.Errorf("debt slip missing %q", want)
		}
	}
	// The customer's credit limit must never appear on the slip they're handed.
	if strings.Contains(s, "Credit limit") {
		t.Error("debt slip must not print the credit limit")
	}
	if out[0] != esc || out[1] != '@' {
		t.Fatalf("expected ESC @ init")
	}
	if !strings.HasSuffix(s, string([]byte{gs, 'V', 1})) {
		t.Fatalf("expected partial-cut at end")
	}
}

func TestDebtDocumentOmitsNullBalances(t *testing.T) {
	d := decimal.RequireFromString
	out := DebtDocument(DebtSlip{
		ReceiptNo: "DP-000099", Date: "2026-06-28 10:00",
		CustomerName: "Old Row", Method: "Cash", Amount: d("500.00"),
	}, cfg("80"), Options{})
	if strings.Contains(string(out), "Remaining balance") {
		t.Errorf("expected no balance block when balances are nil")
	}
}
