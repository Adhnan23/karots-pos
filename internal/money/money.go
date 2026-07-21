// Package money provides currency helpers around shopspring/decimal. All money
// in this system is a decimal.Decimal — never float64 — so arithmetic is exact.
// Decimal implements sql.Scanner/driver.Valuer, so it maps directly to Postgres
// DECIMAL columns with no string round-tripping.
package money

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// Zero is the additive identity, handy as a default.
var Zero = decimal.Zero

// Parse converts a user-supplied string (form field) into a Decimal, treating
// empty input as zero.
func Parse(s string) (decimal.Decimal, error) {
	if s == "" {
		return decimal.Zero, nil
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, fmt.Errorf("invalid amount %q", s)
	}
	return d, nil
}

// FromFloat is a convenience for tests/seeds; avoid in request paths.
func FromFloat(f float64) decimal.Decimal { return decimal.NewFromFloat(f) }

// Format renders an amount with a currency symbol and two decimals, e.g.
// "Rs. 1,250.00".
func Format(symbol string, d decimal.Decimal) string {
	return fmt.Sprintf("%s %s", symbol, withThousands(d.StringFixed(2)))
}

// Display renders an amount with two decimals and thousands separators but no
// symbol, e.g. "1,250.00".
func Display(d decimal.Decimal) string {
	return withThousands(d.StringFixed(2))
}

func withThousands(s string) string {
	neg := false
	if len(s) > 0 && s[0] == '-' {
		neg = true
		s = s[1:]
	}
	intPart, frac := s, ""
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			intPart, frac = s[:i], s[i:]
			break
		}
	}
	n := len(intPart)
	if n <= 3 {
		return sign(neg) + intPart + frac
	}
	var b []byte
	for i, c := range []byte(intPart) {
		if i > 0 && (n-i)%3 == 0 {
			b = append(b, ',')
		}
		b = append(b, c)
	}
	return sign(neg) + string(b) + frac
}

func sign(neg bool) string {
	if neg {
		return "-"
	}
	return ""
}

// Qty renders a stock quantity for display: thousands separated, up to three
// decimal places, with trailing zeros dropped. A count of 1755 reads "1,755"
// rather than "1,755.00", while a genuinely part-used 3.6 stays "3.6".
func Qty(d decimal.Decimal) string {
	s := d.Round(3).String()
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	if s == "" || s == "-" {
		return "0"
	}
	return withThousands(s)
}
