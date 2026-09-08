package repairs

import "github.com/shopspring/decimal"

var hundred = decimal.NewFromInt(100)

// normDiscType defaults a blank/unknown per-line discount type to "fixed".
func normDiscType(t string) string {
	if t == "percent" {
		return "percent"
	}
	return "fixed"
}

// resolvePartDiscount turns a per-part discount into an amount off the line.
// Fixed is per unit (× qty); percent is off the gross. Clamped to [0, gross].
func resolvePartDiscount(dtype string, value, gross, qty decimal.Decimal) decimal.Decimal {
	var amt decimal.Decimal
	if dtype == "percent" {
		amt = gross.Mul(value).Div(hundred)
	} else {
		amt = value.Mul(qty)
	}
	amt = amt.Round(2)
	if amt.IsNegative() {
		return decimal.Zero
	}
	if amt.GreaterThan(gross) {
		return gross
	}
	return amt
}
