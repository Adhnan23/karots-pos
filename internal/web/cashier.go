package web

import (
	"context"
	"strconv"

	"karots-pos/internal/apperr"
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
	return response.RenderPage(c, cashierpages.Receipt(cashierpages.ReceiptData{
		Detail:    *detail,
		Settings:  *cfg,
		AutoPrint: c.QueryParam("print") == "1",
		Narrow:    c.QueryParam("size") == "58",
	}))
}

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
	return htmxDone(c, "Payment recorded", "reload-ccredit")
}
