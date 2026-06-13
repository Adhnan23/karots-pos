package reports

import (
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestGrossMargin(t *testing.T) {
	tests := []struct {
		name           string
		profit, revenue string
		want           string
	}{
		{"half margin", "50", "100", "50"},
		{"zero revenue guards", "10", "0", "0"},
		{"negative revenue guards", "10", "-5", "0"},
		{"loss margin", "-20", "100", "-20"},
		{"rounds to 2dp", "1", "3", "33.33"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := grossMargin(dec(tc.profit), dec(tc.revenue))
			if !got.Equal(dec(tc.want)) {
				t.Errorf("grossMargin(%s,%s) = %s, want %s", tc.profit, tc.revenue, got, tc.want)
			}
		})
	}
}
