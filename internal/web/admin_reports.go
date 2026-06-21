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
	rows, err := a.s.sales.List(ctx, sales.ListFilter{From: &from, To: &to, Status: status, Method: method, Limit: 10000})
	if err != nil {
		return err
	}
	d := adminpages.SalesReportData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx),
		From: fromStr, To: toStr, Preset: preset, Status: status, Method: method, Rows: rows, Count: len(rows),
	}
	for _, s := range rows {
		d.Gross = d.Gross.Add(s.Subtotal)
		d.Discount = d.Discount.Add(s.Discount)
		d.Net = d.Net.Add(s.Total)
	}
	if wantsCSV(c) {
		out := make([][]string, 0, len(rows))
		for _, s := range rows {
			out = append(out, []string{
				s.ReceiptNo, s.CreatedAt.Format("2006-01-02 15:04"), s.SaleType, s.Status,
				csvMoney(s.Discount), csvMoney(s.Total),
			})
		}
		return writeCSV(c, "sales_"+fromStr+"_"+toStr,
			[]string{"Receipt", "Date", "Type", "Status", "Discount", "Total"}, out)
	}
	return response.RenderPage(c, adminpages.SalesReport(d))
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
			{"Supplier recoveries", csvMoney(pl.Recoveries)},
			{"Net profit", csvMoney(pl.NetProfit)},
			{"Cash received", csvMoney(pl.Received)},
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
	d := adminpages.ReturnsReportData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, Preset: preset, Rows: rows,
	}
	for _, r := range rows {
		d.TotalRefund = d.TotalRefund.Add(r.RefundValue)
	}
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
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, Preset: preset, Rows: rows,
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
	}
	for _, p := range all {
		if p.CreatedAt.Before(from) || !p.CreatedAt.Before(to) {
			continue
		}
		d.Rows = append(d.Rows, p)
		d.Total = d.Total.Add(p.Total)
		d.Paid = d.Paid.Add(p.PaidAmount)
		d.Due = d.Due.Add(p.Total.Sub(p.PaidAmount))
	}
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

func (a *adminUI) InventoryReport(c echo.Context) error {
	ctx := c.Request().Context()
	rows, _, err := a.s.products.List(ctx, products.ListQuery{Limit: 10000})
	if err != nil {
		return err
	}
	d := adminpages.InventoryReportData{ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), Rows: rows}
	for _, p := range rows {
		d.CostValue = d.CostValue.Add(p.StockQty.Mul(p.CostPrice))
		d.RetailValue = d.RetailValue.Add(p.StockQty.Mul(p.SellingPrice))
	}
	return response.RenderPage(c, adminpages.InventoryReport(d))
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
	d := adminpages.BatchReportData{ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), Days: daysStr, Rows: rows}
	for _, b := range rows {
		d.TotalValue = d.TotalValue.Add(b.QtyRemaining.Mul(b.CostPrice))
	}
	return response.RenderPage(c, adminpages.BatchReport(d))
}
