package web

import (
	"strconv"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/auth"
	"karots-pos/internal/features/categories"
	"karots-pos/internal/features/conversions"
	"karots-pos/internal/features/customers"
	"karots-pos/internal/features/expenses"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/reports"
	"karots-pos/internal/features/sales"
	"karots-pos/internal/features/stock"
	"karots-pos/internal/features/suppliers"
	"karots-pos/internal/features/units"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"
	adminpages "karots-pos/templates/pages/admin"

	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

// ============================ Suppliers ============================

func (a *adminUI) Suppliers(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.suppliers.List(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.SuppliersPage(adminpages.SuppliersData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Rows:     rows,
	}))
}

func (a *adminUI) SuppliersTable(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.suppliers.List(ctx)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.SupplierRows(rows, a.symbol(ctx)))
}

func (a *adminUI) SupplierForm(c echo.Context) error {
	ctx := c.Request().Context()
	var s *suppliers.Supplier
	if idStr := c.Param("id"); idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return apperr.BadRequest("invalid id")
		}
		if s, err = a.s.suppliers.Get(ctx, id); err != nil {
			return err
		}
	}
	return response.RenderFragment(c, adminpages.SupplierForm(s))
}

func (a *adminUI) SupplierPayForm(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	s, err := a.s.suppliers.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.SupplierPaymentForm(*s, a.symbol(c.Request().Context())))
}

func (a *adminUI) SupplierCreate(c echo.Context) error {
	var in suppliers.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if _, err := a.s.suppliers.Create(c.Request().Context(), in); err != nil {
		return err
	}
	return htmxDone(c, "Supplier created", "reload-suppliers")
}

func (a *adminUI) SupplierUpdate(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in suppliers.UpdateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := a.s.suppliers.Update(c.Request().Context(), id, in); err != nil {
		return err
	}
	return htmxDone(c, "Supplier updated", "reload-suppliers")
}

func (a *adminUI) SupplierDelete(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.suppliers.Delete(c.Request().Context(), id); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionDelete, "supplier", strconv.FormatInt(id, 10), "")
	return htmxReload(c, "Supplier removed", "reload-suppliers")
}

func (a *adminUI) SupplierPay(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.suppliers.RecordPayment(c.Request().Context(), id, c.FormValue("amount")); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionPayment, "supplier", strconv.FormatInt(id, 10), "paid "+c.FormValue("amount"))
	return htmxDone(c, "Payment recorded", "reload-suppliers")
}

// ============================ Purchases (GRN) ============================

func (a *adminUI) Purchases(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.purchases.List(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.PurchasesPage(adminpages.PurchasesData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Rows:     rows,
	}))
}

func (a *adminUI) PurchaseDetail(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	detail, err := a.s.purchases.Get(ctx, id)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.PurchaseDetailPage(adminpages.PurchaseDetailData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Detail:   *detail,
	}))
}

func (a *adminUI) PurchaseEntry(c echo.Context) error {
	ctx := c.Request().Context()
	sups, err := a.s.suppliers.List(ctx)
	if err != nil {
		return err
	}
	prods, _, err := a.s.products.List(ctx, products.ListQuery{Limit: 500})
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.PurchaseEntryPage(adminpages.PurchaseEntryData{
		UserName:  middleware.CurrentUserName(c),
		Symbol:    a.symbol(ctx),
		Suppliers: sups,
		Products:  prods,
	}))
}

// ============================ Purchase returns ============================

func (a *adminUI) PurchaseReturns(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.purchaseReturns.List(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.PurchaseReturnsPage(adminpages.PurchaseReturnsData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Rows:     rows,
	}))
}

func (a *adminUI) PurchaseReturnEntry(c echo.Context) error {
	ctx := c.Request().Context()
	sups, err := a.s.suppliers.List(ctx)
	if err != nil {
		return err
	}
	prods, _, err := a.s.products.List(ctx, products.ListQuery{Limit: 500})
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.PurchaseReturnEntryPage(adminpages.PurchaseReturnEntryData{
		UserName:  middleware.CurrentUserName(c),
		Symbol:    a.symbol(ctx),
		Suppliers: sups,
		Products:  prods,
	}))
}

// ============================ Expenses ============================

func (a *adminUI) Expenses(c echo.Context) error {
	ctx := c.Request().Context()
	from, to := c.QueryParam("from"), c.QueryParam("to")
	f := expenses.Filter{}
	if t, ok := parseDate(from); ok {
		f.From = &t
	}
	if t, ok := parseDate(to); ok {
		f.To = &t
	}
	rows, err := a.s.expenses.List(ctx, f)
	if err != nil {
		return err
	}
	total := decimal.Zero
	for _, e := range rows {
		total = total.Add(e.Amount)
	}
	return response.RenderPage(c, adminpages.ExpensesPage(adminpages.ExpensesData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Rows:     rows,
		From:     from,
		To:       to,
		Total:    total,
	}))
}

func (a *adminUI) ExpenseForm(c echo.Context) error {
	return response.RenderFragment(c, adminpages.ExpenseForm())
}

func (a *adminUI) ExpenseDelete(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.expenses.Delete(c.Request().Context(), id); err != nil {
		return err
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Expense deleted", "success"))
	c.Response().Header().Set("HX-Refresh", "true")
	return c.NoContent(200)
}

func (a *adminUI) ExpenseCreate(c echo.Context) error {
	var in expenses.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if _, err := a.s.expenses.Create(c.Request().Context(), in, middleware.CurrentUserID(c)); err != nil {
		return err
	}
	c.Response().Header().Set("HX-Trigger", response.ToastAnd("Expense recorded", "success", "close-modal"))
	c.Response().Header().Set("HX-Refresh", "true")
	return c.NoContent(200)
}

// ============================ Finance / Profit ============================

func (a *adminUI) Finance(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, err := reports.ParseRange(c.QueryParam("from"), c.QueryParam("to"))
	if err != nil {
		return err
	}
	pl, err := a.s.reports.Compute(ctx, from, to)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.FinancePage(adminpages.FinanceData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		From:     c.QueryParam("from"),
		To:       c.QueryParam("to"),
		PL:       *pl,
	}))
}

// ============================ Categories ============================

func (a *adminUI) Categories(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.categories.Tree(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.CategoriesPage(adminpages.CategoriesData{
		UserName: middleware.CurrentUserName(c),
		Rows:     rows,
	}))
}

func (a *adminUI) CategoriesTable(c echo.Context) error {
	rows, err := a.s.categories.Tree(c.Request().Context())
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.CategoryRows(rows))
}

func (a *adminUI) CategoryForm(c echo.Context) error {
	ctx := c.Request().Context()
	all, err := a.s.categories.List(ctx)
	if err != nil {
		return err
	}
	var cur *categories.Category
	if idStr := c.Param("id"); idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return apperr.BadRequest("invalid id")
		}
		if cur, err = a.s.categories.Get(ctx, id); err != nil {
			return err
		}
	}
	return response.RenderFragment(c, adminpages.CategoryForm(all, cur))
}

func (a *adminUI) CategoryCreate(c echo.Context) error {
	var in categories.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	in.ParentID = normalizeParent(in.ParentID)
	if err := c.Validate(&in); err != nil {
		return err
	}
	if _, err := a.s.categories.Create(c.Request().Context(), in); err != nil {
		return err
	}
	return htmxDone(c, "Category created", "reload-categories")
}

func (a *adminUI) CategoryUpdate(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in categories.UpdateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	in.ParentID = normalizeParent(in.ParentID)
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := a.s.categories.Update(c.Request().Context(), id, in); err != nil {
		return err
	}
	return htmxDone(c, "Category updated", "reload-categories")
}

func (a *adminUI) CategoryDelete(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.categories.Delete(c.Request().Context(), id); err != nil {
		return err
	}
	return htmxReload(c, "Category deleted", "reload-categories")
}

// normalizeParent turns an empty/zero "parent_id" form value into nil (top-level).
func normalizeParent(p *int64) *int64 {
	if p == nil || *p == 0 {
		return nil
	}
	return p
}

// ============================ Barcode labels ============================

func (a *adminUI) Labels(c echo.Context) error {
	ctx := c.Request().Context()
	prods, _, err := a.s.products.List(ctx, products.ListQuery{Limit: 500})
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.LabelsPage(adminpages.LabelsData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Products: prods,
	}))
}

func (a *adminUI) LabelsPrint(c echo.Context) error {
	ctx := c.Request().Context()
	cfg, err := a.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	count := 12
	if n, err := strconv.Atoi(c.QueryParam("qty")); err == nil && n > 0 {
		count = min(n, 200)
	}
	showPrice := c.QueryParam("show_price") == "1"

	// Custom barcode: an arbitrary value typed by the user (no product record).
	if c.QueryParam("custom") == "1" {
		code := strings.TrimSpace(c.QueryParam("code"))
		if code == "" {
			return apperr.BadRequest("enter a barcode value")
		}
		format := strings.TrimSpace(c.QueryParam("format"))
		if format == "" {
			format = "CODE128"
		}
		priceText := ""
		if p := strings.TrimSpace(c.QueryParam("price")); p != "" {
			priceText = cfg.CurrencySymbol + " " + p
		}
		return response.RenderPage(c, adminpages.LabelSheet(adminpages.LabelSheetData{
			ShopName:  cfg.ShopName,
			Name:      strings.TrimSpace(c.QueryParam("text")),
			Code:      code,
			PriceText: priceText,
			ShowPrice: showPrice && priceText != "",
			Count:     count,
			Format:    format,
		}))
	}

	// From a product.
	id, err := strconv.ParseInt(c.QueryParam("product_id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("select a product")
	}
	p, err := a.s.products.Get(ctx, id)
	if err != nil {
		return err
	}
	code := "SKU" + strconv.FormatInt(p.ID, 10)
	if p.Barcode != nil && *p.Barcode != "" {
		code = *p.Barcode
	}
	return response.RenderPage(c, adminpages.LabelSheet(adminpages.LabelSheetData{
		ShopName:  cfg.ShopName,
		Name:      p.Name,
		Code:      code,
		PriceText: money.Format(cfg.CurrencySymbol, p.SellingPrice),
		ShowPrice: showPrice,
		Count:     count,
		Format:    "CODE128",
	}))
}

// ============================ Conversions ============================

func (a *adminUI) Conversions(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.conversions.List(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.ConversionsPage(adminpages.ConversionsData{
		UserName: middleware.CurrentUserName(c),
		Rows:     rows,
	}))
}

func (a *adminUI) ConversionsTable(c echo.Context) error {
	rows, err := a.s.conversions.List(c.Request().Context())
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.ConversionRows(rows))
}

func (a *adminUI) ConversionForm(c echo.Context) error {
	prods, _, err := a.s.products.List(c.Request().Context(), products.ListQuery{Limit: 500})
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.ConversionForm(prods))
}

func (a *adminUI) ConversionRunForm(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	cv, err := a.s.conversions.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.ConversionRunForm(*cv))
}

func (a *adminUI) ConversionCreate(c echo.Context) error {
	var in conversions.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if _, err := a.s.conversions.Create(c.Request().Context(), in); err != nil {
		return err
	}
	return htmxDone(c, "Conversion created", "reload-conversions")
}

func (a *adminUI) ConversionDelete(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.conversions.Delete(c.Request().Context(), id); err != nil {
		return err
	}
	return htmxReload(c, "Conversion removed", "reload-conversions")
}

func (a *adminUI) ConversionRun(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	qty, err := money.Parse(c.FormValue("quantity"))
	if err != nil {
		return apperr.Validation("quantity is invalid")
	}
	if err := a.s.conversions.Run(c.Request().Context(), id, qty, middleware.CurrentUserID(c)); err != nil {
		return err
	}
	// Stock changed on two products; refresh several lists.
	c.Response().Header().Set("HX-Trigger", response.ToastAnd("Conversion done", "success", "reload-conversions", "close-modal"))
	return c.NoContent(200)
}

// ============================ Units ============================

func (a *adminUI) Units(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.units.List(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.UnitsPage(adminpages.UnitsData{
		UserName: middleware.CurrentUserName(c),
		Rows:     rows,
	}))
}

func (a *adminUI) UnitsTable(c echo.Context) error {
	rows, err := a.s.units.List(c.Request().Context())
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.UnitRows(rows))
}

func (a *adminUI) UnitForm(c echo.Context) error {
	ctx := c.Request().Context()
	var cur *units.Unit
	if idStr := c.Param("id"); idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return apperr.BadRequest("invalid id")
		}
		if cur, err = a.s.units.Get(ctx, id); err != nil {
			return err
		}
	}
	return response.RenderFragment(c, adminpages.UnitForm(cur))
}

func (a *adminUI) UnitCreate(c echo.Context) error {
	var in units.Input
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if _, err := a.s.units.Create(c.Request().Context(), in); err != nil {
		return err
	}
	return htmxDone(c, "Unit created", "reload-units-mgmt")
}

func (a *adminUI) UnitUpdate(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in units.Input
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := a.s.units.Update(c.Request().Context(), id, in); err != nil {
		return err
	}
	return htmxDone(c, "Unit updated", "reload-units-mgmt")
}

func (a *adminUI) UnitDelete(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.units.Delete(c.Request().Context(), id); err != nil {
		return err
	}
	return htmxReload(c, "Unit deleted", "reload-units-mgmt")
}

// ============================ Batches (per product) ============================

func (a *adminUI) BatchesView(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	p, err := a.s.products.Get(ctx, id)
	if err != nil {
		return err
	}
	batches, err := a.s.stock.Batches(ctx, id)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.BatchesModal(*p, batches, a.symbol(ctx)))
}

// ============================ Reports: expiring & low-stock ============================

func (a *adminUI) ExpiringReport(c echo.Context) error {
	ctx := c.Request().Context()
	days := 30
	if v := c.QueryParam("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			days = n
		}
	}
	rows, err := a.s.stock.Expiring(ctx, days)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.ExpiringPage(adminpages.ExpiringData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Days:     days,
		Rows:     rows,
	}))
}

func (a *adminUI) LowStockReport(c echo.Context) error {
	ctx := c.Request().Context()
	rows, _, err := a.s.products.List(ctx, products.ListQuery{LowStock: true, Limit: 200})
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.LowStockPage(adminpages.LowStockData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Rows:     rows,
	}))
}

// ============================ Staff Users ============================

func (a *adminUI) Users(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.auth.ListUsers(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.UsersPage(adminpages.UsersData{
		UserName: middleware.CurrentUserName(c),
		Rows:     rows,
	}))
}

func (a *adminUI) UsersTable(c echo.Context) error {
	rows, err := a.s.auth.ListUsers(c.Request().Context())
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.UserRows(rows))
}

func (a *adminUI) UserForm(c echo.Context) error {
	return response.RenderFragment(c, adminpages.UserForm())
}

func (a *adminUI) UserCreate(c echo.Context) error {
	var in auth.CreateUserInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if _, err := a.s.auth.CreateUser(c.Request().Context(), in); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionCreate, "user", "", "created user "+in.Name+" ("+in.Role+")")
	return htmxDone(c, "User created", "reload-users")
}

func (a *adminUI) UserDeactivate(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.auth.DeactivateUser(c.Request().Context(), id); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionDelete, "user", strconv.FormatInt(id, 10), "deactivated user")
	return htmxReload(c, "User deactivated", "reload-users")
}

func (a *adminUI) UserReactivate(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.auth.ReactivateUser(c.Request().Context(), id); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionUpdate, "user", strconv.FormatInt(id, 10), "reactivated user")
	return htmxReload(c, "User reactivated", "reload-users")
}

// ============================ Damage write-off ============================

func (a *adminUI) DamageForm(c echo.Context) error {
	prods, _, err := a.s.products.List(c.Request().Context(), products.ListQuery{Limit: 200})
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.DamageForm(prods))
}

func (a *adminUI) DamageRecord(c echo.Context) error {
	var in stock.DamageInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := a.s.stock.Damage(c.Request().Context(), in, middleware.CurrentUserID(c)); err != nil {
		return err
	}
	return htmxDone(c, "Damage written off", "reload-stock")
}

// ============================ Customer edit / pay ============================

func (a *adminUI) CustomerEditForm(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	cust, err := a.s.customers.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.CustomerEditForm(*cust))
}

func (a *adminUI) CustomerPayForm(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	cust, err := a.s.customers.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.CustomerPaymentForm(*cust, a.symbol(c.Request().Context())))
}

func (a *adminUI) CustomerUpdate(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in customers.UpdateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := a.s.customers.Update(c.Request().Context(), id, in); err != nil {
		return err
	}
	return htmxDone(c, "Customer updated", "reload-customers")
}

func (a *adminUI) CustomerPay(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in customers.PaymentInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := a.s.customers.RecordPayment(c.Request().Context(), id, in); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionPayment, "customer", strconv.FormatInt(id, 10), "credit payment "+in.Amount)
	return htmxDone(c, "Payment recorded", "reload-customers")
}

// ============================ Sale return ============================

func (a *adminUI) SaleReturn(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if _, err := a.s.sales.Return(c.Request().Context(), id, middleware.CurrentUserID(c)); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionReturn, "sale", strconv.FormatInt(id, 10), "full return")
	return htmxReload(c, "Sale returned & restocked", "reload-sales")
}

// SaleReturnForm renders the per-line partial-return modal for a sale.
func (a *adminUI) SaleReturnForm(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	detail, err := a.s.sales.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.SaleReturnForm(*detail))
}

// SalesTable renders the list region (table + pager), honoring filters + page.
func (a *adminUI) SalesTable(c echo.Context) error {
	d, err := a.salesData(c)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.SalesList(d))
}

func salesFilterFromQuery(c echo.Context) sales.ListFilter {
	f := sales.ListFilter{Limit: 200, Status: c.QueryParam("status")}
	if t, ok := parseDate(c.QueryParam("from")); ok {
		f.From = &t
	}
	if t, ok := parseDate(c.QueryParam("to")); ok {
		// make `to` inclusive of the whole day
		end := t.AddDate(0, 0, 1)
		f.To = &end
	}
	return f
}

// parseDate parses a YYYY-MM-DD string; ok is false when empty/invalid.
func parseDate(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
