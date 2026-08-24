package recharge

import (
	"testing"

	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/lockers"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestBuildBankLegs(t *testing.T) {
	account := cashflow.Locker(7) // e.g. a bank locker
	cash := cashflow.Till(3)      // e.g. a till
	ext := cashflow.External()

	type want struct {
		from, to cashflow.Location
		amount   string
	}
	cases := []struct {
		name string
		typ  string
		svc  string
		legs []want
	}{
		{
			name: "billpay no service charge",
			typ:  "billpay", svc: "0",
			legs: []want{
				{account, ext, "100"}, // bank pays biller (down, guarded)
				{ext, cash, "100"},    // customer cash in
			},
		},
		{
			name: "billpay with service charge",
			typ:  "billpay", svc: "20",
			legs: []want{
				{account, ext, "100"},
				{ext, cash, "120"}, // principal + service charge, all cash in
			},
		},
		{
			name: "getmoney no service charge",
			typ:  "getmoney", svc: "0",
			legs: []want{
				{cash, ext, "100"},    // cash out (guarded)
				{ext, account, "100"}, // e-money into the account
			},
		},
		{
			name: "getmoney with service charge",
			typ:  "getmoney", svc: "20",
			legs: []want{
				{cash, ext, "100"},
				{ext, account, "100"},
				{ext, cash, "20"}, // service charge extra cash in
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildBankLegs(tc.typ, account, cash, dec("100"), dec(tc.svc), "Bill 42")
			if len(got) != len(tc.legs) {
				t.Fatalf("got %d legs, want %d", len(got), len(tc.legs))
			}
			for i, w := range tc.legs {
				g := got[i]
				if g.From != w.from || g.To != w.to || !g.Amount.Equal(dec(w.amount)) {
					t.Fatalf("leg %d = {%v->%v %s}, want {%v->%v %s}",
						i, g.From, g.To, g.Amount, w.from, w.to, w.amount)
				}
			}
		})
	}
}

func TestBankUsableByCashier(t *testing.T) {
	mk := func(active bool, kind string, access bool) *lockers.Locker {
		return &lockers.Locker{IsActive: active, Kind: kind, CashierAccess: access}
	}
	cases := []struct {
		name string
		l    *lockers.Locker
		want bool
	}{
		{"accessible active bank", mk(true, lockers.KindBank, true), true},
		{"bank without cashier access", mk(true, lockers.KindBank, false), false},
		{"inactive bank", mk(false, lockers.KindBank, true), false},
		{"safe (not a bank)", mk(true, lockers.KindSafe, true), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bankUsableByCashier(tc.l); got != tc.want {
				t.Fatalf("bankUsableByCashier = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBankBillLegs(t *testing.T) {
	acct := cashflow.Locker(7)
	cash := cashflow.Till(3)
	ext := cashflow.External()

	// Cash flows delegate to buildBankLegs (covered above); assert the credit
	// variants: billpay keeps only the account→biller leg, get-money keeps none.
	t.Run("billpay on credit: account pays biller only, no cash leg", func(t *testing.T) {
		got := bankBillLegs("billpay", true, acct, cash, dec("100"), dec("20"), "Bill 42")
		if len(got) != 1 {
			t.Fatalf("got %d legs, want 1", len(got))
		}
		g := got[0]
		if g.From != acct || g.To != ext || !g.Amount.Equal(dec("100")) {
			t.Fatalf("leg = {%v->%v %s}, want {account->external 100}", g.From, g.To, g.Amount)
		}
	})

	t.Run("getmoney on credit: no cash-flow legs (pure cash advance)", func(t *testing.T) {
		if got := bankBillLegs("getmoney", true, acct, cash, dec("100"), dec("0"), "x"); len(got) != 0 {
			t.Fatalf("got %d legs, want 0", len(got))
		}
	})

	t.Run("cash flow delegates to buildBankLegs", func(t *testing.T) {
		if got := bankBillLegs("billpay", false, acct, cash, dec("100"), dec("0"), "x"); len(got) != 2 {
			t.Fatalf("got %d legs, want 2 (account + customer cash)", len(got))
		}
	})
}

func TestCreditID(t *testing.T) {
	if creditID(true, 9) != 9 {
		t.Fatal("on-credit should carry the customer id")
	}
	if creditID(false, 9) != 0 {
		t.Fatal("a cash bill must not stamp the selected customer as owing it")
	}
}
