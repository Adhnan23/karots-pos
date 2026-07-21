package purchases

import (
	"reflect"
	"testing"
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
