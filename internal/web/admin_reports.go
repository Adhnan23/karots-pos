package web

import (
	"context"
	"strconv"
	"time"

	"karots-pos/internal/features/products"
	"karots-pos/internal/features/reports"
	"karots-pos/internal/features/sales"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"
	adminpages "karots-pos/templates/pages/admin"

	"github.com/labstack/echo/v4"
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
	return response.RenderPage(c, adminpages.ReportsHub(adminpages.ReportsHubData{
		UserName: middleware.CurrentUserName(c),
	}))
}

func (a *adminUI) SalesReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, preset, err := rangeStrings(c)
	if err != nil {
		return err
	}
	status := c.QueryParam("status")
	method := c.QueryParam("method")
	filter := sales.ListFilter{From: &from, To: &to, Status: status, Method: method}

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
				})
			}
		}
		return writeCSV(c, "sales_"+fromStr+"_"+toStr,
			[]string{"Receipt", "Date", "Type", "Status", "Gross", "Discount", "Total"}, out)
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
		Rows:  rows,
		Count: sum.Count, Gross: sum.Gross, Discount: sum.Discount, Net: sum.Net,
		Page: page, PageSize: reportPageSize,
	}))
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
			{"Supplier recoveries", csvMoney(pl.Recoveries)},
			{"Other income (interest)", csvMoney(pl.OtherIncome)},
			{"Net profit", csvMoney(pl.NetProfit)},
			{"Sale tender (paid at sale)", csvMoney(pl.Received)},
			{"Receivables", csvMoney(pl.Receivables)},
			{"Payables", csvMoney(pl.Payables)},
		}
		return writeCSV(c, "finance_"+fromStr+"_"+toStr, []string{"Line", "Amount"}, out)
	}
	return response.RenderPage(c, adminpages.FinanceReport(adminpages.FinanceReportData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, Preset: preset, PL: *pl,
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
	for _, r := range rows {
		d.TotalRefund = d.TotalRefund.Add(r.RefundValue)
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
		Rows: rows, AllCats: allCats, SelCats: cats,
	}
	for _, r := range rows {
		d.TotalRevenue = d.TotalRevenue.Add(r.Revenue)
		d.TotalProfit = d.TotalProfit.Add(r.Profit)
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
		}
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
	all, err := a.s.purchases.List(ctx)
	if err != nil {
		return err
	}
	d := adminpages.PurchasesReportData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, Preset: preset,
		Page: pageParam(c), PageSize: reportPageSize,
	}
	inRange := all[:0:0]
	for _, p := range all {
		if p.CreatedAt.Before(from) || !p.CreatedAt.Before(to) {
			continue
		}
		inRange = append(inRange, p)
		d.Total = d.Total.Add(p.Total)
		d.Paid = d.Paid.Add(p.PaidAmount)
		d.Due = d.Due.Add(p.Total.Sub(p.PaidAmount))
	}
	d.Count = len(inRange)
	d.Rows = paginate(inRange, d.Page, reportPageSize)
	return response.RenderPage(c, adminpages.PurchasesReport(d))
}

func (a *adminUI) SuppliersReport(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.suppliers.List(ctx, "")
	if err != nil {
		return err
	}
	d := adminpages.SuppliersReportData{ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), Rows: rows}
	for _, s := range rows {
		d.TotalPayable = d.TotalPayable.Add(s.OutstandingBalance)
	}
	return response.RenderPage(c, adminpages.SuppliersReport(d))
}

func (a *adminUI) CustomerDuesReport(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.customers.Owing(ctx)
	if err != nil {
		return err
	}
	d := adminpages.CustomerDuesData{ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), Rows: rows}
	for _, r := range rows {
		d.TotalDue = d.TotalDue.Add(r.OutstandingBalance)
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
			})
		}
		return writeCSV(c, "customer_dues",
			[]string{"Customer", "Phone", "Credit limit", "Outstanding", "Oldest credit"}, out)
	}
	return response.RenderPage(c, adminpages.CustomerDuesReport(d))
}

func (a *adminUI) SupplierDuesReport(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.suppliers.Owing(ctx)
	if err != nil {
		return err
	}
	d := adminpages.SupplierDuesData{ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), Rows: rows}
	for _, r := range rows {
		d.TotalDue = d.TotalDue.Add(r.OutstandingBalance)
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
			})
		}
		return writeCSV(c, "supplier_dues",
			[]string{"Supplier", "Phone", "Credit days", "Outstanding", "Oldest unpaid"}, out)
	}
	return response.RenderPage(c, adminpages.SupplierDuesReport(d))
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
			out = append(out, []string{
				b.ProductName, ptrStr(b.BatchNo), expiry, b.QtyRemaining.String(), b.UnitAbbr,
				csvMoney(b.CostPrice), csvMoney(b.QtyRemaining.Mul(b.CostPrice)),
			})
		}
		return writeCSV(c, "batches_"+time.Now().Format("2006-01-02"),
			[]string{"Product", "Batch", "Expiry", "Remaining", "Unit", "Cost", "Value"}, out)
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
