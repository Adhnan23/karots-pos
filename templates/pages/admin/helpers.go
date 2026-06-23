package adminpages

import (
	"encoding/json"
	"strconv"
	"time"

	"karots-pos/internal/features/products"
	"karots-pos/internal/features/suppliers"

	"github.com/shopspring/decimal"
)

// jsArg JSON-encodes a string for safe embedding as a JS literal in an x-data
// attribute (handles quotes/specials in e.g. product names).
func jsArg(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ReorderInfo is the demand-derived hint for one low-stock product: a suggested
// order qty (from trailing-average demand × lead time) and units sold in the same
// period last year. Empty Suggested means "no sales history — use the fallback".
type ReorderInfo struct {
	Suggested    string
	SoldLastYear string
}

// lowStockConfigJSON serialises the low-stock rows for the reorder PO builder's
// Alpine state. The suggested order qty is demand-based when sales history exists
// (ReorderInfo.Suggested), otherwise falls back to ≈ 2× reorder level − on-hand.
func lowStockConfigJSON(rows []products.Product, demand map[int64]ReorderInfo) string {
	type line struct {
		ProductID    int64  `json:"product_id"`
		Name         string `json:"name"`
		OnHand       string `json:"on_hand"`
		Unit         string `json:"unit"`
		Suggested    string `json:"suggested"`
		SoldLastYear string `json:"sold_last_year"`
		Cost         string `json:"cost"`
		SupplierID   int64  `json:"supplier_id"`
		SupplierName string `json:"supplier_name"`
		Selected     bool   `json:"selected"`
	}
	out := make([]line, 0, len(rows))
	for _, p := range rows {
		info := demand[p.ID]
		suggested := info.Suggested
		if suggested == "" {
			need := decimal.NewFromInt(int64(p.ReorderLevel * 2)).Sub(p.StockQty).Ceil()
			if need.IsNegative() {
				need = decimal.Zero
			}
			suggested = need.String()
		}
		soldLY := info.SoldLastYear
		if soldLY == "" {
			soldLY = "0"
		}
		var sup int64
		supName := ""
		if p.PreferredSupplierID != nil {
			sup = *p.PreferredSupplierID
		}
		if p.PreferredSupplierName != nil {
			supName = *p.PreferredSupplierName
		}
		out = append(out, line{
			ProductID: p.ID, Name: p.Name, OnHand: p.StockQty.String(), Unit: p.UnitAbbr,
			Suggested: suggested, SoldLastYear: soldLY, Cost: p.CostPrice.String(), SupplierID: sup, SupplierName: supName,
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
