package adminpages

import (
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"time"

	"karots-pos/internal/features/products"
	"karots-pos/internal/features/suppliers"
	"karots-pos/templates/layouts"

	"github.com/shopspring/decimal"
)

// reportHubCard is one card on the Reports hub (built-in or plugin-contributed).
type reportHubCard struct {
	Href, Title, Desc string
}

// reportHubCards returns every Reports-hub card — the built-in reports plus any
// plugin-contributed ones — sorted alphabetically by title so the grid is easy
// to scan.
func reportHubCards() []reportHubCard {
	cards := []reportHubCard{
		{"/admin/reports/sales", "Sales", "Receipts and totals over a date range"},
		{"/admin/reports/finance", "Finance / P&L", "Revenue, COGS, profit, dues for a period"},
		{"/admin/reports/tax", "Tax Summary", "VAT/tax collected over a period"},
		{"/admin/reports/returns", "Returns / Refunds", "Returned lines and refund value"},
		{"/admin/reports/profit-by-category", "Profit by Category", "Net revenue & profit per category"},
		{"/admin/reports/sales-trend", "Daily Sales Trend", "Day-by-day net revenue & profit"},
		{"/admin/reports/product-sales", "Product Sales", "One product's units over time vs last year"},
		{"/admin/reports/warranty", "Warranty & Recovery", "Replacement cost vs supplier recovery"},
		{"/admin/reports/cash-register", "Cash Register", "Drawer sessions with over/short"},
		{"/admin/reports/purchases", "Purchases", "GRNs received in a period"},
		{"/admin/reports/suppliers", "Suppliers", "Outstanding payables snapshot"},
		{"/admin/reports/customer-dues", "Customer Dues", "Receivables — who owes you money"},
		{"/admin/reports/supplier-dues", "Supplier Dues", "Payables — who you owe money"},
		{"/admin/reports/inventory", "Inventory Valuation", "Stock on hand at cost & retail"},
		{"/admin/reports/batches", "Batches / Expiry", "Live batches and expiry dates"},
		{"/admin/reports/recipe-variance", "Recipe Variance", "Expected vs actual ingredient use"},
		{"/admin/reports/service-profit", "Service Profit", "Income, ingredients & costs per service"},
		{"/admin/reports/low-stock", "Low Stock", "Items at or below reorder level"},
		{"/admin/reports/expiring", "Expiring Stock", "Batches expiring soon"},
		{"/admin/damage", "Damage Report", "Damaged/written-off stock & recovery"},
	}
	for _, rc := range layouts.PluginReportCards() {
		cards = append(cards, reportHubCard{rc.Href, rc.Label, rc.Desc})
	}
	sort.SliceStable(cards, func(i, j int) bool { return cards[i].Title < cards[j].Title })
	return cards
}

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
	Suggested     string
	SoldLastWeek  string
	SoldLastMonth string
	SoldLastYear  string
}

// lowStockConfigJSON serialises the low-stock rows for the reorder PO builder's
// Alpine state. The suggested order qty is demand-based when sales history exists
// (ReorderInfo.Suggested), otherwise falls back to ≈ 2× reorder level − on-hand.
func lowStockConfigJSON(rows []products.Product, demand map[int64]ReorderInfo) string {
	type line struct {
		ProductID     int64  `json:"product_id"`
		Name          string `json:"name"`
		OnHand        string `json:"on_hand"`
		Unit          string `json:"unit"`
		Suggested     string `json:"suggested"`
		SoldLastWeek  string `json:"sold_last_week"`
		SoldLastMonth string `json:"sold_last_month"`
		SoldLastYear  string `json:"sold_last_year"`
		Cost          string `json:"cost"`
		SupplierID    int64  `json:"supplier_id"`
		SupplierName  string `json:"supplier_name"`
		Selected      bool   `json:"selected"`
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
		soldWk := info.SoldLastWeek
		if soldWk == "" {
			soldWk = "0"
		}
		soldMo := info.SoldLastMonth
		if soldMo == "" {
			soldMo = "0"
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
			Suggested: suggested, SoldLastWeek: soldWk, SoldLastMonth: soldMo, SoldLastYear: soldLY,
			Cost: p.CostPrice.String(), SupplierID: sup, SupplierName: supName,
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

// --- inventory valuation helpers ---

func pgFirst(page, size, total int) int {
	if total == 0 {
		return 0
	}
	return (page-1)*size + 1
}

func pgLast(page, size, total int) int { return min(page*size, total) }

// pgOutOfRange reports whether the requested page starts beyond the last row.
func pgOutOfRange(page, size, total int) bool { return (page-1)*size >= total }

// pgLastPage is the highest page number that still holds rows.
func pgLastPage(size, total int) int {
	if total <= 0 || size <= 0 {
		return 1
	}
	return (total + size - 1) / size
}

// pgHref appends ?page=N to a report's existing filter query string. baseQuery
// carries no leading "?" and no page key of its own.
func pgHref(baseQuery string, page int) string {
	if baseQuery == "" {
		return "?page=" + strconv.Itoa(page)
	}
	return "?" + baseQuery + "&page=" + strconv.Itoa(page)
}

// invQuery is the Inventory report's filter state as a query string, so the
// pager and the CSV link both keep the active filters.
func invQuery(d InventoryReportData) string {
	q := url.Values{}
	if d.CategoryID != nil {
		q.Set("category_id", strconv.FormatInt(*d.CategoryID, 10))
	}
	if d.IncludeZero {
		q.Set("include_zero", "1")
	}
	return q.Encode()
}

// rangeQuery is the filter state of a plain date-range report, so its pager
// keeps the period the user is looking at.
func rangeQuery(preset, from, to string) string {
	q := url.Values{}
	setNonEmpty(q, "preset", preset)
	setNonEmpty(q, "from", from)
	setNonEmpty(q, "to", to)
	return q.Encode()
}

// salesQuery / batchQuery mirror invQuery for their reports.
func salesQuery(d SalesReportData) string {
	q := url.Values{}
	setNonEmpty(q, "preset", d.Preset)
	setNonEmpty(q, "from", d.From)
	setNonEmpty(q, "to", d.To)
	setNonEmpty(q, "status", d.Status)
	setNonEmpty(q, "method", d.Method)
	return q.Encode()
}

// movQuery is the Stock Movements filter state, so paging and the CSV link both
// stay inside the product/type/date window the user is looking at.
func movQuery(d StockMovementsData) string {
	q := url.Values{}
	setNonEmpty(q, "product_id", d.ProductID)
	setNonEmpty(q, "type", d.MoveType)
	if d.Preset != "" {
		q.Set("preset", d.Preset)
	} else {
		setNonEmpty(q, "from", d.From)
		setNonEmpty(q, "to", d.To)
	}
	return q.Encode()
}

// presetHref builds a quick-pick range link that KEEPS the page's other filters
// (product, type). The report pages' own preset buttons drop theirs, which is
// why this one takes the current query instead of rebuilding from scratch.
// An empty key clears the date window ("All time").
func presetHref(baseQuery, key string) string {
	q, err := url.ParseQuery(baseQuery)
	if err != nil {
		q = url.Values{}
	}
	q.Del("from")
	q.Del("to")
	q.Del("page") // a new range invalidates the page number
	if key == "" {
		q.Del("preset")
	} else {
		q.Set("preset", key)
	}
	if len(q) == 0 {
		return "?"
	}
	return "?" + q.Encode()
}

func batchQuery(d BatchReportData) string {
	q := url.Values{}
	setNonEmpty(q, "days", d.Days)
	return q.Encode()
}

func setNonEmpty(q url.Values, key, val string) {
	if val != "" {
		q.Set(key, val)
	}
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

// runsQuery is the Conversion Run History filter state, so paging, the preset
// bar and the CSV link all stay inside the same window.
func runsQuery(d ConversionRunsData) string {
	q := url.Values{}
	setNonEmpty(q, "conversion_id", d.ConversionID)
	if d.Preset != "" {
		q.Set("preset", d.Preset)
	} else {
		setNonEmpty(q, "from", d.From)
		setNonEmpty(q, "to", d.To)
	}
	return q.Encode()
}
