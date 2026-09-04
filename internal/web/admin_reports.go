package web

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"karots-pos/internal/features/customers"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/reports"
	"karots-pos/internal/features/sales"
	"karots-pos/internal/features/suppliers"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"
	adminpages "karots-pos/templates/pages/admin"

	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

// shopName returns the configured shop name (falling back to a generic label).
func (a *adminUI) shopName(ctx context.Context) string {
	if cfg, err := a.s.settings.Get(ctx); err == nil && cfg.ShopName != "" {
		return cfg.ShopName
	}
	return "Shop"
}

// reportPageSize is how many detail rows a report shows on screen at once.
// Reports are read as a summary plus a sample; the CSV is the full artifact.
// 50 keeps a page short enough to scan (100 ran to roughly four screens).
const reportPageSize = 50

// pageParam reads ?page=, defaulting to the first page. Never returns < 1 — a
// zero page makes the "showing X–Y" line render nonsense like "-99–0".
func pageParam(c echo.Context) int {
	n, _ := strconv.Atoi(c.QueryParam("page"))
	if n < 1 {
		return 1
	}
	return n
}

// paginate returns the slice of rows for the given 1-based page. Used by the
// reports whose data source has no LIMIT of its own (batch lists, dues, and
// other whole-table reads) so a 600-row report still renders one screen.
func paginate[T any](rows []T, page, size int) []T {
	start := (page - 1) * size
	if start >= len(rows) {
		return nil
	}
	return rows[start:min(start+size, len(rows))]
}

// rangeStrings resolves the quick-pick preset (or from/to query params) into the
// period (with `to` exclusive of the end day) plus the user-facing date strings
// and the active preset key (for highlighting the range-bar button).
func rangeStrings(c echo.Context) (from, to time.Time, fromStr, toStr, preset string, err error) {
	preset = c.QueryParam("preset")
	from, to, fromStr, toStr, err = reports.ResolveRange(preset, c.QueryParam("from"), c.QueryParam("to"))
	return
}

func (a *adminUI) ReportsHub(c echo.Context) error {
	taxReg := false
	if cfg, err := a.s.settings.Get(c.Request().Context()); err == nil {
		taxReg = cfg.TaxRegistered
	}
	return response.RenderPage(c, adminpages.ReportsHub(adminpages.ReportsHubData{
		UserName:      middleware.CurrentUserName(c),
		TaxRegistered: taxReg,
	}))
}

// prevPeriod returns the equal-length period immediately before [from,to): a
// 7-day range compares against the 7 days before it. Used for ▲▼ deltas.
func prevPeriod(from, to time.Time) (pFrom, pTo time.Time) {
	d := to.Sub(from)
	return from.Add(-d), from
}

// hourParam reads an hour-of-day query param (0–23), returning nil when absent
// or out of range so the filter is simply skipped.
func hourParam(c echo.Context, name string) *int {
	v := c.QueryParam(name)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 23 {
		return nil
	}
	return &n
}

func (a *adminUI) SalesReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, preset, err := rangeStrings(c)
	if err != nil {
		return err
	}
	status := c.QueryParam("status")
	method := c.QueryParam("method")
	fromHour, toHour := hourParam(c, "from_hour"), hourParam(c, "to_hour")
	filter := sales.ListFilter{From: &from, To: &to, Status: status, Method: method, FromHour: fromHour, ToHour: toHour}

	// Totals come from an aggregate over the whole range — never from the rows
	// below, which are one page.
	sum, err := a.s.sales.Summarize(ctx, filter)
	if err != nil {
		return err
	}

	if wantsCSV(c) {
		// The CSV is the "every line" artifact, so it walks the full range in
		// pages rather than relying on a single oversized limit.
		out := make([][]string, 0, sum.Count)
		for offset := 0; offset < sum.Count; offset += sales.MaxListLimit {
			page := filter
			page.Limit, page.Offset = sales.MaxListLimit, offset
			rows, lerr := a.s.sales.List(ctx, page)
			if lerr != nil {
				return lerr
			}
			if len(rows) == 0 {
				break
			}
			for _, s := range rows {
				out = append(out, []string{
					s.ReceiptNo, s.CreatedAt.Format("2006-01-02 15:04"), s.SaleType, s.Status,
					csvMoney(s.Subtotal), csvMoney(s.Discount), csvMoney(s.Total),
					csvMoney(s.COGS), csvMoney(s.Profit),
				})
			}
		}
		return writeCSV(c, "sales_"+fromStr+"_"+toStr,
			[]string{"Receipt", "Date", "Type", "Status", "Gross", "Discount", "Total", "COGS", "Profit"}, out)
	}

	// Previous equal-length period, same non-date filters, for the ▲▼ deltas.
	pFrom, pTo := prevPeriod(from, to)
	prevFilter := filter
	prevFilter.From, prevFilter.To = &pFrom, &pTo
	prev, err := a.s.sales.Summarize(ctx, prevFilter)
	if err != nil {
		return err
	}

	// Folded Daily Sales Trend: day-by-day revenue/profit strip.
	trend, err := a.s.reports.SalesByPeriod(ctx, from, to, "day")
	if err != nil {
		return err
	}

	page := pageParam(c)
	filter.Limit, filter.Offset = reportPageSize, (page-1)*reportPageSize
	rows, err := a.s.sales.List(ctx, filter)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.SalesReport(adminpages.SalesReportData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx),
		From: fromStr, To: toStr, Preset: preset, Status: status, Method: method,
		FromHour: c.QueryParam("from_hour"), ToHour: c.QueryParam("to_hour"),
		Rows:  rows,
		Sum:   *sum, Prev: *prev, Trend: trend,
		Page: page, PageSize: reportPageSize,
	}))
}

// SalesReceiptLines returns the line-items partial for one receipt, loaded lazily
// by HTMX when a Sales-report row is expanded (drill-in).
func (a *adminUI) SalesReceiptLines(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	d, err := a.s.sales.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.SalesReceiptLines(a.symbol(c.Request().Context()), d.Items))
}

// PeakHoursReport shows sales activity by local day-of-week × hour-of-day — the
// staffing view. The handler shapes the buckets into a 7×24 grid so the template
// stays a plain heatmap.
func (a *adminUI) PeakHoursReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, preset, err := rangeStrings(c)
	if err != nil {
		return err
	}
	buckets, err := a.s.reports.PeakHours(ctx, from, to)
	if err != nil {
		return err
	}

	if wantsCSV(c) {
		out := make([][]string, 0, len(buckets))
		for _, b := range buckets {
			out = append(out, []string{
				adminpages.WeekdayName(b.DOW), strconv.Itoa(b.Hour),
				strconv.Itoa(b.Count), csvMoney(b.Revenue),
			})
		}
		return writeCSV(c, "peak_hours_"+fromStr+"_"+toStr,
			[]string{"Day", "Hour", "Sales", "Revenue"}, out)
	}

	d := adminpages.PeakHoursData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx),
		From: fromStr, To: toStr, Preset: preset,
	}
	var busiest reports.PeakBucket
	for _, b := range buckets {
		if b.DOW < 0 || b.DOW > 6 || b.Hour < 0 || b.Hour > 23 {
			continue
		}
		d.Grid[b.DOW][b.Hour] = adminpages.PeakCell{Count: b.Count, Revenue: b.Revenue}
		d.HourTotals[b.Hour].Count += b.Count
		d.HourTotals[b.Hour].Revenue = d.HourTotals[b.Hour].Revenue.Add(b.Revenue)
		d.DayTotals[b.DOW].Count += b.Count
		d.DayTotals[b.DOW].Revenue = d.DayTotals[b.DOW].Revenue.Add(b.Revenue)
		d.TotalCount += b.Count
		d.TotalRevenue = d.TotalRevenue.Add(b.Revenue)
		if b.Count > d.MaxCount {
			d.MaxCount = b.Count
		}
		if b.Count > busiest.Count {
			busiest = b
		}
	}
	if busiest.Count > 0 {
		d.BusiestLabel = adminpages.WeekdayName(busiest.DOW) + " " + fmtHour(busiest.Hour)
	}
	return response.RenderPage(c, adminpages.PeakHoursReport(d))
}

// fmtHour renders an hour-of-day as "07:00".
func fmtHour(h int) string {
	if h < 10 {
		return "0" + strconv.Itoa(h) + ":00"
	}
	return strconv.Itoa(h) + ":00"
}

// CashierSalesReport ranks sales per cashier over a range (staff performance).
func (a *adminUI) CashierSalesReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, preset, err := rangeStrings(c)
	if err != nil {
		return err
	}
	rows, err := a.s.reports.SalesByCashier(ctx, from, to)
	if err != nil {
		return err
	}
	d := adminpages.CashierSalesData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, Preset: preset,
	}
	for _, r := range rows {
		d.Count += r.Count
		d.Gross = d.Gross.Add(r.Gross)
		d.Discount = d.Discount.Add(r.Discount)
		d.Net = d.Net.Add(r.Net)
	}
	if d.Count > 0 {
		d.AvgBasket = d.Net.Div(decimal.NewFromInt(int64(d.Count)))
	}
	// Rows are ORDER BY net DESC, so the first is the top earner — the bar scale.
	maxNetF, totalNetF := 0.0, d.Net.InexactFloat64()
	if len(rows) > 0 {
		maxNetF = rows[0].Net.InexactFloat64()
	}
	d.Rows = make([]adminpages.CashierRow, len(rows))
	for i, r := range rows {
		cr := adminpages.CashierRow{Rank: i + 1, R: r}
		if r.Count > 0 {
			cr.AvgBasket = r.Net.Div(decimal.NewFromInt(int64(r.Count)))
		}
		if maxNetF > 0 {
			if f := r.Net.InexactFloat64() / maxNetF * 100; f > 0 {
				cr.BarPct = f
			}
		}
		if totalNetF > 0 {
			cr.SharePct = r.Net.InexactFloat64() / totalNetF * 100
		}
		d.Rows[i] = cr
	}
	if wantsCSV(c) {
		out := make([][]string, 0, len(rows))
		for _, r := range d.Rows {
			out = append(out, []string{
				r.R.Cashier, strconv.Itoa(r.R.Count), csvMoney(r.R.Gross), csvMoney(r.R.Discount),
				csvMoney(r.AvgBasket), csvMoney(r.R.Net),
			})
		}
		return writeCSV(c, "sales_by_cashier_"+fromStr+"_"+toStr,
			[]string{"Cashier", "Sales", "Gross", "Discount", "Avg basket", "Net"}, out)
	}
	return response.RenderPage(c, adminpages.CashierSalesReport(d))
}

// ExpensesReport breaks operating expenses down by category over a range — the
// detail behind the single "operating expenses" line in the P&L.
func (a *adminUI) ExpensesReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, preset, err := rangeStrings(c)
	if err != nil {
		return err
	}
	rows, err := a.s.reports.ExpensesByCategory(ctx, from, to)
	if err != nil {
		return err
	}
	d := adminpages.ExpensesReportData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, Preset: preset,
	}
	for _, r := range rows {
		d.Count += r.Count
		d.Total = d.Total.Add(r.Total)
	}
	// Rows are ORDER BY total DESC, so the first is the biggest spend — the bar
	// scale and share denominator.
	maxTotalF, totalF := 0.0, d.Total.InexactFloat64()
	if len(rows) > 0 {
		maxTotalF = rows[0].Total.InexactFloat64()
	}
	d.Rows = make([]adminpages.ExpenseCatDisplay, len(rows))
	for i, r := range rows {
		er := adminpages.ExpenseCatDisplay{R: r}
		if maxTotalF > 0 {
			if f := r.Total.InexactFloat64() / maxTotalF * 100; f > 0 {
				er.BarPct = f
			}
		}
		if totalF > 0 {
			er.SharePct = r.Total.InexactFloat64() / totalF * 100
		}
		d.Rows[i] = er
	}
	if wantsCSV(c) {
		out := make([][]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, []string{r.Category, strconv.Itoa(r.Count), csvMoney(r.Total)})
		}
		return writeCSV(c, "expenses_by_category_"+fromStr+"_"+toStr,
			[]string{"Category", "Count", "Total"}, out)
	}
	return response.RenderPage(c, adminpages.ExpensesReport(d))
}

// TopProductsReport ranks best-selling products by net revenue or net quantity
// over a range — the multi-product ranking Product Sales (one product at a time)
// can't give.
func (a *adminUI) TopProductsReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, preset, err := rangeStrings(c)
	if err != nil {
		return err
	}
	orderBy := c.QueryParam("order")
	switch orderBy {
	case "qty", "profit":
	default:
		orderBy = "revenue"
	}
	rows, err := a.s.reports.TopProducts(ctx, from, to, orderBy, 50)
	if err != nil {
		return err
	}
	// The metric the ranking (and the in-row bars + Pareto share) is built on.
	metric := func(r reports.ProductRevenue) decimal.Decimal {
		switch orderBy {
		case "qty":
			return r.Qty
		case "profit":
			return r.Profit
		default:
			return r.Revenue
		}
	}
	d := adminpages.TopProductsData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx),
		From: fromStr, To: toStr, Preset: preset, Order: orderBy, Count: len(rows),
	}
	var total decimal.Decimal
	for _, r := range rows {
		d.TotalQty = d.TotalQty.Add(r.Qty)
		d.TotalRevenue = d.TotalRevenue.Add(r.Revenue)
		d.TotalProfit = d.TotalProfit.Add(r.Profit)
		total = total.Add(metric(r))
	}
	if d.TotalRevenue.GreaterThan(decimal.Zero) {
		d.OverallMargin = d.TotalProfit.Div(d.TotalRevenue).InexactFloat64() * 100
	}
	switch orderBy {
	case "qty":
		d.MetricLabel = "units"
	case "profit":
		d.MetricLabel = "profit"
	default:
		d.MetricLabel = "revenue"
	}
	// Rows are ORDER BY metric DESC, so the first row's metric is the max — the
	// bar scale. Cumulative share drives the Pareto (80/20) callout.
	maxF, totalF := 0.0, total.InexactFloat64()
	if len(rows) > 0 {
		maxF = metric(rows[0]).InexactFloat64()
	}
	var run decimal.Decimal
	d.Rows = make([]adminpages.TopProductRow, len(rows))
	for i, r := range rows {
		run = run.Add(metric(r))
		tr := adminpages.TopProductRow{Rank: i + 1, R: r}
		if r.Revenue.GreaterThan(decimal.Zero) {
			tr.Margin = r.Profit.Div(r.Revenue).InexactFloat64() * 100
		}
		if maxF > 0 {
			if f := metric(r).InexactFloat64() / maxF * 100; f > 0 {
				tr.BarPct = f
			}
		}
		if totalF > 0 {
			tr.CumPct = run.InexactFloat64() / totalF * 100
			if d.ParetoN == 0 && tr.CumPct >= 80 {
				d.ParetoN = i + 1
			}
		}
		d.Rows[i] = tr
	}
	if wantsCSV(c) {
		out := make([][]string, 0, len(rows))
		for _, r := range rows {
			m := "0"
			if r.Revenue.GreaterThan(decimal.Zero) {
				m = r.Profit.Div(r.Revenue).Mul(decimal.NewFromInt(100)).StringFixed(1)
			}
			out = append(out, []string{r.ProductName, r.Qty.String(), csvMoney(r.Revenue), csvMoney(r.Profit), m})
		}
		return writeCSV(c, "top_products_"+fromStr+"_"+toStr,
			[]string{"Product", "Qty sold", "Revenue", "Profit", "Margin %"}, out)
	}
	return response.RenderPage(c, adminpages.TopProductsReport(d))
}

func (a *adminUI) FinanceReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, preset, err := rangeStrings(c)
	if err != nil {
		return err
	}
	pl, err := a.s.reports.Compute(ctx, from, to)
	if err != nil {
		return err
	}
	income := pluginIncomeLines(ctx, from, to)
	if wantsCSV(c) {
		out := [][]string{
			{"Gross revenue", csvMoney(pl.GrossRevenue)},
			{"Returns", csvMoney(pl.Returns)},
			{"Net revenue", csvMoney(pl.Revenue)},
			{"COGS", csvMoney(pl.COGS)},
			{"Gross profit", csvMoney(pl.GrossProfit)},
			{"Operating expenses", csvMoney(pl.Expenses)},
			{"Stock losses", csvMoney(pl.Losses)},
			{"Shop own use", csvMoney(pl.OwnUse)},
			{"Staff welfare", csvMoney(pl.StaffWelfare)},
			{"Stock corrections (count)", csvMoney(pl.StockCorrections)},
			{"Register over/short", csvMoney(pl.RegisterDiff)},
			{"Revenue sold at no recorded cost", csvMoney(pl.ZeroCostRevenue)},
			{"Supplier recoveries", csvMoney(pl.Recoveries)},
			{"Other income (interest)", csvMoney(pl.OtherIncome)},
		}
		net := pl.NetProfit
		for _, l := range income {
			out = append(out, []string{l.Label, csvMoney(l.Amount)})
			net = net.Add(l.Amount)
		}
		out = append(out,
			[]string{"Net profit", csvMoney(net)},
			[]string{"Sale tender (paid at sale)", csvMoney(pl.Received)},
			[]string{"Receivables", csvMoney(pl.Receivables)},
			[]string{"Payables", csvMoney(pl.Payables)},
			[]string{"Supplier credit (they owe us)", csvMoney(pl.SupplierCredit)},
		)
		return writeCSV(c, "finance_"+fromStr+"_"+toStr, []string{"Line", "Amount"}, out)
	}
	return response.RenderPage(c, adminpages.FinanceReport(adminpages.FinanceReportData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, Preset: preset, PL: *pl,
		PluginIncome: income,
	}))
}

func (a *adminUI) ReturnsReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, preset, err := rangeStrings(c)
	if err != nil {
		return err
	}
	rows, err := a.s.reports.Returns(ctx, from, to)
	if err != nil {
		return err
	}
	// Total covers every return in the range; Rows is one page of them.
	d := adminpages.ReturnsReportData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, Preset: preset,
		Total: len(rows), Page: pageParam(c), PageSize: reportPageSize,
	}
	// Summarise the whole range (not just the page) + rank the worst-offending
	// products, all from the rows already in hand — no extra query.
	type retAgg struct {
		qty, refund decimal.Decimal
	}
	byProduct := map[string]*retAgg{}
	receipts := map[string]struct{}{}
	for _, r := range rows {
		d.TotalRefund = d.TotalRefund.Add(r.RefundValue)
		d.TotalQty = d.TotalQty.Add(r.Qty)
		receipts[r.ReceiptNo] = struct{}{}
		g := byProduct[r.ProductName]
		if g == nil {
			g = &retAgg{}
			byProduct[r.ProductName] = g
		}
		g.qty = g.qty.Add(r.Qty)
		g.refund = g.refund.Add(r.RefundValue)
	}
	d.Receipts = len(receipts)
	top := make([]adminpages.ReturnProductRow, 0, len(byProduct))
	for name, g := range byProduct {
		top = append(top, adminpages.ReturnProductRow{Name: name, Qty: g.qty, Refund: g.refund})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].Refund.GreaterThan(top[j].Refund) })
	if len(top) > 10 {
		top = top[:10]
	}
	totalRefundF := d.TotalRefund.InexactFloat64()
	maxRefundF := 0.0
	if len(top) > 0 {
		maxRefundF = top[0].Refund.InexactFloat64()
	}
	for i := range top {
		v := top[i].Refund.InexactFloat64()
		if maxRefundF > 0 && v > 0 {
			top[i].BarPct = v / maxRefundF * 100
		}
		if totalRefundF > 0 {
			top[i].SharePct = v / totalRefundF * 100
		}
	}
	d.TopProducts = top
	if wantsCSV(c) {
		out := make([][]string, 0, len(rows))
		for _, r := range rows {
			cust := ""
			if r.Customer != nil {
				cust = *r.Customer
			}
			out = append(out, []string{
				r.SaleDate.Format("2006-01-02 15:04"), r.ReceiptNo, r.ProductName,
				r.Qty.String(), csvMoney(r.RefundValue), cust,
			})
		}
		return writeCSV(c, "returns_"+fromStr+"_"+toStr,
			[]string{"Sale date", "Receipt", "Product", "Qty", "Refund", "Customer"}, out)
	}
	d.Rows = paginate(rows, d.Page, reportPageSize)
	return response.RenderPage(c, adminpages.ReturnsReport(d))
}

func (a *adminUI) ProfitByCategoryReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, preset, err := rangeStrings(c)
	if err != nil {
		return err
	}
	cats := c.QueryParams()["category"]
	rows, err := a.s.reports.ProfitByCategory(ctx, from, to, cats...)
	if err != nil {
		return err
	}
	allCats, err := a.s.reports.CategoryNames(ctx)
	if err != nil {
		return err
	}
	d := adminpages.CategoryProfitData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, Preset: preset,
		AllCats: allCats, SelCats: cats,
	}
	for _, r := range rows {
		d.TotalRevenue = d.TotalRevenue.Add(r.Revenue)
		d.TotalProfit = d.TotalProfit.Add(r.Profit)
	}
	if d.TotalRevenue.IsPositive() {
		d.TotalMargin = d.TotalProfit.Div(d.TotalRevenue).InexactFloat64() * 100
	}
	// Rows are ORDER BY profit DESC, so the first is the biggest earner — the bar
	// scale. In-row bars replace the old SVG chart, which shrank to nothing once
	// there were many categories.
	maxProfitF := 0.0
	if len(rows) > 0 {
		maxProfitF = rows[0].Profit.InexactFloat64()
	}
	d.Rows = make([]adminpages.CategoryProfitRow, len(rows))
	for i, r := range rows {
		cr := adminpages.CategoryProfitRow{R: r}
		if r.Revenue.IsPositive() {
			cr.Margin = r.Profit.Div(r.Revenue).InexactFloat64() * 100
		}
		if maxProfitF > 0 {
			if f := r.Profit.InexactFloat64() / maxProfitF * 100; f > 0 {
				cr.BarPct = f
			}
		}
		d.Rows[i] = cr
	}
	if wantsCSV(c) {
		out := make([][]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, []string{
				r.Category, csvMoney(r.Revenue), csvMoney(r.COGS), csvMoney(r.Profit),
			})
		}
		return writeCSV(c, "profit_by_category_"+fromStr+"_"+toStr,
			[]string{"Category", "Revenue", "COGS", "Profit"}, out)
	}
	return response.RenderPage(c, adminpages.ProfitByCategoryReport(d))
}

func (a *adminUI) SalesTrendReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, preset, err := rangeStrings(c)
	if err != nil {
		return err
	}
	group := c.QueryParam("group")
	if group == "" {
		group = "day"
	}
	rows, err := a.s.reports.SalesByPeriod(ctx, from, to, group)
	if err != nil {
		return err
	}
	d := adminpages.SalesTrendData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, Preset: preset, Group: group, Rows: rows,
	}
	for _, r := range rows {
		d.TotalRevenue = d.TotalRevenue.Add(r.Revenue)
		d.TotalProfit = d.TotalProfit.Add(r.Profit)
		if r.Revenue.GreaterThan(d.MaxRevenue) {
			d.MaxRevenue = r.Revenue
		}
	}
	if wantsCSV(c) {
		out := make([][]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, []string{
				r.Day.Format("2006-01-02"), strconv.Itoa(r.Count), csvMoney(r.Revenue), csvMoney(r.Profit),
			})
		}
		return writeCSV(c, "sales_trend_"+fromStr+"_"+toStr,
			[]string{"Date", "Sales", "Revenue", "Profit"}, out)
	}
	return response.RenderPage(c, adminpages.SalesTrendReport(d))
}

// ProductSalesReport charts one product's units sold over time, with a same-period
// last-year overlay — the per-product view that feeds reorder intuition.
func (a *adminUI) ProductSalesReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, preset, err := rangeStrings(c)
	if err != nil {
		return err
	}
	group := c.QueryParam("group")
	if group == "" {
		group = "month"
	}
	var pid int64
	if v := c.QueryParam("product"); v != "" {
		pid, _ = strconv.ParseInt(v, 10, 64)
	}
	d := adminpages.ProductSalesData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx),
		From: fromStr, To: toStr, Preset: preset, Group: group, ProductID: pid,
	}
	if pid > 0 {
		if p, err := a.s.products.Get(ctx, pid); err == nil && p != nil {
			d.ProductName = p.Name
		}
		rows, err := a.s.reports.ProductSalesByPeriod(ctx, pid, from, to, group)
		if err != nil {
			return err
		}
		ly, err := a.s.reports.ProductSalesByPeriod(ctx, pid, from.AddDate(-1, 0, 0), to.AddDate(-1, 0, 0), group)
		if err != nil {
			return err
		}
		d.Rows = rows
		d.LastYear = ly
		for _, r := range rows {
			d.TotalQty = d.TotalQty.Add(r.Qty)
			d.TotalRevenue = d.TotalRevenue.Add(r.Revenue)
		}
	}
	return response.RenderPage(c, adminpages.ProductSalesReport(d))
}

func (a *adminUI) WarrantyReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, preset, err := rangeStrings(c)
	if err != nil {
		return err
	}
	sum, err := a.s.reports.WarrantyAndRecovery(ctx, from, to)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.WarrantyReport(adminpages.WarrantyReportData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, Preset: preset, Summary: *sum,
	}))
}

func (a *adminUI) CashRegisterReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, preset, err := rangeStrings(c)
	if err != nil {
		return err
	}
	rows, err := a.s.cashRegister.SessionsInRange(ctx, from, to)
	if err != nil {
		return err
	}
	d := adminpages.CashReportData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, Preset: preset,
		Total: len(rows), Page: pageParam(c), PageSize: reportPageSize,
	}
	for _, s := range rows {
		d.Opening = d.Opening.Add(s.OpeningCash)
		if s.ExpectedCash != nil {
			d.Expected = d.Expected.Add(*s.ExpectedCash)
		}
		if s.ClosingCash != nil {
			d.Counted = d.Counted.Add(*s.ClosingCash)
		}
		if s.Difference != nil {
			d.OverShort = d.OverShort.Add(*s.Difference)
			if s.Difference.IsNegative() {
				d.ShortCount++
			}
		}
	}
	if wantsCSV(c) {
		out := make([][]string, 0, len(rows))
		for _, s := range rows {
			closedAt, expected, closing, diff := "", "", "", ""
			if s.ClosedAt != nil {
				closedAt = s.ClosedAt.Format("2006-01-02 15:04")
			}
			if s.ExpectedCash != nil {
				expected = csvMoney(*s.ExpectedCash)
			}
			if s.ClosingCash != nil {
				closing = csvMoney(*s.ClosingCash)
			}
			if s.Difference != nil {
				diff = csvMoney(*s.Difference)
			}
			out = append(out, []string{
				s.UserName, s.OpenedAt.Format("2006-01-02 15:04"), closedAt,
				csvMoney(s.OpeningCash), expected, closing, diff,
			})
		}
		return writeCSV(c, "cash_register_"+fromStr+"_"+toStr,
			[]string{"Cashier", "Opened", "Closed", "Opening", "Expected", "Counted", "Over/Short"}, out)
	}
	d.Rows = paginate(rows, d.Page, reportPageSize)
	return response.RenderPage(c, adminpages.CashRegisterReport(d))
}

func (a *adminUI) PurchasesReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, preset, err := rangeStrings(c)
	if err != nil {
		return err
	}
	// Received purchases only, and the whole range — the previous version listed
	// the most recent 100 purchases of any status and filtered afterwards, so
	// drafts inflated the totals and an older range could come back near-empty.
	inRange, err := a.s.purchases.ListBetween(ctx, from, to)
	if err != nil {
		return err
	}
	d := adminpages.PurchasesReportData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, Preset: preset,
		Page: pageParam(c), PageSize: reportPageSize,
	}
	for _, p := range inRange {
		d.Total = d.Total.Add(p.Total)
		d.Paid = d.Paid.Add(p.PaidAmount)
		d.Due = d.Due.Add(p.Total.Sub(p.PaidAmount))
	}
	d.Count = len(inRange)
	// The CSV carries every purchase in the range, not the page being viewed —
	// a download that silently stopped at the page break would be the same class
	// of quiet under-reporting as the row cap this report used to have.
	if wantsCSV(c) {
		out := make([][]string, 0, len(inRange))
		for _, p := range inRange {
			invoice := ""
			if p.InvoiceNo != nil {
				invoice = *p.InvoiceNo
			}
			out = append(out, []string{
				p.CreatedAt.Format("2006-01-02"),
				p.SupplierName,
				invoice,
				p.Status,
				csvMoney(p.Total),
				csvMoney(p.PaidAmount),
				csvMoney(p.Total.Sub(p.PaidAmount)),
			})
		}
		return writeCSV(c, "purchases_"+fromStr+"_to_"+toStr,
			[]string{"Date", "Supplier", "Invoice", "Status", "Total", "Paid", "Due"}, out)
	}
	d.Rows = paginate(inRange, d.Page, reportPageSize)
	return response.RenderPage(c, adminpages.PurchasesReport(d))
}

func (a *adminUI) CustomerDuesReport(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.customers.Owing(ctx)
	if err != nil {
		return err
	}
	d := adminpages.CustomerDuesData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), Count: len(rows),
	}
	now := time.Now()
	// age in whole days from the oldest credit sale (0 when unknown).
	ageDays := func(r customers.OwingRow) int {
		if r.OldestCredit == nil {
			return 0
		}
		return int(now.Sub(*r.OldestCredit).Hours() / 24)
	}
	// Rows are ORDER BY outstanding DESC, so the first is the biggest debtor.
	maxDueF := 0.0
	if len(rows) > 0 {
		maxDueF = rows[0].OutstandingBalance.InexactFloat64()
	}
	d.Rows = make([]adminpages.CustomerDueRow, len(rows))
	for i, r := range rows {
		age := ageDays(r)
		d.TotalDue = d.TotalDue.Add(r.OutstandingBalance)
		if age > d.OldestDays {
			d.OldestDays = age
		}
		overLimit := r.CreditLimit.IsPositive() && r.OutstandingBalance.GreaterThan(r.CreditLimit)
		if overLimit {
			d.OverLimit++
		}
		// AR aging: bucket the whole balance by the oldest credit's age.
		// ponytail: customer-level aging (oldest date, not per-invoice) — upgrade
		// to per-invoice buckets if a customer ever mixes very old + fresh debt.
		switch {
		case age <= 30:
			d.Buckets[0] = d.Buckets[0].Add(r.OutstandingBalance)
		case age <= 60:
			d.Buckets[1] = d.Buckets[1].Add(r.OutstandingBalance)
		case age <= 90:
			d.Buckets[2] = d.Buckets[2].Add(r.OutstandingBalance)
		default:
			d.Buckets[3] = d.Buckets[3].Add(r.OutstandingBalance)
		}
		row := adminpages.CustomerDueRow{R: r, AgeDays: age, OverLimit: overLimit}
		if maxDueF > 0 {
			if f := r.OutstandingBalance.InexactFloat64() / maxDueF * 100; f > 0 {
				row.BarPct = f
			}
		}
		d.Rows[i] = row
	}
	if wantsCSV(c) {
		out := make([][]string, 0, len(rows))
		for _, r := range rows {
			phone := ""
			if r.Phone != nil {
				phone = *r.Phone
			}
			oldest := ""
			if r.OldestCredit != nil {
				oldest = r.OldestCredit.Format("2006-01-02")
			}
			out = append(out, []string{
				r.Name, phone, csvMoney(r.CreditLimit), csvMoney(r.OutstandingBalance), oldest,
				strconv.Itoa(ageDays(r)),
			})
		}
		return writeCSV(c, "customer_dues",
			[]string{"Customer", "Phone", "Credit limit", "Outstanding", "Oldest credit", "Age (days)"}, out)
	}
	return response.RenderPage(c, adminpages.CustomerDuesReport(d))
}

func (a *adminUI) SupplierDuesReport(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.suppliers.Owing(ctx)
	if err != nil {
		return err
	}
	d := adminpages.SupplierDuesData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), Count: len(rows),
	}
	now := time.Now()
	ageDays := func(r suppliers.OwingRow) int {
		if r.OldestUnpaid == nil {
			return 0
		}
		return int(now.Sub(*r.OldestUnpaid).Hours() / 24)
	}
	// Rows are ORDER BY outstanding DESC, so the first is the biggest we owe.
	maxDueF := 0.0
	if len(rows) > 0 {
		maxDueF = rows[0].OutstandingBalance.InexactFloat64()
	}
	d.Rows = make([]adminpages.SupplierDueRow, len(rows))
	for i, r := range rows {
		age := ageDays(r)
		d.TotalDue = d.TotalDue.Add(r.OutstandingBalance)
		if age > d.OldestDays {
			d.OldestDays = age
		}
		// Overdue = unpaid past the supplier's payment terms (credit days).
		overdue := r.CreditDays > 0 && r.OldestUnpaid != nil && age > r.CreditDays
		if overdue {
			d.OverdueCount++
			d.OverdueAmount = d.OverdueAmount.Add(r.OutstandingBalance)
		}
		// AP aging: bucket the whole balance by the oldest unpaid invoice's age.
		// ponytail: supplier-level aging (oldest date, not per-invoice).
		switch {
		case age <= 30:
			d.Buckets[0] = d.Buckets[0].Add(r.OutstandingBalance)
		case age <= 60:
			d.Buckets[1] = d.Buckets[1].Add(r.OutstandingBalance)
		case age <= 90:
			d.Buckets[2] = d.Buckets[2].Add(r.OutstandingBalance)
		default:
			d.Buckets[3] = d.Buckets[3].Add(r.OutstandingBalance)
		}
		row := adminpages.SupplierDueRow{R: r, AgeDays: age, Overdue: overdue}
		if maxDueF > 0 {
			if f := r.OutstandingBalance.InexactFloat64() / maxDueF * 100; f > 0 {
				row.BarPct = f
			}
		}
		d.Rows[i] = row
	}
	if wantsCSV(c) {
		out := make([][]string, 0, len(rows))
		for _, r := range rows {
			phone := ""
			if r.Phone != nil {
				phone = *r.Phone
			}
			oldest := ""
			if r.OldestUnpaid != nil {
				oldest = r.OldestUnpaid.Format("2006-01-02")
			}
			out = append(out, []string{
				r.Name, phone, strconv.Itoa(r.CreditDays), csvMoney(r.OutstandingBalance), oldest,
				strconv.Itoa(ageDays(r)),
			})
		}
		return writeCSV(c, "supplier_dues",
			[]string{"Supplier", "Phone", "Credit days", "Outstanding", "Oldest unpaid", "Age (days)"}, out)
	}
	return response.RenderPage(c, adminpages.SupplierDuesReport(d))
}

// SupplierSpendReport ranks suppliers by purchase spend over a range — the
// supplier twin of Top Products.
func (a *adminUI) SupplierSpendReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, preset, err := rangeStrings(c)
	if err != nil {
		return err
	}
	rows, err := a.s.reports.SupplierSpend(ctx, from, to)
	if err != nil {
		return err
	}
	d := adminpages.SupplierSpendData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, Preset: preset,
		Count: len(rows),
	}
	for _, r := range rows {
		d.TotalSpend = d.TotalSpend.Add(r.Spend)
		d.TotalPaid = d.TotalPaid.Add(r.Paid)
		d.TotalDue = d.TotalDue.Add(r.Due)
		d.Orders += r.Orders
	}
	// Rows are ORDER BY spend DESC, so the first is the biggest supplier.
	maxSpendF, totalSpendF := 0.0, d.TotalSpend.InexactFloat64()
	if len(rows) > 0 {
		maxSpendF = rows[0].Spend.InexactFloat64()
	}
	d.Rows = make([]adminpages.SupplierSpendDisplay, len(rows))
	for i, r := range rows {
		sr := adminpages.SupplierSpendDisplay{R: r}
		if maxSpendF > 0 {
			if f := r.Spend.InexactFloat64() / maxSpendF * 100; f > 0 {
				sr.BarPct = f
			}
		}
		if totalSpendF > 0 {
			sr.SharePct = r.Spend.InexactFloat64() / totalSpendF * 100
		}
		d.Rows[i] = sr
	}
	if wantsCSV(c) {
		out := make([][]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, []string{
				r.Supplier, strconv.Itoa(r.Orders), csvMoney(r.Spend), csvMoney(r.Paid), csvMoney(r.Due),
			})
		}
		return writeCSV(c, "supplier_spend_"+fromStr+"_"+toStr,
			[]string{"Supplier", "Orders", "Spend", "Paid", "Due"}, out)
	}
	return response.RenderPage(c, adminpages.SupplierSpendReport(d))
}

func (a *adminUI) TaxReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, preset, err := rangeStrings(c)
	if err != nil {
		return err
	}
	sum, err := a.s.reports.TaxSummary(ctx, from, to)
	if err != nil {
		return err
	}
	if wantsCSV(c) {
		out := make([][]string, 0, len(sum.Rows))
		for _, r := range sum.Rows {
			out = append(out, []string{
				r.Day.Format("2006-01-02"), strconv.Itoa(r.Count), csvMoney(r.Base), csvMoney(r.Tax),
			})
		}
		return writeCSV(c, "tax_"+fromStr+"_"+toStr,
			[]string{"Date", "Sales", "Taxable base", "Tax"}, out)
	}
	return response.RenderPage(c, adminpages.TaxReport(adminpages.TaxReportData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, Preset: preset, Summary: *sum,
	}))
}

// InventoryReport values the stock on hand. The headline numbers and the
// per-category breakdown are aggregated in SQL over the whole catalog — never
// summed from the rendered rows, which are only ever one page.
func (a *adminUI) InventoryReport(c echo.Context) error {
	ctx := c.Request().Context()

	var q products.ValuationQuery
	if idStr := c.QueryParam("category_id"); idStr != "" {
		if id, perr := strconv.ParseInt(idStr, 10, 64); perr == nil && id > 0 {
			q.CategoryID = &id
		}
	}
	q.IncludeZero = c.QueryParam("include_zero") == "1"
	q.Page, _ = strconv.Atoi(c.QueryParam("page"))
	// Normalize here too: the template renders the pager off q.Page, and an
	// un-normalized 0 would read "Showing -99–0 of 598".
	q.Normalize()

	if wantsCSV(c) {
		rows, err := a.s.products.ValuationAll(ctx, q)
		if err != nil {
			return err
		}
		out := make([][]string, 0, len(rows))
		for _, p := range rows {
			out = append(out, []string{
				p.Name, ptrStr(p.Barcode), p.CategoryName, p.UnitAbbr,
				p.StockQty.String(), csvMoney(p.CostPrice), csvMoney(p.SellingPrice),
				csvMoney(p.StockQty.Mul(p.CostPrice)), csvMoney(p.StockQty.Mul(p.SellingPrice)),
			})
		}
		return writeCSV(c, "inventory_valuation_"+time.Now().Format("2006-01-02"),
			[]string{"Product", "Barcode", "Category", "Unit", "On hand",
				"Cost", "Retail", "Cost value", "Retail value"}, out)
	}

	val, err := a.s.products.Valuation(ctx, q.CategoryID)
	if err != nil {
		return err
	}
	rows, total, err := a.s.products.ValuationDetail(ctx, q, reportPageSize)
	if err != nil {
		return err
	}
	cats, err := a.s.categories.Tree(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.InventoryReport(adminpages.InventoryReportData{
		ShopName:    a.shopName(ctx),
		Symbol:      a.symbol(ctx),
		Val:         *val,
		Breadcrumb:  val.Breadcrumb,
		Categories:  cats,
		Rows:        rows,
		Total:       total,
		Page:        q.Page,
		PageSize:    reportPageSize,
		CategoryID:  q.CategoryID,
		IncludeZero: q.IncludeZero,
	}))
}

func (a *adminUI) BatchReport(c echo.Context) error {
	ctx := c.Request().Context()
	daysStr := c.QueryParam("days")
	rows, err := a.s.stock.AllBatches(ctx)
	if err != nil {
		return err
	}
	// Optional "expiring within N days" filter (blank = all live batches).
	if daysStr != "" {
		if days, derr := strconv.Atoi(daysStr); derr == nil && days >= 0 {
			cutoff := time.Now().AddDate(0, 0, days)
			filtered := rows[:0:0]
			for _, b := range rows {
				if b.ExpiryDate != nil && !b.ExpiryDate.After(cutoff) {
					filtered = append(filtered, b)
				}
			}
			rows = filtered
		}
	}
	// Totals cover every matching batch; the table below shows one page of them.
	d := adminpages.BatchReportData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), Days: daysStr,
		Total: len(rows), PageSize: reportPageSize,
	}
	now := time.Now()
	soon := now.AddDate(0, 0, 30)
	for _, b := range rows {
		d.TotalValue = d.TotalValue.Add(b.QtyRemaining.Mul(b.CostPrice))
		switch {
		case b.ExpiryDate == nil:
			d.NoExpiry++
		case b.ExpiryDate.Before(now):
			d.Expired++
			d.ExpiredValue = d.ExpiredValue.Add(b.QtyRemaining.Mul(b.CostPrice))
		case b.ExpiryDate.Before(soon):
			d.ExpiringSoon++
			d.ExpiringValue = d.ExpiringValue.Add(b.QtyRemaining.Mul(b.CostPrice))
		}
	}

	if wantsCSV(c) {
		out := make([][]string, 0, len(rows))
		for _, b := range rows {
			expiry := ""
			if b.ExpiryDate != nil {
				expiry = b.ExpiryDate.Format("2006-01-02")
			}
			// "Own price" tells the reader whether this lot carries its own price
			// (and so can be picked at the till) or merely follows the product.
			ownPrice := "no"
			if b.SellingPrice.IsPositive() {
				ownPrice = "yes"
			}
			out = append(out, []string{
				b.ProductName, ptrStr(b.BatchNo), expiry, b.QtyRemaining.String(), b.UnitAbbr,
				csvMoney(b.CostPrice), csvMoney(b.EffectivePrice(b.ProductPrice)), ownPrice,
				csvMoney(b.QtyRemaining.Mul(b.CostPrice)),
			})
		}
		return writeCSV(c, "batches_"+time.Now().Format("2006-01-02"),
			[]string{"Product", "Batch", "Expiry", "Remaining", "Unit", "Cost", "Sells at", "Own price", "Value"}, out)
	}

	d.Page = pageParam(c)
	d.Rows = paginate(rows, d.Page, reportPageSize)
	return response.RenderPage(c, adminpages.BatchReport(d))
}

// RecipeVarianceReport compares what recipes say was consumed against what
// stock actually moved. A yield is an estimate ("this bag makes 50 cups"), so
// drift is expected; this is what makes the drift visible instead of letting it
// quietly bleed stock.
func (a *adminUI) RecipeVarianceReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, preset, err := rangeStrings(c)
	if err != nil {
		return err
	}
	rows, err := a.s.recipes.Variance(ctx, from, to)
	if err != nil {
		return err
	}
	if wantsCSV(c) {
		out := make([][]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, []string{
				r.ComponentName, r.UnitAbbr, r.Expected.String(), r.Actual.String(),
				r.Diff.String(), r.DriftPct().String(),
			})
		}
		return writeCSV(c, "recipe_variance_"+fromStr+"_"+toStr,
			[]string{"Ingredient", "Unit", "Expected", "Actual", "Difference", "Drift %"}, out)
	}
	page := pageParam(c)
	return response.RenderPage(c, adminpages.RecipeVarianceReport(adminpages.RecipeVarianceData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx),
		From: fromStr, To: toStr, Preset: preset,
		Rows:  paginate(rows, page, reportPageSize),
		Total: len(rows), Page: page, PageSize: reportPageSize,
	}))
}

// ServiceProfitReport answers "did this counter pay for itself" per service.
// The shop-wide P&L blends every service into one number, and no stock report
// includes them at all, so this is the only place the question can be asked.
func (a *adminUI) ServiceProfitReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, preset, err := rangeStrings(c)
	if err != nil {
		return err
	}
	rows, err := a.s.reports.ServiceProfit(ctx, from, to)
	if err != nil {
		return err
	}
	if wantsCSV(c) {
		out := make([][]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, []string{
				r.Name, r.Units.String(), csvMoney(r.Revenue), csvMoney(r.COGS),
				csvMoney(r.GrossProfit()), csvMoney(r.Expenses),
				csvMoney(r.NetProfit()), r.MarginPct().String(),
			})
		}
		return writeCSV(c, "service_profit_"+fromStr+"_"+toStr,
			[]string{"Service", "Sold", "Income", "Ingredients", "Gross", "Expenses", "Net profit", "Margin %"}, out)
	}
	return response.RenderPage(c, adminpages.ServiceProfitReport(adminpages.ServiceProfitData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx),
		From: fromStr, To: toStr, Preset: preset, Rows: rows,
	}))
}
