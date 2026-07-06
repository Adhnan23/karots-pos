package documents

import (
	"context"
	"strconv"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/cashregister"
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

// LabourData lists custom jobs awaiting a labour decision + the open tills a
// payout's cash can be taken from.
type LabourData struct {
	UserName string
	Symbol   string
	Rows     []UnpaidJob
	Tills    []cashregister.SessionRow
	History  []LabourPayment
}

func (a *adminUI) Labour(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.p.store.UnpaidJobs(ctx)
	if err != nil {
		return err
	}
	tills, err := a.p.core.CashRegister.OpenSessions(ctx)
	if err != nil {
		return err
	}
	hist, err := a.p.store.LabourHistory(ctx, 100)
	if err != nil {
		return err
	}
	return response.RenderPage(c, LabourPage(LabourData{
		UserName: middleware.CurrentUserName(c), Symbol: a.symbol(ctx),
		Rows: rows, Tills: tills, History: hist,
	}))
}

// PayJob settles one custom job's labour: it books a "Labour" expense and, when
// the cash comes "from a till", also records that drawer's withdrawal (done first
// so a drawer-overdraw aborts before the expense is booked). The job is then
// stamped paid and leaves the worklist.
func (a *adminUI) PayJob(c echo.Context) error {
	ctx := c.Request().Context()
	jobID, _ := strconv.ParseInt(c.Param("job"), 10, 64)
	job, err := a.p.store.UnpaidJob(ctx, jobID)
	if err != nil {
		return err
	}
	if job == nil {
		return apperr.Validation("this job is already settled")
	}
	amt, err := decimal.NewFromString(strings.TrimSpace(c.FormValue("amount")))
	if err != nil || !amt.IsPositive() {
		return apperr.Validation("enter a valid amount")
	}
	note := strings.TrimSpace(c.FormValue("note"))

	in := SettleInput{JobID: jobID, Amount: amt, Note: note, Source: "external"}

	if c.FormValue("source") == "till" {
		tillUID, err := strconv.ParseInt(c.FormValue("till_user_id"), 10, 64)
		if err != nil || tillUID == 0 {
			return apperr.Validation("choose which till the cash came from")
		}
		reason := "Labour - " + job.ServiceName
		if note != "" {
			reason += " (" + note + ")"
		}
		if _, err := a.p.core.CashRegister.Withdraw(ctx, tillUID, cashregister.MovementInput{Amount: amt.String(), Reason: reason}); err != nil {
			return err
		}
		in.Source = "till"
		in.TillUID = &tillUID
	}

	desc := "Documents labour - " + job.ServiceName
	exp, err := a.p.core.Expenses.Create(ctx, expenses.CreateInput{
		Category: "Labour", Amount: amt.StringFixed(2), Description: &desc,
		ExpenseDate: time.Now().Format("2006-01-02"),
	}, middleware.CurrentUserID(c))
	if err == nil && exp != nil {
		in.ExpenseID = exp.ID
	}
	if err := a.p.store.SettleJob(ctx, in); err != nil {
		return err
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Labour paid", "success"))
	c.Response().Header().Set("HX-Refresh", "true")
	return c.NoContent(200)
}

// DismissJob clears a custom job from the worklist without paying labour (no
// expense, no drawer movement) — for jobs that needed no payout.
func (a *adminUI) DismissJob(c echo.Context) error {
	ctx := c.Request().Context()
	jobID, _ := strconv.ParseInt(c.Param("job"), 10, 64)
	job, err := a.p.store.UnpaidJob(ctx, jobID)
	if err != nil {
		return err
	}
	if job == nil {
		return apperr.Validation("this job is already settled")
	}
	if err := a.p.store.SettleJob(ctx, SettleInput{JobID: jobID, Amount: decimal.Zero, Source: "none"}); err != nil {
		return err
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Dismissed", "success"))
	c.Response().Header().Set("HX-Refresh", "true")
	return c.NoContent(200)
}

func (a *adminUI) symbol(ctx context.Context) string {
	if cfg, err := a.p.core.Settings.Get(ctx); err == nil && cfg != nil {
		return cfg.CurrencySymbol
	}
	return "Rs."
}
