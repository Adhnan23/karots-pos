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
	if err := printing.Raw(ctx, cfg.ReceiptPrinter, buildReceiptSlip(cfg, *rec)); err != nil {
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
		_ = printing.Raw(ctx, cfg.ReceiptPrinter, buildReceiptSlip(cfg, *rec))
	}
	c.Response().Header().Set("HX-Trigger", response.ToastAnd("Receipt "+rec.ReceiptNo+" recorded", "success", "close-modal"))
	c.Response().Header().Set("HX-Refresh", "true")
	return c.NoContent(200)
}

// printMoneyReceipt best-effort prints a money receipt's thermal slip. Used by
// cashier counter flows (credit collection, refunds) which can't redirect to the
// admin-only receipt page — they print the slip and stay on the cashier screen.
func (s *Server) printMoneyReceipt(ctx context.Context, rec *cashflow.Receipt) {
	cfg, err := s.settings.Get(ctx)
	if err != nil || cfg == nil || strings.TrimSpace(cfg.ReceiptPrinter) == "" {
		return
	}
	_ = printing.Raw(ctx, cfg.ReceiptPrinter, buildReceiptSlip(cfg, *rec))
}

// buildReceiptSlip renders a money receipt as raw ESC/POS bytes for the thermal
// printer, carrying the shop header from Settings. Mirrors the recharge slip.
func buildReceiptSlip(cfg *settings.Settings, r cashflow.Receipt) []byte {
	width := 32
	if strings.TrimSpace(cfg.ReceiptWidth) == "80" {
		width = 48
	}
	var b bytes.Buffer
	b.Write([]byte{0x1B, 0x40}) // ESC @  initialise
	center := func() { b.Write([]byte{0x1B, 0x61, 0x01}) }
	left := func() { b.Write([]byte{0x1B, 0x61, 0x00}) }
	bold := func(on bool) {
		if on {
			b.Write([]byte{0x1B, 0x45, 0x01})
		} else {
			b.Write([]byte{0x1B, 0x45, 0x00})
		}
	}
	// Sanitise every free-text/label field to printable ASCII: the built-in
	// thermal font has no glyphs for en/em dashes or non-Latin names, so raw
	// bytes print as codepage garbage (e.g. a "—" shows up as a CJK character).
	line := func(s string) { b.WriteString(escpos.ASCII(s)); b.WriteByte('\n') }
	rule := func() { b.WriteString(strings.Repeat("-", width)); b.WriteByte('\n') }

	center()
	bold(true)
	line(cfg.ShopName)
	bold(false)
	if cfg.Address != nil && strings.TrimSpace(*cfg.Address) != "" {
		line(*cfg.Address)
	}
	if cfg.Phone != nil && strings.TrimSpace(*cfg.Phone) != "" {
		line(*cfg.Phone)
	}
	rule()
	bold(true)
	line(strings.ToUpper(receiptKindLabel(r.Kind) + " RECEIPT"))
	bold(false)
	line(r.ReceiptNo)
	rule()
	left()
	line("Date    : " + r.CreatedAt.Format("2006-01-02 15:04"))
	line("From    : " + r.FromLabel)
	line("To      : " + r.ToLabel)
	if strings.TrimSpace(r.Party) != "" {
		line("Party   : " + r.Party)
	}
	line("Amount  : " + money.Format(cfg.CurrencySymbol, r.Amount))
	if strings.TrimSpace(r.Note) != "" {
		line("Note    : " + r.Note)
	}
	if r.CreatedByName != nil && *r.CreatedByName != "" {
		line("By      : " + *r.CreatedByName)
	}
	rule()
	center()
	line("Signature: ____________________")
	line("")
	line("Thank you")
	b.WriteString("\n\n\n")
	b.Write([]byte{0x1D, 0x56, 0x42, 0x00}) // GS V partial cut with feed
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
	}
	return k
}
