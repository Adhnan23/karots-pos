package sales

import (
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestNormDiscountType(t *testing.T) {
	cases := map[string]string{"": "fixed", "fixed": "fixed", "percent": "percent", "bogus": "fixed"}
	for in, want := range cases {
		if got := normDiscountType(in); got != want {
			t.Errorf("normDiscountType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveItemDiscount(t *testing.T) {
	tests := []struct {
		name      string
		dtype     string
		value     string
		lineGross string
		qty       string
		want      string
	}{
		{"fixed per unit times qty", "fixed", "5", "300", "3", "15"},
		{"fixed single unit", "fixed", "5", "100", "1", "5"},
		{"percent off line", "percent", "10", "300", "3", "30"},
		{"percent rounds to 2dp", "percent", "12.5", "99.99", "1", "12.50"},
		{"fixed clamped to line gross", "fixed", "500", "300", "3", "300"},
		{"percent over 100 clamps", "percent", "150", "300", "3", "300"},
		{"negative clamps to zero", "fixed", "-5", "300", "3", "0"},
		{"zero default", "fixed", "0", "300", "3", "0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveItemDiscount(tc.dtype, dec(tc.value), dec(tc.lineGross), dec(tc.qty))
			if !got.Equal(dec(tc.want)) {
				t.Errorf("resolveItemDiscount(%s, %s, gross=%s, qty=%s) = %s, want %s",
					tc.dtype, tc.value, tc.lineGross, tc.qty, got, tc.want)
			}
		})
	}
}

func TestResolveBillDiscount(t *testing.T) {
	tests := []struct {
		name  string
		dtype string
		value string
		base  string
		want  string
	}{
		{"fixed amount", "fixed", "50", "1000", "50"},
		{"percent of base", "percent", "10", "1000", "100"},
		{"percent rounds", "percent", "7.5", "333.33", "25.00"},
		{"fixed clamped to base", "fixed", "2000", "1000", "1000"},
		{"percent over 100 clamps", "percent", "120", "1000", "1000"},
		{"negative clamps to zero", "fixed", "-10", "1000", "0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveBillDiscount(tc.dtype, dec(tc.value), dec(tc.base))
			if !got.Equal(dec(tc.want)) {
				t.Errorf("resolveBillDiscount(%s, %s, base=%s) = %s, want %s",
					tc.dtype, tc.value, tc.base, got, tc.want)
			}
		})
	}
}
