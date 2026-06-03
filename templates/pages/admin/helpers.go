package adminpages

import (
	"strconv"
	"time"

	"karots-pos/internal/features/suppliers"

	"github.com/shopspring/decimal"
)

// daysSince renders the whole-days elapsed since t (em-dash when t is nil), used
// for the customer-dues aging column.
func daysSince(t *time.Time) string {
	if t == nil {
		return "—"
	}
	d := max(int(time.Since(*t).Hours()/24), 0)
	return strconv.Itoa(d)
}

func decimalFromInt(n int) decimal.Decimal { return decimal.NewFromInt(int64(n)) }

func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// supVal prefills the supplier form for edits (empty string when creating).
func supVal(s *suppliers.Supplier, field string) string {
	if s == nil {
		if field == "credit_days" {
			return "30"
		}
		return ""
	}
	switch field {
	case "name":
		return s.Name
	case "contact":
		return strOrEmpty(s.ContactPerson)
	case "phone":
		return strOrEmpty(s.Phone)
	case "address":
		return strOrEmpty(s.Address)
	case "credit_days":
		return strconv.Itoa(s.CreditDays)
	default:
		return ""
	}
}
