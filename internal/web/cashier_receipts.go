package web

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/datetime"
	"karots-pos/internal/escpos"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/customers"
	"karots-pos/internal/features/settings"
	"karots-pos/internal/features/warranty"
	"karots-pos/internal/printing"
	"karots-pos/internal/response"
	cashierpages "karots-pos/templates/pages/cashier"
	"karots-pos/templates/shared"

	"github.com/labstack/echo/v4"
)

// monthsLeftLabel formats how much warranty cover remains from now until until,
// e.g. "11 mo left". Returns "expired" once past, "<1 mo left" within a month.
func monthsLeftLabel(until time.Time) string {
	now := time.Now()
	if !until.After(now) {
		return "expired"
	}
	months := int(until.Sub(now).Hours() / 24 / 30.4375)
	if months < 1 {
		return "<1 mo left"
	}
	return fmt.Sprintf("%d mo left", months)
}

// txMethodLabel converts a raw payment method string to a display label.
func txMethodLabel(method string) string {
	labels := map[string]string{
		"cash":   "Cash",
		"card":   "Card",
		"online": "Online",
	}
	if l, ok := labels[method]; ok {
		return l
	}
	return method
}

// ReceiptsCash renders the Cash tab fragment: shop-wide CR- money receipts with
// search, kind filter, and the shared date-range presets.
func (h *cashierUI) ReceiptsCash(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := resolveReceiptRange(c)
	if err != nil {
		return err
	}
	q := strings.TrimSpace(c.QueryParam("q"))
	kind := strings.TrimSpace(c.QueryParam("kind"))
	rows, err := h.s.cashflowReceipts.List(ctx, cashflow.ReceiptFilter{Query: q, Kind: kind, From: from, To: to})
	if err != nil {
		return err
	}
	return response.RenderFragment(c, cashierpages.ReceiptsCashTab(cashierpages.ReceiptsCashData{
		Symbol: h.cashierSymbol(ctx),
		Rows:   rows,
		Query:  q,
		Kind:   kind,
		Preset: c.QueryParam("preset"),
		From:   fromStr,
		To:     toStr,
	}))
}

// MoneyReceipt renders one CR- receipt as a cashier-accessible print-friendly page.
func (h *cashierUI) MoneyReceipt(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	rec, err := h.s.cashflowReceipts.Get(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := h.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	base := "/cashier/money-receipts/" + strconv.FormatInt(id, 10)
	return response.RenderPage(c, cashierpages.MoneyReceiptPage(cashierpages.MoneyReceiptViewData{
		Thermal:  shared.ThermalFrom(cfg.ReceiptWidth, c.QueryParam("size"), "Receipt "+rec.ReceiptNo, base, base+"/print"),
		Symbol:   h.cashierSymbol(ctx),
		Settings: *cfg,
		Receipt:  *rec,
	}))
}

// MoneyReceiptPrint re-sends a CR- receipt's thermal slip from the terminal.
func (h *cashierUI) MoneyReceiptPrint(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	rec, err := h.s.cashflowReceipts.Get(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := h.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	target := h.receiptQueue(c, cfg)
	if strings.TrimSpace(target) == "" {
		c.Response().Header().Set("HX-Trigger", response.Toast("No receipt printer configured", "error"))
		return c.NoContent(200)
	}
	if err := printing.Raw(ctx, target, buildReceiptSlip(cfg, *rec)); err != nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("Print failed: "+err.Error(), "error"))
		return c.NoContent(200)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Slip sent to printer", "success"))
	return c.NoContent(200)
}

// ReceiptsWarranty renders the Warranty tab fragment.
func (h *cashierUI) ReceiptsWarranty(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := resolveReceiptRange(c)
	if err != nil {
		return err
	}
	q := strings.TrimSpace(c.QueryParam("q"))
	rows, err := h.s.warranty.ListClaims(ctx, warranty.ClaimFilter{Search: q, From: from, To: to})
	if err != nil {
		return err
	}
	return response.RenderFragment(c, cashierpages.ReceiptsWarrantyTab(cashierpages.ReceiptsWarrantyData{
		Rows: rows, Query: q, Preset: c.QueryParam("preset"), From: fromStr, To: toStr,
	}))
}

// ReceiptsCredit renders the Credit tab fragment: DP- credit-payment receipts.
func (h *cashierUI) ReceiptsCredit(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := resolveReceiptRange(c)
	if err != nil {
		return err
	}
	q := strings.TrimSpace(c.QueryParam("q"))
	rows, err := h.s.customers.ListPayments(ctx, customers.DebtFilter{Query: q, From: from, To: to, Limit: 50})
	if err != nil {
		return err
	}
	return response.RenderFragment(c, cashierpages.ReceiptsCreditTab(cashierpages.ReceiptsCreditData{
		Symbol: h.cashierSymbol(ctx),
		Rows:   rows,
		Query:  q,
		Preset: c.QueryParam("preset"),
		From:   fromStr,
		To:     toStr,
	}))
}

// DebtReceiptView shows one DP- credit-payment receipt print-friendly.
func (h *cashierUI) DebtReceiptView(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	r, err := h.s.customers.GetPayment(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := h.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	base := "/cashier/receipts/credit/" + strconv.FormatInt(id, 10)
	return response.RenderPage(c, cashierpages.DebtReceiptPage(cashierpages.DebtReceiptViewData{
		Thermal:  shared.ThermalFrom(cfg.ReceiptWidth, c.QueryParam("size"), "Receipt "+r.ReceiptNo, base, base+"/print"),
		Symbol:   h.cashierSymbol(ctx),
		Settings: *cfg,
		Receipt:  *r,
	}))
}

// DebtReceiptPrint (re)sends the DP- credit-payment slip to the thermal printer.
func (h *cashierUI) DebtReceiptPrint(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	r, err := h.s.customers.GetPayment(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := h.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	target := h.receiptQueue(c, cfg)
	if strings.TrimSpace(target) == "" {
		c.Response().Header().Set("HX-Trigger", response.Toast("No receipt printer configured", "error"))
		return c.NoContent(200)
	}
	if err := printing.Raw(ctx, target, h.s.buildDebtSlip(ctx, cfg, debtReceiptToPayment(*r), debtReceiptToCustomer(*r), cashierNameOf(*r))); err != nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("Print failed: "+err.Error(), "error"))
		return c.NoContent(200)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Slip sent to printer", "success"))
	return c.NoContent(200)
}

// debtReceiptToPayment / debtReceiptToCustomer adapt a stored DebtReceipt back
// into the inputs buildDebtSlip expects, so reprint renders an identical slip.
func debtReceiptToPayment(r customers.DebtReceipt) customers.CustomerPayment {
	return customers.CustomerPayment{
		Amount: r.Amount, Method: r.Method, CreatedAt: r.CreatedAt,
		ReceiptNo: &r.ReceiptNo, BalanceBefore: r.BalanceBefore, BalanceAfter: r.BalanceAfter,
	}
}

func debtReceiptToCustomer(r customers.DebtReceipt) *customers.Customer {
	return &customers.Customer{Name: r.CustomerName, Phone: r.CustomerPhone, CreditLimit: r.CreditLimit}
}

func cashierNameOf(r customers.DebtReceipt) string {
	if r.CashierName != nil {
		return *r.CashierName
	}
	return ""
}

// WarrantyReceiptView shows one warranty replacement slip print-friendly.
func (h *cashierUI) WarrantyReceiptView(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("claimId"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	cl, err := h.s.warranty.GetClaim(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := h.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	until, left := h.s.warrantyCover(ctx, cl)
	base := "/cashier/receipts/warranty/" + strconv.FormatInt(id, 10)
	return response.RenderPage(c, cashierpages.WarrantyReceiptPage(cashierpages.WarrantyReceiptViewData{
		Thermal:       shared.ThermalFrom(cfg.ReceiptWidth, c.QueryParam("size"), "Warranty slip", base, "/cashier/warranty/"+strconv.FormatInt(id, 10)+"/print"),
		Settings:      *cfg,
		Claim:         *cl,
		WarrantyUntil: until,
		WarrantyLeft:  left,
	}))
}

// WarrantyReprint re-sends a warranty replacement slip from the terminal.
func (h *cashierUI) WarrantyReprint(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("claimId"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	cl, err := h.s.warranty.GetClaim(ctx, id)
	if err != nil {
		return err
	}
	if cl.ReplacementUnitID == nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("No replacement slip for this claim", "error"))
		return c.NoContent(200)
	}
	newUnit, err := h.s.warranty.GetUnit(ctx, *cl.ReplacementUnitID)
	if err != nil {
		return err
	}
	cfg, err := h.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	target := h.receiptQueue(c, cfg)
	if strings.TrimSpace(target) == "" {
		c.Response().Header().Set("HX-Trigger", response.Toast("No receipt printer configured", "error"))
		return c.NoContent(200)
	}
	if err := printing.Raw(ctx, target, h.s.buildWarrantySlip(ctx, cfg, cl.OldSerial, newUnit)); err != nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("Print failed: "+err.Error(), "error"))
		return c.NoContent(200)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Warranty slip sent to printer", "success"))
	return c.NoContent(200)
}

// warrantyCover resolves a claim's replacement-unit cover for the view page:
// the formatted end date and remaining months. Returns empty strings when the
// claim has no replacement unit (e.g. repair/refund resolution).
func (s *Server) warrantyCover(ctx context.Context, cl *warranty.Claim) (until, left string) {
	if cl.ReplacementUnitID == nil {
		return "", ""
	}
	u, err := s.warranty.GetUnit(ctx, *cl.ReplacementUnitID)
	if err != nil {
		return "", ""
	}
	return datetime.Date(u.WarrantyUntil), monthsLeftLabel(u.WarrantyUntil)
}

// warrantyReplaceTrigger applies the shop's print policy after a warranty
// replacement, identically for the admin and cashier flows. AskToPrint on → the
// shared Print / Skip prompt pointing at the slip's reprint URL; off → best-effort
// auto-print now. Either way it returns the HX-Trigger payload (carrying the
// "reload-warranty" refresh) to attach to the WarrantyResult fragment.
func (s *Server) warrantyReplaceTrigger(ctx context.Context, cfg *settings.Settings, target, reprintURL, oldSerial string, u *warranty.Unit) string {
	if cfg != nil && cfg.AskToPrint {
		return response.PrintPrompt("Replacement recorded", reprintURL, false, "reload-warranty")
	}
	if cfg != nil && strings.TrimSpace(target) != "" {
		_ = printing.Raw(ctx, target, s.buildWarrantySlip(ctx, cfg, oldSerial, u))
	}
	return response.ToastAnd("Replacement recorded", "success", "reload-warranty")
}

// buildWarrantySlip renders a replacement slip for (re)printing (UI-agnostic).
func (s *Server) buildWarrantySlip(ctx context.Context, cfg *settings.Settings, oldSerial string, u *warranty.Unit) []byte {
	slip := escpos.WarrantySlip{
		ProductName:   u.ProductName,
		OldSerial:     oldSerial,
		NewSerial:     u.SerialNo,
		WarrantyUntil: datetime.Date(u.WarrantyUntil),
		WarrantyLeft:  monthsLeftLabel(u.WarrantyUntil),
	}
	if u.CustomerName != nil {
		slip.CustomerName = *u.CustomerName
	}
	return escpos.WarrantyDocument(slip, *cfg, s.receiptImgOptions(ctx, cfg))
}

// buildDebtSlip renders a credit-payment slip for printing/reprint (UI-agnostic).
func (s *Server) buildDebtSlip(ctx context.Context, cfg *settings.Settings, p customers.CustomerPayment, cust *customers.Customer, cashierName string) []byte {
	slip := escpos.DebtSlip{
		Date:          datetime.DateTime(p.CreatedAt),
		Method:        txMethodLabel(p.Method),
		Amount:        p.Amount,
		BalanceBefore: p.BalanceBefore,
		BalanceAfter:  p.BalanceAfter,
		CashierName:   cashierName,
	}
	if p.ReceiptNo != nil {
		slip.ReceiptNo = *p.ReceiptNo
	}
	if cust != nil {
		slip.CustomerName = cust.Name
		if cust.Phone != nil {
			slip.CustomerPhone = *cust.Phone
		}
		cl := cust.CreditLimit
		slip.CreditLimit = &cl
	}
	return escpos.DebtDocument(slip, *cfg, s.receiptImgOptions(ctx, cfg))
}
