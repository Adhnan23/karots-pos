package web

import (
	"context"
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/internal/datetime"
	"karots-pos/internal/escpos"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/settings"
	"karots-pos/internal/features/warranty"
	"karots-pos/internal/middleware"
	"karots-pos/internal/printing"
	"karots-pos/internal/response"
	cashierpages "karots-pos/templates/pages/cashier"

	"github.com/labstack/echo/v4"
)

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
	return response.RenderPage(c, cashierpages.MoneyReceiptPage(cashierpages.MoneyReceiptViewData{
		CashierName:   middleware.CurrentUserName(c),
		Role:          middleware.CurrentRole(c),
		ShowChangePin: h.showChangePin(c),
		Symbol:        h.cashierSymbol(ctx),
		Settings:      *cfg,
		Receipt:       *rec,
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

// buildWarrantySlip renders a replacement slip for (re)printing (UI-agnostic).
func (s *Server) buildWarrantySlip(ctx context.Context, cfg *settings.Settings, oldSerial string, u *warranty.Unit) []byte {
	slip := escpos.WarrantySlip{
		ProductName:   u.ProductName,
		OldSerial:     oldSerial,
		NewSerial:     u.SerialNo,
		WarrantyUntil: datetime.Date(u.WarrantyUntil),
	}
	if u.CustomerName != nil {
		slip.CustomerName = *u.CustomerName
	}
	return escpos.WarrantyDocument(slip, *cfg, s.receiptImgOptions(ctx, cfg))
}
