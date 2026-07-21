package sales

import (
	"testing"

	"github.com/shopspring/decimal"
)

func td(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestSplitTenderSeparatesAccountFromMoney(t *testing.T) {
	got := SplitTender(
		[]string{"cash", "credit", "card"},
		[]decimal.Decimal{td("500"), td("700"), td("100")},
	)
	if !got.Paid.Equal(td("600")) {
		t.Errorf("Paid = %s, want 600", got.Paid)
	}
	if !got.OnAccount.Equal(td("700")) {
		t.Errorf("OnAccount = %s, want 700", got.OnAccount)
	}
}

func TestCheckTenderAcceptsExactCash(t *testing.T) {
	tn := Tender{Paid: td("1200"), OnAccount: decimal.Zero}
	if err := CheckTender(tn, td("1200"), false, decimal.Zero); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckTenderAcceptsOverpaymentInCash(t *testing.T) {
	tn := Tender{Paid: td("2000"), OnAccount: decimal.Zero}
	if err := CheckTender(tn, td("1200"), false, decimal.Zero); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// The bug this whole change exists to kill: money missing and nobody named.
func TestCheckTenderRejectsAShortfallWithNoAccountLine(t *testing.T) {
	tn := Tender{Paid: td("500"), OnAccount: decimal.Zero}
	if err := CheckTender(tn, td("1200"), true, td("9999")); err == nil {
		t.Error("a short-paid sale was accepted")
	}
}

func TestCheckTenderAcceptsPartCashPartAccount(t *testing.T) {
	tn := Tender{Paid: td("500"), OnAccount: td("700")}
	if err := CheckTender(tn, td("1200"), true, td("5000")); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckTenderRequiresACustomerForAnAccountLine(t *testing.T) {
	tn := Tender{Paid: td("500"), OnAccount: td("700")}
	if err := CheckTender(tn, td("1200"), false, decimal.Zero); err == nil {
		t.Error("an account line with no customer was accepted")
	}
}

func TestCheckTenderEnforcesTheCreditLimit(t *testing.T) {
	tn := Tender{Paid: decimal.Zero, OnAccount: td("700")}
	if err := CheckTender(tn, td("700"), true, td("300")); err == nil {
		t.Error("borrowing past the credit limit was accepted")
	}
}

// You cannot hand back cash against money that was never paid.
func TestCheckTenderRejectsChangeOnACreditSale(t *testing.T) {
	tn := Tender{Paid: td("1000"), OnAccount: td("700")}
	if err := CheckTender(tn, td("1200"), true, td("5000")); err == nil {
		t.Error("an over-covered credit sale was accepted")
	}
}
