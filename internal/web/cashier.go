package web

import (
	"context"
	"strconv"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/escpos"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/auth"
	"karots-pos/internal/features/customers"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/sales"
	"karots-pos/internal/features/stock"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"
	cashierpages "karots-pos/templates/pages/cashier"

	"github.com/labstack/echo/v4"
)

type cashierUI struct{ s *Server }

// cashierSymbol returns the configured currency symbol (falling back to "Rs.").
func (h *cashierUI) cashierSymbol(ctx context.Context) string {
	if cfg, err := h.s.settings.Get(ctx); err == nil {
		return cfg.CurrencySymbol
	}
	return "Rs."
}

func (h *cashierUI) cashierShopName(ctx context.Context) string {
	if cfg, err := h.s.settings.Get(ctx); err == nil && cfg.ShopName != "" {
		return cfg.ShopName
	}
	return "Shop"
}

// ZReport renders the printable day-end (Z) summary for a drawer session. A
// cashier may only view their own session; admins/managers may view any.
func (h *cashierUI) ZReport(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	sess, moves, err := h.s.cashRegister.SessionDetail(ctx, id)
	if err != nil {
		return err
	}
	role := middleware.CurrentRole(c)
	if role != auth.RoleAdmin && role != auth.RoleManager && sess.UserID != middleware.CurrentUserID(c) {
		return apperr.Forbidden("you can only print your own session")
	}
	to := time.Now()
	if sess.ClosedAt != nil {
		to = *sess.ClosedAt
	}
	sum, err := h.s.sales.PeriodSummary(ctx, sess.UserID, sess.OpenedAt, to)
	if err != nil {
		return err
	}
	return response.RenderPage(c, cashierpages.ZReport(cashierpages.ZReportData{
		ShopName:  h.cashierShopName(ctx),
		Symbol:    h.cashierSymbol(ctx),
		Session:   *sess,
		Movements: moves,
		Sales:     sum,
	}))
}

func (h *cashierUI) POS(c echo.Context) error {
	ctx := c.Request().Context()
	symbol, defaultType := "Rs.", "retail"
	if cfg, err := h.s.settings.Get(ctx); err == nil {
		symbol = cfg.CurrencySymbol
		defaultType = cfg.DefaultSaleType
	}
	return response.RenderPage(c, cashierpages.POS(cashierpages.POSData{
		CashierName:     middleware.CurrentUserName(c),
		Role:            middleware.CurrentRole(c),
		Symbol:          symbol,
		DefaultSaleType: defaultType,
	}))
}

// Receipt renders a printable thermal bill for a single sale. ?print=1 makes it
// auto-open the browser print dialog on load.
func (h *cashierUI) Receipt(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	detail, err := h.s.sales.Get(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := h.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	// Paper width defaults to the saved setting; an explicit ?size= overrides it
	// (used by the "Switch to 58/80mm" links on the receipt page).
	narrow := cfg.ReceiptWidth == "58"
	if sz := c.QueryParam("size"); sz != "" {
		narrow = sz == "58"
	}
	return response.RenderPage(c, cashierpages.Receipt(cashierpages.ReceiptData{
		Detail:    *detail,
		Settings:  *cfg,
		AutoPrint: c.QueryParam("print") == "1",
		Narrow:    narrow,
	}))
}

// PrintReceipt sends a sale straight to the thermal printer as ESC/POS bytes
// (built-in font, sized to the receipt_width setting, with an auto-cut). This is
// the reliable path for the Xprinter: it bypasses the browser/PDF route that a
// driverless raw queue prints as garbage.
func (h *cashierUI) PrintReceipt(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	detail, err := h.s.sales.Get(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := h.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	// Prefer the queue chosen in Settings; fall back to the RECEIPT_PRINTER env.
	queue := cfg.ReceiptPrinter
	if queue == "" {
		queue = h.s.cfg.ReceiptPrinter
	}
	if err := escpos.Send(ctx, queue, escpos.Document(*detail, *cfg)); err != nil {
		return apperr.Internal("could not print receipt", err)
	}
	// Feedback for the HTMX reprint button; the Alpine apiFetch path toasts itself.
	c.Response().Header().Set("HX-Trigger", response.Toast("Receipt sent to printer", "success"))
	return response.OK(c, map[string]bool{"ok": true})
}

// Receipts lists recent sales (optionally filtered by receipt number) so the
// cashier can reprint a bill from the terminal.
func (h *cashierUI) Receipts(c echo.Context) error {
	ctx := c.Request().Context()
	q := c.QueryParam("q")
	rows, err := h.s.sales.List(ctx, sales.ListFilter{Receipt: q, Limit: 50})
	if err != nil {
		return err
	}
	return response.RenderPage(c, cashierpages.Receipts(cashierpages.ReceiptsData{
		CashierName: middleware.CurrentUserName(c),
		Role:        middleware.CurrentRole(c),
		Symbol:      h.cashierSymbol(ctx),
		Query:       q,
		Sales:       rows,
	}))
}

// ============================ Barcode labels ============================

// Labels is the terminal's barcode-label printer (product or custom code),
// sending directly to the configured label printer.
func (h *cashierUI) Labels(c echo.Context) error {
	ctx := c.Request().Context()
	prods, _, err := h.s.products.List(ctx, products.ListQuery{Limit: 500})
	if err != nil {
		return err
	}
	return response.RenderPage(c, cashierpages.Labels(cashierpages.LabelsData{
		CashierName: middleware.CurrentUserName(c),
		Role:        middleware.CurrentRole(c),
		Symbol:      h.cashierSymbol(ctx),
		Products:    prods,
	}))
}

// LabelsSend prints a label directly to the configured label printer (shared
// renderer with the admin labels page).
func (h *cashierUI) LabelsSend(c echo.Context) error { return h.s.sendLabel(c) }

// ============================ Returns ============================

func (h *cashierUI) returnsData(c echo.Context) (cashierpages.ReturnsData, error) {
	ctx := c.Request().Context()
	rows, err := h.s.sales.List(ctx, sales.ListFilter{Limit: 50})
	if err != nil {
		return cashierpages.ReturnsData{}, err
	}
	return cashierpages.ReturnsData{
		CashierName: middleware.CurrentUserName(c),
		Role:        middleware.CurrentRole(c),
		Symbol:      h.cashierSymbol(ctx),
		Sales:       rows,
	}, nil
}

func (h *cashierUI) Returns(c echo.Context) error {
	d, err := h.returnsData(c)
	if err != nil {
		return err
	}
	return response.RenderPage(c, cashierpages.Returns(d))
}

func (h *cashierUI) ReturnsTable(c echo.Context) error {
	d, err := h.returnsData(c)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, cashierpages.ReturnsTable(d))
}

// ReturnForm renders the per-line return modal for a sale.
func (h *cashierUI) ReturnForm(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	detail, err := h.s.sales.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, cashierpages.CashierReturnForm(*detail))
}

// ReturnSubmit processes a partial return; it returns JSON so the saleReturn()
// Alpine component (apiFetch) can handle it just like the admin path, but it is
// reachable by cashiers (the /api equivalent is admin/manager only).
func (h *cashierUI) ReturnSubmit(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in sales.PartialReturnInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	detail, err := h.s.sales.PartialReturn(c.Request().Context(), id, in, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	h.s.logAudit(c, audit.ActionReturn, "sale", strconv.FormatInt(id, 10), "partial return")
	return response.OK(c, detail)
}

// ============================ Damage ============================

func (h *cashierUI) Damage(c echo.Context) error {
	prods, _, err := h.s.products.List(c.Request().Context(), products.ListQuery{Limit: 200})
	if err != nil {
		return err
	}
	return response.RenderPage(c, cashierpages.Damage(cashierpages.DamageData{
		CashierName: middleware.CurrentUserName(c),
		Role:        middleware.CurrentRole(c),
		Products:    prods,
	}))
}

func (h *cashierUI) DamageRecord(c echo.Context) error {
	var in stock.DamageInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := h.s.stock.Damage(c.Request().Context(), in, middleware.CurrentUserID(c)); err != nil {
		return err
	}
	h.s.logAudit(c, audit.ActionUpdate, "product", strconv.FormatInt(in.ProductID, 10), "damage write-off")
	return htmxDone(c, "Damage written off", "reload-stock")
}

// ============================ Credit collection ============================

func (h *cashierUI) creditData(c echo.Context) (cashierpages.CreditData, error) {
	ctx := c.Request().Context()
	all, err := h.s.customers.List(ctx, "")
	if err != nil {
		return cashierpages.CreditData{}, err
	}
	owing := make([]customers.Customer, 0, len(all))
	for _, cust := range all {
		if cust.OutstandingBalance.IsPositive() {
			owing = append(owing, cust)
		}
	}
	return cashierpages.CreditData{
		CashierName: middleware.CurrentUserName(c),
		Role:        middleware.CurrentRole(c),
		Symbol:      h.cashierSymbol(ctx),
		Customers:   owing,
	}, nil
}

func (h *cashierUI) Credit(c echo.Context) error {
	d, err := h.creditData(c)
	if err != nil {
		return err
	}
	return response.RenderPage(c, cashierpages.Credit(d))
}

func (h *cashierUI) CreditTable(c echo.Context) error {
	d, err := h.creditData(c)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, cashierpages.CreditTable(d))
}

func (h *cashierUI) CreditPayForm(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	cust, err := h.s.customers.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, cashierpages.CreditPayForm(*cust, h.cashierSymbol(c.Request().Context())))
}

func (h *cashierUI) CreditPay(c echo.Context) error {
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
	cust, err := h.s.customers.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	if err := h.s.customers.RecordPayment(c.Request().Context(), id, in); err != nil {
		return err
	}
	// Mirror the cash into the cashier's open drawer (no-op if none is open) so
	// credit collected shows up in the register's expected cash and audit trail.
	if amt, perr := money.Parse(in.Amount); perr == nil {
		h.s.cashRegister.RecordCreditCash(c.Request().Context(), middleware.CurrentUserID(c), amt, "credit collected: "+cust.Name)
	}
	h.s.logAudit(c, audit.ActionPayment, "customer", strconv.FormatInt(id, 10), "credit collected "+in.Amount+" from "+cust.Name)
	return htmxDone(c, "Payment recorded", "reload-ccredit")
}
