package cashierpages

import (
	"strconv"

	"github.com/shopspring/decimal"
)

func itoa(n int) string     { return strconv.Itoa(n) }
func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// jsBool renders a Go bool as a JS boolean literal for inline x-data init.
func jsBool(b bool) string { return strconv.FormatBool(b) }

// zMoveLabel turns a cash_movement_type into a readable label for the Z-report.
func zMoveLabel(t string) string {
	switch t {
	case "opening":
		return "Opening cash"
	case "sale":
		return "Sale"
	case "credit_payment":
		return "Credit collected"
	case "withdrawal":
		return "Withdrawal"
	case "pay_in":
		return "Pay-in"
	case "refund":
		return "Refund"
	case "closing":
		return "Closing count"
	default:
		return t
	}
}

// overShortClass colours the over/short figure: red when short, green otherwise.
func overShortClass(v *decimal.Decimal) string {
	if v != nil && v.IsNegative() {
		return "text-rose-600"
	}
	return "text-emerald-600"
}
