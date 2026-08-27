package web

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/escpos"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/reports"
	"karots-pos/internal/features/settings"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/printing"
	"karots-pos/internal/response"
	adminpages "karots-pos/templates/pages/admin"
	"karots-pos/templates/shared"

	"github.com/labstack/echo/v4"
)

// ===================== Money Receipts (cash receipts) =====================

// MoneyReceipts lists money-movement receipts, searchable by number / party /
// kind and filterable by the shared report date presets.
func (a *adminUI) MoneyReceipts(c echo.Context) error {
	ctx := c.Request().Context()
	preset := c.QueryParam("preset")
	from, to, fromStr, toStr, err := resolveReceiptRange(c)
	if err != nil {
		return err
	}
	rows, err := a.s.cashflowReceipts.List(ctx, cashflow.ReceiptFilter{
		Query: strings.TrimSpace(c.QueryParam("q")),
		Kind:  strings.TrimSpace(c.QueryParam("kind")),
		From:  from,
		To:    to,
	})
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.MoneyReceiptsPage(adminpages.MoneyReceiptsData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Rows:     rows,
		Query:    strings.TrimSpace(c.QueryParam("q")),
		Kind:     strings.TrimSpace(c.QueryParam("kind")),
		Preset:   preset,
		From:     fromStr,
		To:       toStr,
	}))
}

// MoneyReceipt renders one receipt as a print-friendly page (browser Print for
// A4, Reprint slip for the thermal printer).
func (a *adminUI) MoneyReceipt(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	rec, err := a.s.cashflowReceipts.Get(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := a.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	base := "/admin/money-receipts/" + strconv.FormatInt(id, 10)
	return response.RenderPage(c, adminpages.MoneyReceiptPage(adminpages.MoneyReceiptData{
		Thermal:  shared.ThermalFrom(cfg.ReceiptWidth, c.QueryParam("size"), "Receipt "+rec.ReceiptNo, base, base+"/print"),
		Settings: *cfg,
		Receipt:  *rec,
	}))
}

// MoneyReceiptPrint re-sends a receipt's thermal slip to the configured printer.
func (a *adminUI) MoneyReceiptPrint(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	rec, err := a.s.cashflowReceipts.Get(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := a.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.ReceiptPrinter) == "" {
		c.Response().Header().Set("HX-Trigger", response.Toast("No receipt printer configured", "error"))
		return c.NoContent(200)
	}
	eff := receiptSizeOverride(*cfg, c)
	if err := printing.Raw(ctx, cfg.ReceiptPrinter, buildReceiptSlip(&eff, *rec, a.s.receiptImgOptions(ctx, &eff))); err != nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("Print failed: "+err.Error(), "error"))
		return c.NoContent(200)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Slip sent to printer", "success"))
	return c.NoContent(200)
}

// resolveReceiptRange resolves the shared report date presets to a [from, to)
// range for the receipts list (nil/nil means "all time" when no preset given).
func resolveReceiptRange(c echo.Context) (from, to *time.Time, fromStr, toStr string, err error) {
	preset := c.QueryParam("preset")
	if preset == "" && c.QueryParam("from") == "" && c.QueryParam("to") == "" {
		return nil, nil, "", "", nil
	}
	f, t, fStr, tStr, rerr := reports.ResolveRange(preset, c.QueryParam("from"), c.QueryParam("to"))
	if rerr != nil {
		return nil, nil, "", "", apperr.Validation(rerr.Error())
	}
	return &f, &t, fStr, tStr, nil
}

// afterMoneyMove applies the shop's print policy after an ADMIN money move,
// identically for every admin money-move handler. With AskToPrint on, it fires
// the shared Print / Skip prompt (via the "money-print" HX-Trigger) and reloads
// the page once the operator decides; off, it best-effort prints the thermal slip
// and refreshes in place. Mirrors the sale + drawer paths. (It lives on Server so
// admin handlers reach it via a.s.)
func (s *Server) afterMoneyMove(c echo.Context, rec *cashflow.Receipt) error {
	ctx := c.Request().Context()
	cfg, err := s.settings.Get(ctx)
	if err != nil {
		return err
	}
	if cfg.AskToPrint {
		// Prompt in place: close the modal, ask Print/Skip, reload after so the
		// cash-flow balances refresh once the operator has decided.
		printURL := "/admin/money-receipts/" + strconv.FormatInt(rec.ID, 10) + "/print"
		c.Response().Header().Set("HX-Trigger",
			response.PrintPrompt("Receipt "+rec.ReceiptNo+" recorded", printURL, true, "close-modal"))
		return c.NoContent(200)
	}
	// Skip & print: send the slip best-effort, then refresh in place.
	if strings.TrimSpace(cfg.ReceiptPrinter) != "" {
		_ = printing.Raw(ctx, cfg.ReceiptPrinter, buildReceiptSlip(cfg, *rec, s.receiptImgOptions(ctx, cfg)))
	}
	c.Response().Header().Set("HX-Trigger", response.ToastAnd("Receipt "+rec.ReceiptNo+" recorded", "success", "close-modal"))
	c.Response().Header().Set("HX-Refresh", "true")
	return c.NoContent(200)
}

// printMoneyReceipt best-effort prints a money receipt's thermal slip. Used by
// cashier counter flows (credit collection, refunds) which can't redirect to the
// admin-only receipt page — they print the slip and stay on the cashier screen.
func (s *Server) printMoneyReceipt(ctx context.Context, rec *cashflow.Receipt) {
	s.printMoneyReceiptKick(ctx, rec, false)
}

// printMoneyReceiptKick is printMoneyReceipt with an optional leading drawer
// pulse. When kick is true (the cash actually left/entered the physical till
// drawer) the pulse is folded into the SAME print job as the slip, so on a USB
// thermal printer the drawer pops and the slip prints in one pass — no separate
// kick job, no inter-job device open/close gap. Pass kick=false when the money
// never touched the drawer (locker/bank leg) or when the caller kicks separately
// (prompt mode, where the slip prints later on click).
func (s *Server) printMoneyReceiptKick(ctx context.Context, rec *cashflow.Receipt, kick bool) {
	cfg, err := s.settings.Get(ctx)
	if err != nil || cfg == nil || strings.TrimSpace(cfg.ReceiptPrinter) == "" {
		return
	}
	slip := buildReceiptSlip(cfg, *rec, s.receiptImgOptions(ctx, cfg))
	if kick {
		slip = append(escpos.DrawerKick(*cfg), slip...)
	}
	_ = printing.Raw(ctx, cfg.ReceiptPrinter, slip)
}

// buildReceiptSlip renders a money receipt as raw ESC/POS bytes for the thermal
// printer using the SHARED receipt header/footer (escpos.Header/Footer), so a
// money receipt carries the same branding — big shop name, secondary-language
// name, address, settings footer + credit line — as the sale receipt. Only the
// body (From/To/Party/Amount) is receipt-specific.
func buildReceiptSlip(cfg *settings.Settings, r cashflow.Receipt, opts escpos.Options) []byte {
	w := escpos.Columns(cfg.ReceiptWidth)
	var b bytes.Buffer
	escpos.Init(&b)
	escpos.Header(&b, *cfg, opts)
	escpos.Title(&b, receiptKindLabel(r.Kind), w)

	// --- Meta (left, values right-aligned like the sale) ---
	escpos.Left(&b)
	escpos.Divider(&b, w)
	escpos.Line(&b, escpos.LeftRight("Receipt:", r.ReceiptNo, w))
	escpos.Line(&b, escpos.LeftRight("Date:", r.CreatedAt.Format("2006-01-02 15:04"), w))
	escpos.Line(&b, escpos.LeftRight("From:", escpos.ASCII(r.FromLabel), w))
	escpos.Line(&b, escpos.LeftRight("To:", escpos.ASCII(r.ToLabel), w))
	if strings.TrimSpace(r.Party) != "" {
		escpos.Line(&b, escpos.LeftRight("Party:", escpos.ASCII(r.Party), w))
	}
	if r.CreatedByName != nil && *r.CreatedByName != "" {
		escpos.Line(&b, escpos.LeftRight("By:", escpos.ASCII(*r.CreatedByName), w))
	}
	escpos.Divider(&b, w)

	// --- Amount (emphasized, like the sale's TOTAL) ---
	escpos.Emphasis(&b, true)
	escpos.Line(&b, escpos.LeftRight("Amount", money.Format(cfg.CurrencySymbol, r.Amount), w))
	escpos.Emphasis(&b, false)
	if strings.TrimSpace(r.Note) != "" {
		for _, ln := range escpos.Wrap(escpos.ASCII("Note: "+r.Note), w) {
			escpos.Line(&b, ln)
		}
	}
	escpos.Divider(&b, w)

	// A money receipt is a cash hand-over, so keep a signature strip.
	escpos.Center(&b)
	escpos.Line(&b, "Signature: ____________________")

	escpos.Footer(&b, *cfg)
	return b.Bytes()
}

// receiptKindLabel is the human label for a receipt kind on the slip.
func receiptKindLabel(k string) string {
	switch k {
	case "transfer":
		return "Transfer"
	case "payment":
		return "Payment"
	case "intake":
		return "Intake"
	case "supplier_payment":
		return "Supplier payment"
	case "supplier_refund":
		return "Supplier refund"
	case "customer_payment":
		return "Customer payment"
	case "expense":
		return "Expense"
	case "refund":
		return "Refund"
	case "capital":
		return "Capital"
	case "bank_charge":
		return "Bank charge"
	case "interest":
		return "Interest"
	case "adjust":
		return "Adjustment"
	case "billpay":
		return "Bill payment"
	case "getmoney":
		return "Money out"
	case "reload", "deposit", "withdrawal", "topup":
		return "Reload"
	}
	return k
}
