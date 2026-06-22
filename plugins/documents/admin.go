package documents

import (
	"context"
	"strconv"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/expenses"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/reports"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"

	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

type adminUI struct{ p *Plugin }

// ServiceVM bundles a service with its price matrix + consumable mappings.
type ServiceVM struct {
	Service     Service
	Prices      []Price
	Consumables []Consumable
}

// HubData is the admin management page.
type HubData struct {
	UserName string
	Symbol   string
	Services []ServiceVM
	Products []products.Product // stock products for the consumable picker
}

func (a *adminUI) Hub(c echo.Context) error {
	ctx := c.Request().Context()
	svcs, err := a.p.store.Services(ctx, true)
	if err != nil {
		return err
	}
	vms := make([]ServiceVM, 0, len(svcs))
	for _, sv := range svcs {
		prices, _ := a.p.store.Prices(ctx, sv.ID)
		cons, _ := a.p.store.Consumables(ctx, sv.ID)
		vms = append(vms, ServiceVM{Service: sv, Prices: prices, Consumables: cons})
	}
	prods, _, err := a.p.core.Products.List(ctx, products.ListQuery{Limit: 1000})
	if err != nil {
		return err
	}
	return response.RenderPage(c, HubPage(HubData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Services: vms,
		Products: prods,
	}))
}

func (a *adminUI) ServiceCreate(c echo.Context) error {
	ctx := c.Request().Context()
	name := strings.TrimSpace(c.FormValue("name"))
	if name == "" {
		return apperr.Validation("service name is required")
	}
	kind := c.FormValue("kind")
	if kind != "custom" {
		kind = "metered"
	}
	category := c.FormValue("category")
	if category == "" {
		category = "other"
	}
	catID, unitID, err := a.p.store.serviceDefaults(ctx)
	if err != nil {
		return apperr.Internal("failed to resolve product defaults", err)
	}
	prod, err := a.p.core.Products.Create(ctx, products.CreateInput{
		Name: name, CategoryID: catID, UnitID: unitID,
		CostPrice: "0", SellingPrice: "0", WholesalePrice: "0", TaxRate: "0", IsService: true,
	})
	if err != nil {
		return err
	}
	if _, err := a.p.store.CreateService(ctx, name, kind, category, prod.ID); err != nil {
		return err
	}
	c.Response().Header().Set("HX-Refresh", "true")
	return c.NoContent(200)
}

func (a *adminUI) ServiceDelete(c echo.Context) error {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if err := a.p.store.DeactivateService(c.Request().Context(), id); err != nil {
		return err
	}
	c.Response().Header().Set("HX-Refresh", "true")
	return c.NoContent(200)
}

func (a *adminUI) PriceAdd(c echo.Context) error {
	ctx := c.Request().Context()
	sid, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	price, err := decimal.NewFromString(strings.TrimSpace(c.FormValue("unit_price")))
	if err != nil || price.IsNegative() {
		return apperr.Validation("unit price is invalid")
	}
	minQty, _ := strconv.Atoi(c.FormValue("min_qty"))
	if minQty < 1 {
		minQty = 1
	}
	var size *string
	if s := strings.TrimSpace(c.FormValue("size")); s != "" {
		size = &s
	}
	if err := a.p.store.AddPrice(ctx, Price{
		ServiceID: sid, Size: size, Color: truthy(c.FormValue("color")),
		DoubleSide: truthy(c.FormValue("double_side")), MinQty: minQty, UnitPrice: price,
	}); err != nil {
		return err
	}
	c.Response().Header().Set("HX-Refresh", "true")
	return c.NoContent(200)
}

func (a *adminUI) PriceDelete(c echo.Context) error {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if err := a.p.store.DeletePrice(c.Request().Context(), id); err != nil {
		return err
	}
	c.Response().Header().Set("HX-Refresh", "true")
	return c.NoContent(200)
}

func (a *adminUI) ConsumableAdd(c echo.Context) error {
	ctx := c.Request().Context()
	sid, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	pid, _ := strconv.ParseInt(c.FormValue("product_id"), 10, 64)
	if pid <= 0 {
		return apperr.Validation("pick a consumable product")
	}
	qpu, err := decimal.NewFromString(strings.TrimSpace(c.FormValue("qty_per_unit")))
	if err != nil || !qpu.IsPositive() {
		qpu = decimal.NewFromInt(1)
	}
	var size *string
	if s := strings.TrimSpace(c.FormValue("size")); s != "" {
		size = &s
	}
	if err := a.p.store.AddConsumable(ctx, Consumable{
		ServiceID: sid, Size: size, ProductID: pid, QtyPerUnit: qpu,
	}); err != nil {
		return err
	}
	c.Response().Header().Set("HX-Refresh", "true")
	return c.NoContent(200)
}

func (a *adminUI) ConsumableDelete(c echo.Context) error {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if err := a.p.store.DeleteConsumable(c.Request().Context(), id); err != nil {
		return err
	}
	c.Response().Header().Set("HX-Refresh", "true")
	return c.NoContent(200)
}

// ReportData is the documents report (revenue/profit by service + paper/labour).
type ReportData struct {
	UserName, Symbol, Preset, From, To string
	Rows                               []ServiceTotals
	Revenue, Consumables, Labour       decimal.Decimal
}

func (a *adminUI) Report(c echo.Context) error {
	ctx := c.Request().Context()
	preset := c.QueryParam("preset")
	from, to, fromStr, toStr, err := reports.ResolveRange(preset, c.QueryParam("from"), c.QueryParam("to"))
	if err != nil {
		return apperr.Validation(err.Error())
	}
	rows, err := a.p.store.ServiceTotals(ctx, from, to)
	if err != nil {
		return err
	}
	d := ReportData{
		UserName: middleware.CurrentUserName(c), Symbol: a.symbol(ctx),
		Preset: preset, From: fromStr, To: toStr, Rows: rows,
	}
	for _, r := range rows {
		d.Revenue = d.Revenue.Add(r.Revenue)
		d.Consumables = d.Consumables.Add(r.Consumables)
		d.Labour = d.Labour.Add(r.Labour)
	}
	return response.RenderPage(c, ReportPage(d))
}

// PayoutsData lists per-worker outstanding labour.
type PayoutsData struct {
	UserName string
	Symbol   string
	Rows     []WorkerBalance
}

func (a *adminUI) Payouts(c echo.Context) error {
	rows, err := a.p.store.WorkerBalances(c.Request().Context())
	if err != nil {
		return err
	}
	return response.RenderPage(c, PayoutsPage(PayoutsData{
		UserName: middleware.CurrentUserName(c), Symbol: a.symbol(c.Request().Context()), Rows: rows,
	}))
}

func (a *adminUI) PayWorker(c echo.Context) error {
	ctx := c.Request().Context()
	workerID, _ := strconv.ParseInt(c.Param("worker"), 10, 64)
	total, err := a.p.store.UnpaidTotal(ctx, workerID)
	if err != nil {
		return err
	}
	if !total.IsPositive() {
		return apperr.Validation("nothing to pay this worker")
	}
	name := a.p.store.WorkerName(ctx, workerID)
	desc := "Documents labour — " + name
	exp, err := a.p.core.Expenses.Create(ctx, expenses.CreateInput{
		Category: "Labour", Amount: total.StringFixed(2), Description: &desc,
		ExpenseDate: time.Now().Format("2006-01-02"),
	}, middleware.CurrentUserID(c))
	var expID int64
	if err == nil && exp != nil {
		expID = exp.ID
	}
	if _, _, err := a.p.store.SettleWorker(ctx, workerID, expID); err != nil {
		return err
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Paid "+name, "success"))
	c.Response().Header().Set("HX-Refresh", "true")
	return c.NoContent(200)
}

func (a *adminUI) symbol(ctx context.Context) string {
	if cfg, err := a.p.core.Settings.Get(ctx); err == nil && cfg != nil {
		return cfg.CurrencySymbol
	}
	return "Rs."
}
