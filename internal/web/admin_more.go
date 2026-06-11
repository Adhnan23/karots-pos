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
	"karots-pos/internal/features/supplierpay"
	"karots-pos/internal/features/suppliers"
	"karots-pos/internal/features/units"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/printing"
	"karots-pos/internal/response"
	"karots-pos/internal/tspl"
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
	ctx := c.Request().Context()
	s, err := a.s.suppliers.Get(ctx, id)
	if err != nil {
		return err
	}
	invoices, err := a.s.supplierPay.OpenInvoices(ctx, id)
	if err != nil {
		return err
	}
	history, err := a.s.supplierPay.History(ctx, id)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.SupplierPaymentForm(adminpages.SupplierPayData{
		Supplier: *s,
		Invoices: invoices,
		History:  history,
		Symbol:   a.symbol(ctx),
	}))
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
	ctx := c.Request().Context()

	invoices, err := a.s.supplierPay.OpenInvoices(ctx, id)
	if err != nil {
		return err
	}

	in := supplierpay.PayInput{
		Method:    c.FormValue("method"),
		Reference: strings.TrimSpace(c.FormValue("reference")),
		Note:      strings.TrimSpace(c.FormValue("note")),
	}
	// Read the per-invoice allocation inputs the form rendered (alloc_<id>).
	for _, pu := range invoices {
		raw := strings.TrimSpace(c.FormValue("alloc_" + strconv.FormatInt(pu.ID, 10)))
		if raw == "" {
			continue
		}
		amt, perr := money.Parse(raw)
		if perr != nil || amt.IsNegative() {
			return apperr.Validation("invalid allocation amount")
		}
		if amt.IsZero() {
			continue
		}
		in.Allocations = append(in.Allocations, supplierpay.Alloc{PurchaseID: pu.ID, Amount: amt})
	}
	// Fallback for a supplier carrying a balance with no open invoices: a plain
	// unallocated amount that just reduces the payable.
	if len(invoices) == 0 {
		if raw := strings.TrimSpace(c.FormValue("amount")); raw != "" {
			amt, perr := money.Parse(raw)
			if perr != nil || amt.IsNegative() {
				return apperr.Validation("invalid amount")
			}
			in.Unallocated = amt
		}
	}

	res, err := a.s.supplierPay.Pay(ctx, id, in, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}

	// Cash paid to a supplier leaves the cashier's drawer (no-op without an open
	// session), mirroring how collected customer credit enters it.
	if res.Method == "cash" {
		name := ""
		if sup, gerr := a.s.suppliers.Get(ctx, id); gerr == nil {
			name = sup.Name
		}
		a.s.cashRegister.RecordSupplierCash(ctx, middleware.CurrentUserID(c), res.Total, "supplier paid: "+name)
	}
	a.s.logAudit(c, audit.ActionPayment, "supplier", strconv.FormatInt(id, 10),
		"paid "+money.Display(res.Total)+" ("+res.Method+")")
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

// labelReq is the resolved label content shared by the browser sheet
// (LabelsPrint) and the direct TSPL print (LabelsSend).
type labelReq struct {
	ShopName, Name, Code, Format, PriceText string
	ShowPrice                               bool
	Count                                   int
}

// parseLabelReq reads the label form (works for GET query params and POST form
// bodies) and resolves it to a product label or a custom barcode label.
func (s *Server) parseLabelReq(c echo.Context, shopName, symbol string) (labelReq, error) {
	count := 12
	if n, err := strconv.Atoi(c.FormValue("qty")); err == nil && n > 0 {
		count = min(n, 200)
	}
	showPrice := c.FormValue("show_price") == "1"

	// Custom barcode: an arbitrary value typed by the user (no product record).
	if c.FormValue("custom") == "1" {
		code := strings.TrimSpace(c.FormValue("code"))
		if code == "" {
			return labelReq{}, apperr.BadRequest("enter a barcode value")
		}
		format := strings.TrimSpace(c.FormValue("format"))
		if format == "" {
			format = "CODE128"
		}
		priceText := ""
		if p := strings.TrimSpace(c.FormValue("price")); p != "" {
			priceText = symbol + " " + p
		}
		return labelReq{
			ShopName:  shopName,
			Name:      strings.TrimSpace(c.FormValue("text")),
			Code:      code,
			Format:    format,
			PriceText: priceText,
			ShowPrice: showPrice && priceText != "",
			Count:     count,
		}, nil
	}

	// From a product.
	id, err := strconv.ParseInt(c.FormValue("product_id"), 10, 64)
	if err != nil {
		return labelReq{}, apperr.BadRequest("select a product")
	}
	p, err := s.products.Get(c.Request().Context(), id)
	if err != nil {
		return labelReq{}, err
	}
	code := "SKU" + strconv.FormatInt(p.ID, 10)
	if p.Barcode != nil && *p.Barcode != "" {
		code = *p.Barcode
	}
	return labelReq{
		ShopName:  shopName,
		Name:      p.Name,
		Code:      code,
		Format:    "CODE128",
		PriceText: money.Format(symbol, p.SellingPrice),
		ShowPrice: showPrice,
		Count:     count,
	}, nil
}

// resolveLabelSize picks the sticker dimensions for this print: a "WxH" preset,
// a "custom" size from the label_w/label_h/label_gap inputs, or the shop's saved
// default. All values are clamped to sane millimetre bounds.
func resolveLabelSize(c echo.Context, defW, defH, defGap int) (w, h, gap int) {
	w, h, gap = defW, defH, defGap
	switch size := strings.TrimSpace(strings.ToLower(c.FormValue("label_size"))); size {
	case "", "default":
		// keep the saved default
	case "custom":
		if v, err := strconv.Atoi(c.FormValue("label_w")); err == nil && v > 0 {
			w = v
		}
		if v, err := strconv.Atoi(c.FormValue("label_h")); err == nil && v > 0 {
			h = v
		}
		if v, err := strconv.Atoi(c.FormValue("label_gap")); err == nil && v >= 0 {
			gap = v
		}
	default: // "WxH" preset, e.g. "40x30"
		if parts := strings.SplitN(size, "x", 2); len(parts) == 2 {
			pw, e1 := strconv.Atoi(strings.TrimSpace(parts[0]))
			ph, e2 := strconv.Atoi(strings.TrimSpace(parts[1]))
			if e1 == nil && e2 == nil && pw > 0 && ph > 0 {
				w, h = pw, ph
			}
		}
	}
	return min(max(w, 10), 200), min(max(h, 10), 200), min(max(gap, 0), 20)
}

func (a *adminUI) LabelsPrint(c echo.Context) error {
	cfg, err := a.s.settings.Get(c.Request().Context())
	if err != nil {
		return err
	}
	req, err := a.s.parseLabelReq(c, cfg.ShopName, cfg.CurrencySymbol)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.LabelSheet(adminpages.LabelSheetData{
		ShopName:  req.ShopName,
		Name:      req.Name,
		Code:      req.Code,
		PriceText: req.PriceText,
		ShowPrice: req.ShowPrice,
		Count:     req.Count,
		Format:    req.Format,
	}))
}

// sendLabel renders the label as TSPL and sends it raw to the configured label
// printer — bypassing the browser PDF path that a raw thermal queue mis-prints.
// Shared by the admin and cashier label printers. Mirrors cashier.PrintReceipt.
func (s *Server) sendLabel(c echo.Context) error {
	ctx := c.Request().Context()
	cfg, err := s.settings.Get(ctx)
	if err != nil {
		return err
	}
	req, err := s.parseLabelReq(c, cfg.ShopName, cfg.CurrencySymbol)
	if err != nil {
		return err
	}
	w, h, gap := resolveLabelSize(c, cfg.LabelWidthMM, cfg.LabelHeightMM, cfg.LabelGapMM)

	// Prefer the queue chosen in Settings; fall back to the LABEL_PRINTER env.
	queue := cfg.LabelPrinter
	if queue == "" {
		queue = s.cfg.LabelPrinter
	}
	doc := tspl.Document(tspl.Input{
		Name:      req.Name,
		Code:      req.Code,
		Format:    req.Format,
		PriceText: req.PriceText,
		ShowPrice: req.ShowPrice,
		Count:     req.Count,
		WidthMM:   w,
		HeightMM:  h,
		GapMM:     gap,
	})
	if err := printing.Raw(ctx, queue, doc); err != nil {
		return apperr.Internal("could not print labels", err)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Labels sent to printer", "success"))
	return response.OK(c, map[string]bool{"ok": true})
}

// LabelsSend (admin) prints a label directly to the configured label printer.
func (a *adminUI) LabelsSend(c echo.Context) error { return a.s.sendLabel(c) }

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
