package web

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/datetime"
	"karots-pos/internal/escpos"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/auth"
	"karots-pos/internal/features/customers"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/sales"
	"karots-pos/internal/features/settings"
	"karots-pos/internal/features/stock"
	"karots-pos/internal/features/warranty"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/receiptimg"
	"karots-pos/internal/response"
	poststatic "karots-pos/static"
	cashierpages "karots-pos/templates/pages/cashier"

	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
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

// showChangePin decides whether the terminal shows the "Change PIN" link.
// Admins/managers always may; cashiers only when the shop allows it.
func (h *cashierUI) showChangePin(c echo.Context) bool {
	if middleware.CurrentRole(c) != auth.RoleCashier {
		return true
	}
	return h.s.auth.AllowCashierPinChange(c.Request().Context())
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
	symbol, defaultType, askToPrint := "Rs.", "retail", true
	if cfg, err := h.s.settings.Get(ctx); err == nil {
		symbol = cfg.CurrencySymbol
		defaultType = cfg.DefaultSaleType
		askToPrint = cfg.AskToPrint
	}
	return response.RenderPage(c, cashierpages.POS(cashierpages.POSData{
		CashierName:     middleware.CurrentUserName(c),
		Role:            middleware.CurrentRole(c),
		ShowChangePin:   h.showChangePin(c),
		Symbol:          symbol,
		DefaultSaleType: defaultType,
		AskToPrint:      askToPrint,
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
		Detail:      *detail,
		Settings:    *cfg,
		AutoPrint:   c.QueryParam("print") == "1",
		Narrow:      narrow,
		CustomerDue: h.customerDue(ctx, detail),
	}))
}

// customerDue returns the customer's current outstanding balance for a sale, or
// zero when the sale has no customer (or the lookup fails). Used to print the
// "Total due" line on credit receipts. Best-effort: it never blocks the receipt.
func (h *cashierUI) customerDue(ctx context.Context, detail *sales.Detail) decimal.Decimal {
	if detail.Sale.CustomerID == nil {
		return decimal.Zero
	}
	cust, err := h.s.customers.Get(ctx, *detail.Sale.CustomerID)
	if err != nil {
		return decimal.Zero
	}
	return cust.OutstandingBalance
}

// PrintReceipt sends a sale straight to the thermal printer as ESC/POS bytes
// (built-in font, sized to the receipt_width setting, with an auto-cut). This is
// the reliable path for the Xprinter: it bypasses the browser/PDF route that a
// driverless raw queue prints as garbage.
// receiptOptions renders the logo and secondary (non-Latin) shop name to ESC/POS
// raster blocks for the printed receipt. Failures are non-fatal — the receipt
// still prints without that element.
func (h *cashierUI) receiptOptions(ctx context.Context, cfg *settings.Settings) escpos.Options {
	var opts escpos.Options
	dots := receiptimg.PrinterDots(cfg.ReceiptWidth)
	if src := cfg.LogoSrc(); src != "" {
		if img, err := receiptimg.LoadImage(ctx, src, poststatic.Files); err == nil {
			opts.Logo = receiptimg.Logo(img, dots)
		}
	}
	if cfg.ShopNameSi != nil && *cfg.ShopNameSi != "" {
		opts.SubName = receiptimg.SubName(*cfg.ShopNameSi, dots, dots/14)
	}
	return opts
}

// receiptQueue is the print target for the logged-in cashier: their own
// per-counter printer (set on their user record) when present, otherwise the
// shop-wide printer chosen in Settings (a detected printer name, a
// "tcp://host:9100" network address, or empty = the OS default printer). Only
// the target varies per cashier — paper width, logo and footer stay global.
func (h *cashierUI) receiptQueue(c echo.Context, cfg *settings.Settings) string {
	if uid := middleware.CurrentUserID(c); uid != 0 {
		if u, err := h.s.auth.GetUser(c.Request().Context(), uid); err == nil && strings.TrimSpace(u.ReceiptPrinter) != "" {
			return u.ReceiptPrinter
		}
	}
	return cfg.ReceiptPrinter
}

// printRefundSlip prints the refund slip for a sale's latest return. Best-effort:
// any failure (no return rows, no printer) is logged and swallowed so the return
// flow is never blocked by printing.
func (h *cashierUI) printRefundSlip(c echo.Context, saleID int64) {
	ctx := c.Request().Context()
	rr, err := h.s.sales.ReturnReceipt(ctx, saleID)
	if err != nil {
		log.Printf("refund slip: load return for sale %d: %v", saleID, err)
		return
	}
	cfg, err := h.s.settings.Get(ctx)
	if err != nil {
		log.Printf("refund slip: load settings: %v", err)
		return
	}
	if err := escpos.Send(ctx, h.receiptQueue(c, cfg), escpos.ReturnDocument(*rr, *cfg, h.receiptOptions(ctx, cfg))); err != nil {
		log.Printf("refund slip: print for sale %d: %v", saleID, err)
	}
}

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
	opts := h.receiptOptions(ctx, cfg)
	opts.Serials = h.saleSerials(ctx, id)
	opts.CustomerDue = h.customerDue(ctx, detail)
	if err := escpos.Send(ctx, h.receiptQueue(c, cfg), escpos.Document(*detail, *cfg, opts)); err != nil {
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
	rows, err := h.s.sales.List(ctx, sales.ListFilter{Query: q, Limit: 50})
	if err != nil {
		return err
	}
	return response.RenderPage(c, cashierpages.Receipts(cashierpages.ReceiptsData{
		CashierName:   middleware.CurrentUserName(c),
		Role:          middleware.CurrentRole(c),
		ShowChangePin: h.showChangePin(c),
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
		CashierName:   middleware.CurrentUserName(c),
		Role:          middleware.CurrentRole(c),
		ShowChangePin: h.showChangePin(c),
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
		CashierName:   middleware.CurrentUserName(c),
		Role:          middleware.CurrentRole(c),
		ShowChangePin: h.showChangePin(c),
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
	detail, cashRefund, err := h.s.sales.PartialReturn(c.Request().Context(), id, in, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	h.s.logAudit(c, audit.ActionReturn, "sale", strconv.FormatInt(id, 10), "partial return")
	// A cash refund leaves the cashier's drawer; record it so the close
	// reconciliation stays accurate (no-op when no session is open).
	h.s.cashRegister.RecordRefundCash(c.Request().Context(), middleware.CurrentUserID(c), cashRefund, "cash refund: "+detail.Sale.ReceiptNo)
	// Hand the customer a refund slip. Non-fatal: a printer problem must never
	// fail the return itself (the goods are already restocked / credit adjusted).
	h.printRefundSlip(c, id)
	return response.OK(c, detail)
}

// ============================ Damage ============================

func (h *cashierUI) Damage(c echo.Context) error {
	prods, _, err := h.s.products.List(c.Request().Context(), products.ListQuery{Limit: 200})
	if err != nil {
		return err
	}
	return response.RenderPage(c, cashierpages.Damage(cashierpages.DamageData{
		CashierName:   middleware.CurrentUserName(c),
		Role:          middleware.CurrentRole(c),
		ShowChangePin: h.showChangePin(c),
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

// QuickItem creates a missing product on the fly so the cashier can sell an item
// that isn't in the catalog yet, and returns it (as JSON, same shape as a barcode
// lookup) so the POS can drop it straight into the cart. It is flagged for admin
// review. Any cashier may do this; the created_by stamp records who.
func (h *cashierUI) QuickItem(c echo.Context) error {
	var in products.QuickInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request")
	}
	p, err := h.s.products.QuickCreate(c.Request().Context(), in, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	h.s.logAudit(c, audit.ActionCreate, "product", strconv.FormatInt(p.ID, 10), "quick-add at till: "+p.Name)
	return response.OK(c, p)
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
		CashierName:   middleware.CurrentUserName(c),
		Role:          middleware.CurrentRole(c),
		ShowChangePin: h.showChangePin(c),
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
	if err := h.s.customers.RecordPayment(c.Request().Context(), id, in, middleware.CurrentUserID(c)); err != nil {
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

// ============================ Warranty ============================

// saleSerials returns the serials recorded on a sale, formatted per product for
// the printed receipt (e.g. "SN: ABC123 (wty 2027-06-13)"). Best-effort: any
// error yields a nil map and the receipt simply omits serials.
func (h *cashierUI) saleSerials(ctx context.Context, saleID int64) map[int64][]string {
	units, err := h.s.warranty.UnitsForSale(ctx, saleID)
	if err != nil || len(units) == 0 {
		return nil
	}
	m := make(map[int64][]string, len(units))
	for _, u := range units {
		label := "SN: " + u.SerialNo
		if u.WarrantyMonths > 0 {
			label += " (wty " + datetime.Date(u.WarrantyUntil) + ")"
		}
		m[u.ProductID] = append(m[u.ProductID], label)
	}
	return m
}

func (h *cashierUI) warrantyData(c echo.Context) (cashierpages.WarrantyData, error) {
	ctx := c.Request().Context()
	status := c.QueryParam("status")
	if status == "" {
		status = "all"
	}
	search := c.QueryParam("q")
	units, err := h.s.warranty.List(ctx, status, search)
	if err != nil {
		return cashierpages.WarrantyData{}, err
	}
	return cashierpages.WarrantyData{
		CashierName:   middleware.CurrentUserName(c),
		Role:          middleware.CurrentRole(c),
		ShowChangePin: h.showChangePin(c),
		Symbol:      h.cashierSymbol(ctx),
		Base:        "/cashier",
		Status:      status,
		Search:      search,
		Units:       units,
	}, nil
}

func (h *cashierUI) Warranty(c echo.Context) error {
	d, err := h.warrantyData(c)
	if err != nil {
		return err
	}
	return response.RenderPage(c, cashierpages.Warranty(d))
}

func (h *cashierUI) WarrantyTable(c echo.Context) error {
	d, err := h.warrantyData(c)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, cashierpages.WarrantyTable(d))
}

// WarrantyLookup renders the result card for a serial search. A not-found serial
// renders a friendly "not found" card rather than an error page.
func (h *cashierUI) WarrantyLookup(c echo.Context) error {
	ctx := c.Request().Context()
	serial := c.QueryParam("serial")
	if strings.TrimSpace(serial) == "" {
		return response.RenderFragment(c, cashierpages.WarrantyResult(nil, serial, "/cashier"))
	}
	detail, err := h.s.warranty.Lookup(ctx, serial)
	if err != nil {
		if ae, ok := apperr.As(err); ok && ae.Status == http.StatusNotFound {
			return response.RenderFragment(c, cashierpages.WarrantyResult(nil, serial, "/cashier"))
		}
		return err
	}
	return response.RenderFragment(c, cashierpages.WarrantyResult(detail, serial, "/cashier"))
}

// WarrantyReplace records a replacement and returns the refreshed card for the
// new (replacement) unit, prints a replacement slip, and refreshes the list.
func (h *cashierUI) WarrantyReplace(c echo.Context) error {
	ctx := c.Request().Context()
	unitID, err := strconv.ParseInt(c.FormValue("unit_id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid unit")
	}
	newSerial := c.FormValue("new_serial")
	reason := c.FormValue("reason")
	oldSerial := c.FormValue("old_serial")

	newUnit, err := h.s.warranty.RecordReplacement(ctx, unitID, newSerial, reason, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	h.s.logAudit(c, audit.ActionUpdate, "warranty", strconv.FormatInt(unitID, 10), "replaced "+oldSerial+" -> "+newUnit.SerialNo)
	// Hand the customer a replacement slip. Non-fatal: a printer problem must
	// never fail the replacement (it's already recorded).
	h.printWarrantySlip(c, oldSerial, newUnit)

	detail, err := h.s.warranty.Lookup(ctx, newUnit.SerialNo)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, cashierpages.WarrantyResult(detail, newUnit.SerialNo, "/cashier"),
		response.ToastAnd("Replacement recorded", "success", "reload-warranty"))
}

// printWarrantySlip prints the replacement slip best-effort (logged, swallowed).
func (h *cashierUI) printWarrantySlip(c echo.Context, oldSerial string, u *warranty.Unit) {
	ctx := c.Request().Context()
	cfg, err := h.s.settings.Get(ctx)
	if err != nil {
		log.Printf("warranty slip: load settings: %v", err)
		return
	}
	slip := escpos.WarrantySlip{
		ProductName:   u.ProductName,
		OldSerial:     oldSerial,
		NewSerial:     u.SerialNo,
		WarrantyUntil: datetime.Date(u.WarrantyUntil),
	}
	if u.CustomerName != nil {
		slip.CustomerName = *u.CustomerName
	}
	if err := escpos.Send(ctx, h.receiptQueue(c, cfg), escpos.WarrantyDocument(slip, *cfg, h.receiptOptions(ctx, cfg))); err != nil {
		log.Printf("warranty slip: print for %s: %v", u.SerialNo, err)
	}
}
