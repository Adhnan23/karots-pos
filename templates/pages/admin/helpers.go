package adminpages

import (
	"encoding/json"
	"net/url"
	"strconv"
	"time"

	"karots-pos/internal/datetime"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/reports"
	"karots-pos/internal/features/suppliers"
	"karots-pos/internal/money"
	"karots-pos/templates/layouts"

	"github.com/shopspring/decimal"
)

// datetimeDisplay renders a sale timestamp in the shop's local timezone (the DB
// stores UTC; showing it raw was the old "times look 5h30m off" bug).
func datetimeDisplay(t time.Time) string { return datetime.DateTime(t) }

// financeSeg is one segment of the Finance "where every rupee went" bar.
type financeSeg struct {
	Label  string
	Amount decimal.Decimal
	Pct    float64 // width %, clamped to [0,100]
	Color  string  // CSS colour
}

// financeBar splits net sales revenue into cost-of-goods, expenses, other costs
// and the remainder (operating profit) as bar segments. Returns nil when there
// is no revenue to divide. Recharge/plugin earnings are NOT part of this bar —
// they are separate income shown alongside — so the bar reads "of sales revenue".
func financeBar(pl reports.PL, _ string) []financeSeg {
	base := pl.Revenue
	if !base.IsPositive() {
		return nil
	}
	other := pl.Losses.Add(pl.OwnUse).Add(pl.StaffWelfare).Add(pl.StockCorrections)
	if other.IsNegative() {
		other = decimal.Zero // a stock-count gain isn't a "cost" for the bar
	}
	hundred := decimal.NewFromInt(100)
	pct := func(v decimal.Decimal) float64 {
		f, _ := v.Div(base).Mul(hundred).Float64()
		if f < 0 {
			f = 0
		}
		if f > 100 {
			f = 100
		}
		return f
	}
	cogsP, expP, othP := pct(pl.COGS), pct(pl.Expenses), pct(other)
	profP := 100 - cogsP - expP - othP
	if profP < 0 {
		profP = 0
	}
	profit := base.Sub(pl.COGS).Sub(pl.Expenses).Sub(other)
	return []financeSeg{
		{"Cost of goods", pl.COGS, cogsP, "#94a3b8"},
		{"Expenses", pl.Expenses, expP, "#f59e0b"},
		{"Other costs", other, othP, "#fb7185"},
		{"Profit", profit, profP, "#10b981"},
	}
}

// pctLabel renders a segment's share as a whole-number percent.
func pctLabel(f float64) string { return strconv.Itoa(int(f+0.5)) + "%" }

// fmtPct1 renders a percentage with one decimal (handles negatives — e.g. a
// loss-making product's margin).
func fmtPct1(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) + "%" }

// barWidth is the inline style for an in-row bar, clamped to [0,100].
func barWidth(pct float64) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return "width:" + strconv.FormatFloat(pct, 'f', 1, 64) + "%"
}

// deltaInfo compares a current figure with the previous period's and returns a
// display string plus a direction (1 up, -1 down, 0 flat) for the ▲▼ badge.
func deltaInfo(cur, prev decimal.Decimal) (string, int) {
	if prev.IsZero() {
		if cur.IsZero() {
			return "no change", 0
		}
		return "new", 1
	}
	pct := cur.Sub(prev).Div(prev.Abs()).Mul(decimal.NewFromInt(100))
	switch {
	case cur.GreaterThan(prev):
		return money.Display(pct.Abs()) + "%", 1
	case cur.LessThan(prev):
		return money.Display(pct.Abs()) + "%", -1
	default:
		return "no change", 0
	}
}

// reportHubCard is one card on the Reports hub (built-in or plugin-contributed).
type reportHubCard struct {
	Href, Title, Desc string
}

// reportHubGroup is a labelled section of the Reports hub.
type reportHubGroup struct {
	Label string
	Cards []reportHubCard
}

// reportHubGroups returns the Reports-hub cards organised into labelled groups
// (instead of one flat alphabetical grid). Plugin-contributed cards go under a
// trailing "More" group so the hook stays intact. Curated order within a group
// beats alphabetical here — the common reports sit at the top.
func reportHubGroups() []reportHubGroup {
	groups := []reportHubGroup{
		{"Sales & Customers", []reportHubCard{
			{"/admin/reports/sales", "Sales", "Receipts, profit & time-of-day, with a daily trend"},
			{"/admin/reports/peak-hours", "Peak Hours", "Busiest days & hours — plan staffing and breaks"},
			{"/admin/reports/top-products", "Top Products", "Best sellers by revenue or quantity"},
			{"/admin/reports/product-sales", "Product Sales", "One product's units over time vs last year"},
			{"/admin/reports/sales-by-cashier", "Sales by Cashier", "Per-cashier takings & discounts"},
			{"/admin/reports/returns", "Returns / Refunds", "Returned lines and refund value"},
			{"/admin/reports/customer-dues", "Customer Dues", "Receivables — who owes you money"},
		}},
		{"Money & Profit", []reportHubCard{
			{"/admin/reports/finance", "Finance / P&L", "Revenue, COGS, profit, dues for a period"},
			{"/admin/reports/tender", "Tender / Payments", "Cash, card, wallet & credit collected"},
			{"/admin/reports/tax", "Tax Summary", "VAT/tax collected over a period"},
			{"/admin/reports/profit-by-category", "Profit by Category", "Net revenue & profit per category"},
			{"/admin/reports/expenses", "Expenses by Category", "Operating expenses grouped by category"},
			{"/admin/reports/cash-register", "Cash Register", "Drawer sessions with over/short"},
		}},
		{"Inventory & Suppliers", []reportHubCard{
			{"/admin/reports/inventory", "Inventory Valuation", "Stock on hand at cost & retail"},
			{"/admin/reports/low-stock", "Low Stock", "Items at or below reorder level"},
			{"/admin/reports/batches", "Batches / Expiry", "Live batches and expiry dates"},
			{"/admin/reports/purchases", "Purchases", "GRNs received in a period"},
			{"/admin/reports/supplier-dues", "Supplier Dues", "Payables — who you owe money"},
			{"/admin/reports/recipe-variance", "Recipe Variance", "Expected vs actual ingredient use"},
			{"/admin/reports/service-profit", "Service Profit", "Income, ingredients & costs per service"},
			{"/admin/reports/losses", "Losses & Recovery", "Damage & warranty write-offs vs supplier recovery"},
		}},
	}
	if plugins := layouts.PluginReportCards(); len(plugins) > 0 {
		more := reportHubGroup{Label: "More"}
		for _, rc := range plugins {
			more.Cards = append(more.Cards, reportHubCard{rc.Href, rc.Label, rc.Desc})
		}
		groups = append(groups, more)
	}
	return groups
}

// tenderLabel gives a payment-method enum value a display name for the tender
// report (unknown values pass through unchanged).
func tenderLabel(method string) string {
	switch method {
	case "cash":
		return "Cash"
	case "card":
		return "Card"
	case "online":
		return "Online"
	case "credit":
		return "Credit"
	case "wallet":
		return "Wallet (eZ Cash / mCash)"
	}
	return method
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
func lowStockConfigJSON(rows []products.Product, demand map[int64]ReorderInfo, notes map[int64]string) string {
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
		Note          string `json:"note"`
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
			Note: notes[p.ID],
		})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// LowStockPrintRow is one already-resolved line for the printable low-stock
// sheet — the same values the interactive report shows, computed server-side so
// the A4 print carries them without any Alpine.
type LowStockPrintRow struct {
	Name      string
	Note      string
	OnHand    string
	Unit      string
	Suggested string
	SoldWeek  string
	SoldMonth string
	SoldYear  string
	Supplier  string
}

// lowStockPrintRows resolves rows for the print sheet, mirroring
// lowStockConfigJSON's suggested-qty fallback (demand-based when there's sales
// history, else ≈ 2× reorder level − on-hand).
func lowStockPrintRows(rows []products.Product, demand map[int64]ReorderInfo, notes map[int64]string) []LowStockPrintRow {
	orZero := func(s string) string {
		if s == "" {
			return "0"
		}
		return s
	}
	out := make([]LowStockPrintRow, 0, len(rows))
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
		supplier := ""
		if p.PreferredSupplierName != nil {
			supplier = *p.PreferredSupplierName
		}
		out = append(out, LowStockPrintRow{
			Name: p.Name, Note: notes[p.ID], OnHand: p.StockQty.String(), Unit: p.UnitAbbr,
			Suggested: suggested,
			SoldWeek:  orZero(info.SoldLastWeek), SoldMonth: orZero(info.SoldLastMonth), SoldYear: orZero(info.SoldLastYear),
			Supplier: supplier,
		})
	}
	return out
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

// lowStockQuery is the reorder page's active filters as a query string, so the
// pager keeps the category / supplier / search selection across pages.
func lowStockQuery(d LowStockData) string {
	q := url.Values{}
	if d.Search != "" {
		q.Set("search", d.Search)
	}
	if d.CategoryID != "" {
		q.Set("category_id", d.CategoryID)
	}
	if d.SupplierID != "" {
		q.Set("supplier_id", d.SupplierID)
	}
	if d.Alt {
		q.Set("alt", "1")
	}
	return q.Encode()
}

// lowStockPrintURL builds the print-sheet link carrying the report's active
// filters. all=true prints every match; otherwise just the page being viewed.
func lowStockPrintURL(d LowStockData, all bool) string {
	q := url.Values{}
	if d.Search != "" {
		q.Set("search", d.Search)
	}
	if d.CategoryID != "" {
		q.Set("category_id", d.CategoryID)
	}
	if d.SupplierID != "" {
		q.Set("supplier_id", d.SupplierID)
	}
	if d.Alt {
		q.Set("alt", "1")
	}
	if all {
		q.Set("scope", "all")
	} else {
		q.Set("page", strconv.Itoa(d.Page))
	}
	return "/admin/reports/low-stock/print?" + q.Encode()
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
	setNonEmpty(q, "from_hour", d.FromHour)
	setNonEmpty(q, "to_hour", d.ToHour)
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
