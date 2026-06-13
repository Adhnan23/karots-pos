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

// rangeStrings resolves the from/to query params into the period (with `to`
// exclusive of the end day) plus the user-facing date strings for the form.
func rangeStrings(c echo.Context) (from, to time.Time, fromStr, toStr string, err error) {
	fromQ, toQ := c.QueryParam("from"), c.QueryParam("to")
	from, to, err = reports.ParseRange(fromQ, toQ)
	if err != nil {
		return
	}
	fromStr = fromQ
	if fromStr == "" {
		fromStr = from.Format("2006-01-02")
	}
	toStr = toQ
	if toStr == "" {
		toStr = to.AddDate(0, 0, -1).Format("2006-01-02")
	}
	return
}

func (a *adminUI) ReportsHub(c echo.Context) error {
	return response.RenderPage(c, adminpages.ReportsHub(adminpages.ReportsHubData{
		UserName: middleware.CurrentUserName(c),
	}))
}

func (a *adminUI) SalesReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := rangeStrings(c)
	if err != nil {
		return err
	}
	status := c.QueryParam("status")
	rows, err := a.s.sales.List(ctx, sales.ListFilter{From: &from, To: &to, Status: status, Limit: 10000})
	if err != nil {
		return err
	}
	d := adminpages.SalesReportData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx),
		From: fromStr, To: toStr, Status: status, Rows: rows, Count: len(rows),
	}
	for _, s := range rows {
		d.Gross = d.Gross.Add(s.Subtotal)
		d.Discount = d.Discount.Add(s.Discount)
		d.Net = d.Net.Add(s.Total)
	}
	return response.RenderPage(c, adminpages.SalesReport(d))
}

func (a *adminUI) FinanceReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := rangeStrings(c)
	if err != nil {
		return err
	}
	pl, err := a.s.reports.Compute(ctx, from, to)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.FinanceReport(adminpages.FinanceReportData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, PL: *pl,
	}))
}

func (a *adminUI) ReturnsReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := rangeStrings(c)
	if err != nil {
		return err
	}
	rows, err := a.s.reports.Returns(ctx, from, to)
	if err != nil {
		return err
	}
	d := adminpages.ReturnsReportData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, Rows: rows,
	}
	for _, r := range rows {
		d.TotalRefund = d.TotalRefund.Add(r.RefundValue)
	}
	return response.RenderPage(c, adminpages.ReturnsReport(d))
}

func (a *adminUI) ProfitByCategoryReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := rangeStrings(c)
	if err != nil {
		return err
	}
	rows, err := a.s.reports.ProfitByCategory(ctx, from, to)
	if err != nil {
		return err
	}
	d := adminpages.CategoryProfitData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, Rows: rows,
	}
	for _, r := range rows {
		d.TotalRevenue = d.TotalRevenue.Add(r.Revenue)
		d.TotalProfit = d.TotalProfit.Add(r.Profit)
	}
	return response.RenderPage(c, adminpages.ProfitByCategoryReport(d))
}

func (a *adminUI) SalesTrendReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := rangeStrings(c)
	if err != nil {
		return err
	}
	rows, err := a.s.reports.DailySales(ctx, from, to)
	if err != nil {
		return err
	}
	d := adminpages.SalesTrendData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, Rows: rows,
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
	from, to, fromStr, toStr, err := rangeStrings(c)
	if err != nil {
		return err
	}
	sum, err := a.s.reports.WarrantyAndRecovery(ctx, from, to)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.WarrantyReport(adminpages.WarrantyReportData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, Summary: *sum,
	}))
}

func (a *adminUI) CashRegisterReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := rangeStrings(c)
	if err != nil {
		return err
	}
	rows, err := a.s.cashRegister.SessionsInRange(ctx, from, to)
	if err != nil {
		return err
	}
	d := adminpages.CashReportData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr, Rows: rows,
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
	from, to, fromStr, toStr, err := rangeStrings(c)
	if err != nil {
		return err
	}
	all, err := a.s.purchases.List(ctx)
	if err != nil {
		return err
	}
	d := adminpages.PurchasesReportData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), From: fromStr, To: toStr,
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
	return response.RenderPage(c, adminpages.SupplierDuesReport(d))
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
