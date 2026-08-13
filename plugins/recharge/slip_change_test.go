package recharge

import (
	"strings"
	"testing"

	"karots-pos/internal/escpos"
	"karots-pos/internal/features/settings"

	"github.com/shopspring/decimal"
)

// TestBuildSlipChange verifies the sale-style tender/change lines: change when the
// customer overpays, balance when they underpay, and nothing when no cash-given
// was recorded (older rows / cash-out slips).
func TestBuildSlipChange(t *testing.T) {
	cfg := &settings.Settings{ShopName: "Shop", CurrencySymbol: "Rs", ReceiptWidth: "58"}
	d := func(s string) decimal.Decimal { return decimal.RequireFromString(s) }

	cases := []struct {
		name         string
		amount, svc  decimal.Decimal
		cash         decimal.Decimal
		wantContains []string
		wantOmits    []string
	}{
		{"overpay shows change", d("850"), d("20"), d("1000"),
			[]string{"Paid", "Rs 1,000.00", "Change", "Rs 130.00"}, []string{"Balance"}},
		{"underpay shows balance", d("850"), d("20"), d("500"),
			[]string{"Paid", "Rs 500.00", "Balance", "Rs 370.00"}, []string{"Change"}},
		{"no tender omits both", d("850"), d("20"), decimal.Zero,
			nil, []string{"Paid", "Change", "Balance"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := string(buildSlip(cfg, escpos.Options{}, slipData{
				Kind: "billpay", Carrier: "My Bank",
				Amount: tc.amount, ServiceCharge: tc.svc, CashGiven: tc.cash,
			}))
			for _, w := range tc.wantContains {
				if !strings.Contains(out, w) {
					t.Errorf("slip missing %q\n%s", w, out)
				}
			}
			for _, w := range tc.wantOmits {
				if strings.Contains(out, w) {
					t.Errorf("slip should not contain %q\n%s", w, out)
				}
			}
		})
	}
}
