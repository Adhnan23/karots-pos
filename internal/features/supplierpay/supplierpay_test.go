package supplierpay

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestNormMethod(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"":       {"cash", true},
		"cash":   {"cash", true},
		"card":   {"card", true},
		"online": {"online", true},
		"credit": {"", false},
		"bogus":  {"", false},
	}
	for in, exp := range cases {
		got, ok := normMethod(in)
		if got != exp.want || ok != exp.ok {
			t.Errorf("normMethod(%q) = (%q,%v), want (%q,%v)", in, got, ok, exp.want, exp.ok)
		}
	}
}

// Pay validates method and total before opening a transaction, so these cases
// never touch the (nil) DB.
func TestPayValidation(t *testing.T) {
	s := &Service{}
	ctx := context.Background()

	if _, err := s.Pay(ctx, 1, PayInput{Method: "bogus", Allocations: []Alloc{{PurchaseID: 1, Amount: dec("10")}}}, 1); err == nil {
		t.Error("expected error for invalid method")
	}
	if _, err := s.Pay(ctx, 1, PayInput{Method: "cash"}, 1); err == nil {
		t.Error("expected error for zero total")
	}
	if _, err := s.Pay(ctx, 1, PayInput{Method: "cash", Allocations: []Alloc{{PurchaseID: 1, Amount: dec("-5")}}}, 1); err == nil {
		t.Error("expected error for negative allocation")
	}
}

func TestStrOrNil(t *testing.T) {
	if strOrNil("") != nil {
		t.Error("empty string should map to nil")
	}
	if v := strOrNil("x"); v == nil || *v != "x" {
		t.Error("non-empty string should map to pointer")
	}
}
