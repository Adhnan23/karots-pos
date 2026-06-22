package adminpages

import (
	"encoding/json"
	"strconv"
	"time"

	"karots-pos/internal/features/products"
	"karots-pos/internal/features/suppliers"

	"github.com/shopspring/decimal"
)

// lowStockConfigJSON serialises the low-stock rows for the reorder PO builder's
// Alpine state: a suggested order qty (target ≈ 2× reorder level minus on-hand),
// the product's default cost, and its preferred supplier (0 when none).
func lowStockConfigJSON(rows []products.Product) string {
	type line struct {
		ProductID  int64  `json:"product_id"`
		Name       string `json:"name"`
		OnHand     string `json:"on_hand"`
		Unit       string `json:"unit"`
		Suggested  string `json:"suggested"`
		Cost       string `json:"cost"`
		SupplierID int64  `json:"supplier_id"`
		Selected   bool   `json:"selected"`
	}
	out := make([]line, 0, len(rows))
	for _, p := range rows {
		need := decimal.NewFromInt(int64(p.ReorderLevel * 2)).Sub(p.StockQty).Ceil()
		if need.IsNegative() {
			need = decimal.Zero
		}
		var sup int64
		if p.PreferredSupplierID != nil {
			sup = *p.PreferredSupplierID
		}
		out = append(out, line{
			ProductID: p.ID, Name: p.Name, OnHand: p.StockQty.String(), Unit: p.UnitAbbr,
			Suggested: need.String(), Cost: p.CostPrice.String(), SupplierID: sup,
		})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

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
