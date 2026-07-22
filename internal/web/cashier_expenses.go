package web

import (
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/expenses"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"
	cashierpages "karots-pos/templates/pages/cashier"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

// ============================ Expenses at the counter ============================
//
// A cashier is often alone when a running cost lands: a utility bill, bags that
// ran out, a repairman for the photocopier. These routes let them book it and pay
// it from their own drawer (or a cashier-allowed locker) — nowhere else. Gated by
// middleware.RequireSupplierAccess, the same counter-operations permission as
// suppliers: admins/managers always pass, a cashier only with the per-user flag.

// Expenses renders the form-only expense page. No ledger is shown to cashiers —
// the full list holds salaries and rent.
func (h *cashierUI) Expenses(c echo.Context) error {
	ctx := c.Request().Context()
	sources, err := h.cashierCashSources(ctx, middleware.CurrentUserID(c), middleware.CurrentUserName(c))
	if err != nil {
		return err
	}
	svcs, err := h.s.products.ListServices(ctx)
	if err != nil {
		return err
	}
	distinct, err := expenses.NewRepository(h.s.db).DistinctCategories(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, cashierpages.CashierExpense(cashierpages.CashierExpenseData{
		CashierName:   middleware.CurrentUserName(c),
		Role:          middleware.CurrentRole(c),
		ShowChangePin: h.showChangePin(c),
		Symbol:        h.cashierSymbol(ctx),
		Sources:       sources,
		Services:      svcs,
		Categories:    expenses.MergedCategories(distinct),
	}))
}

// ExpenseRecord books a counter expense and pays it out of the chosen cash source
// in ONE transaction, so the expense row, the drawer/locker debit and the receipt
// always commit together. The source is validated through counterSource: the
// cashier's till and cashier-flagged lockers only. Always dated today.
func (h *cashierUI) ExpenseRecord(c echo.Context) error {
	ctx := c.Request().Context()
	var in expenses.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	in.ExpenseDate = "" // cashiers can't backdate — parseCreate defaults to now
	if err := c.Validate(&in); err != nil {
		return err
	}
	src, err := h.counterSource(c, c.FormValue("source"))
	if err != nil {
		return err
	}
	userID := middleware.CurrentUserID(c)

	reason := strings.TrimSpace(in.Category)
	if in.Description != nil && strings.TrimSpace(*in.Description) != "" {
		reason += " - " + strings.TrimSpace(*in.Description)
	}
	var rec *cashflow.Receipt
	var expenseID int64
	err = appdb.WithTx(ctx, h.s.db, func(tx *sqlx.Tx) error {
		e, err := h.s.expenses.CreateInTx(ctx, tx, in, userID)
		if err != nil {
			return err
		}
		expenseID = e.ID
		r, err := h.s.cashflow.MoveTx(ctx, tx, cashflow.MoveInput{
			From:        src,
			To:          cashflow.External(),
			Amount:      e.Amount,
			Reason:      reason,
			ReceiptKind: "expense",
			Ref:         &cashflow.Ref{Kind: "expense", ID: e.ID},
			ActorID:     userID,
		})
		rec = r
		return err
	})
	if err != nil {
		return err
	}
	h.s.logAudit(c, audit.ActionCreate, "expense", strconv.FormatInt(expenseID, 10),
		"recorded expense paid from "+rec.FromLabel+" at the counter")

	msg := "Expense recorded — " + money.Display(rec.Amount)
	cfg, _ := h.s.settings.Get(ctx)
	if rec != nil && cfg != nil && cfg.AskToPrint {
		printURL := "/cashier/money-receipts/" + strconv.FormatInt(rec.ID, 10) + "/print"
		c.Response().Header().Set("HX-Trigger", response.PrintPrompt(msg+" · "+rec.ReceiptNo, printURL, false))
		return c.NoContent(200)
	}
	if rec != nil {
		h.s.printMoneyReceipt(ctx, rec)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast(msg, "success"))
	return c.NoContent(200)
}
