package clearance

import (
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { d, _ := decimal.NewFromString(s); return d }

func TestSuggestPercentFlooredAtCost(t *testing.T) {
	cases := []struct {
		name                               string
		sell, cost, deflt, minMargin, want string
	}{
		// 20% off 120 = 96, but floor = cost*1.05 = 105 → clamp to 12.5%.
		{"clamped to floor", "120", "100", "20", "5", "12.5"},
		// 20% off 200 = 160 >= floor 105 → full 20%.
		{"unclamped", "200", "100", "20", "5", "20"},
		// price already at/under floor → nothing safe to give.
		{"already cheap", "100", "100", "20", "5", "0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := suggestPercent(dec(c.sell), dec(c.cost), dec(c.deflt), dec(c.minMargin))
			if !got.Equal(dec(c.want)) {
				t.Errorf("suggestPercent = %s, want %s", got, c.want)
			}
		})
	}
}

func TestNewPrice(t *testing.T) {
	if got := newPrice(dec("120"), dec("12.5")); !got.Equal(dec("105")) {
		t.Errorf("newPrice = %s, want 105", got)
	}
}
