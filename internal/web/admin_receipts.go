package web

import (
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/customers"
	"karots-pos/internal/features/sales"
	"karots-pos/internal/features/warranty"
	"karots-pos/internal/middleware"
	"karots-pos/internal/printing"
	"karots-pos/internal/response"
	adminpages "karots-pos/templates/pages/admin"
	"karots-pos/templates/shared"

	"github.com/labstack/echo/v4"
)

// Receipts renders the admin Receipts hub shell (Sales tab inline as default;
// Cash + Warranty lazy-load).
func (a *adminUI) Receipts(c echo.Context) error {
	ctx := c.Request().Context()
	sd, err := a.salesReceiptData(c)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.ReceiptsHub(adminpages.ReceiptsHubData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Sales:    sd,
	}))
}

func (a *adminUI) salesReceiptData(c echo.Context) (adminpages.RcSalesData, error) {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := resolveReceiptRange(c)
	if err != nil {
		return adminpages.RcSalesData{}, err
	}
	q := strings.TrimSpace(c.QueryParam("q"))
	rows, err := a.s.sales.List(ctx, sales.ListFilter{Query: q, From: from, To: to, Limit: 100})
	if err != nil {
		return adminpages.RcSalesData{}, err
	}
	return adminpages.RcSalesData{Symbol: a.symbol(ctx), Rows: rows, Query: q, Preset: c.QueryParam("preset"), From: fromStr, To: toStr}, nil
}

// ReceiptsSales renders the Sales tab fragment.
func (a *adminUI) ReceiptsSales(c echo.Context) error {
	d, err := a.salesReceiptData(c)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.RcSalesTab(d))
}

// ReceiptsCash renders the Cash tab fragment.
func (a *adminUI) ReceiptsCash(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := resolveReceiptRange(c)
	if err != nil {
		return err
	}
	q := strings.TrimSpace(c.QueryParam("q"))
	kind := strings.TrimSpace(c.QueryParam("kind"))
	rows, err := a.s.cashflowReceipts.List(ctx, cashflow.ReceiptFilter{Query: q, Kind: kind, From: from, To: to})
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.RcCashTab(adminpages.RcCashData{
		Symbol: a.symbol(ctx), Rows: rows, Query: q, Kind: kind, Preset: c.QueryParam("preset"), From: fromStr, To: toStr,
	}))
}

// ReceiptsWarranty renders the Warranty tab fragment.
func (a *adminUI) ReceiptsWarranty(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := resolveReceiptRange(c)
	if err != nil {
		return err
	}
	q := strings.TrimSpace(c.QueryParam("q"))
	rows, err := a.s.warranty.ListClaims(ctx, warranty.ClaimFilter{Search: q, From: from, To: to})
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.RcWarrantyTab(adminpages.RcWarrantyData{
		Rows: rows, Query: q, Preset: c.QueryParam("preset"), From: fromStr, To: toStr,
	}))
}

// WarrantyReprint (admin) re-sends a warranty replacement slip.
func (a *adminUI) WarrantyReprint(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("claimId"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	cl, err := a.s.warranty.GetClaim(ctx, id)
	if err != nil {
		return err
	}
	if cl.ReplacementUnitID == nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("No replacement slip for this claim", "error"))
		return c.NoContent(200)
	}
	newUnit, err := a.s.warranty.GetUnit(ctx, *cl.ReplacementUnitID)
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
	if err := printing.Raw(ctx, cfg.ReceiptPrinter, a.s.buildWarrantySlip(ctx, cfg, cl.OldSerial, newUnit)); err != nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("Print failed: "+err.Error(), "error"))
		return c.NoContent(200)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Warranty slip sent to printer", "success"))
	return c.NoContent(200)
}

// WarrantyReceiptView shows one warranty replacement slip print-friendly (admin).
func (a *adminUI) WarrantyReceiptView(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("claimId"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	cl, err := a.s.warranty.GetClaim(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := a.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	until, left := a.s.warrantyCover(ctx, cl)
	base := "/admin/receipts/warranty/" + strconv.FormatInt(id, 10)
	return response.RenderPage(c, adminpages.RcWarrantyReceiptPage(adminpages.RcWarrantyViewData{
		Thermal:       shared.ThermalFrom(cfg.ReceiptWidth, c.QueryParam("size"), "Warranty slip", base, base+"/print"),
		Settings:      *cfg,
		Claim:         *cl,
		WarrantyUntil: until,
		WarrantyLeft:  left,
	}))
}

// ReceiptsCredit renders the admin Credit tab fragment: DP- credit-payment receipts.
func (a *adminUI) ReceiptsCredit(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := resolveReceiptRange(c)
	if err != nil {
		return err
	}
	q := strings.TrimSpace(c.QueryParam("q"))
	rows, err := a.s.customers.ListPayments(ctx, customers.DebtFilter{Query: q, From: from, To: to, Limit: 100})
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.RcCreditTab(adminpages.RcCreditData{
		Symbol: a.symbol(ctx), Rows: rows, Query: q, Preset: c.QueryParam("preset"), From: fromStr, To: toStr,
	}))
}

// DebtReceiptView shows one DP- credit-payment receipt print-friendly (admin).
func (a *adminUI) DebtReceiptView(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	r, err := a.s.customers.GetPayment(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := a.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	base := "/admin/receipts/credit/" + strconv.FormatInt(id, 10)
	return response.RenderPage(c, adminpages.RcDebtReceiptPage(adminpages.RcDebtViewData{
		Thermal:  shared.ThermalFrom(cfg.ReceiptWidth, c.QueryParam("size"), "Receipt "+r.ReceiptNo, base, base+"/print"),
		Symbol:   a.symbol(ctx),
		Settings: *cfg,
		Receipt:  *r,
	}))
}

// DebtReceiptPrint (re)sends a DP- credit-payment slip to the printer (admin).
func (a *adminUI) DebtReceiptPrint(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	r, err := a.s.customers.GetPayment(ctx, id)
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
	if err := printing.Raw(ctx, cfg.ReceiptPrinter, a.s.buildDebtSlip(ctx, cfg, debtReceiptToPayment(*r), debtReceiptToCustomer(*r), cashierNameOf(*r))); err != nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("Print failed: "+err.Error(), "error"))
		return c.NoContent(200)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Slip sent to printer", "success"))
	return c.NoContent(200)
}
