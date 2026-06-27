package web

import (
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/expenses"
	"karots-pos/internal/features/lockers"
	"karots-pos/internal/features/reports"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"
	adminpages "karots-pos/templates/pages/admin"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

// ============================ Cash Lockers ============================

// Lockers lists every locker with its live balance plus the combined total.
func (a *adminUI) Lockers(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.lockers.List(ctx, false)
	if err != nil {
		return err
	}
	total := decimal.Zero
	for _, l := range rows {
		if l.IsActive {
			total = total.Add(l.Balance)
		}
	}
	return response.RenderPage(c, adminpages.LockersPage(adminpages.LockersData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Rows:     rows,
		Total:    total,
	}))
}

func (a *adminUI) LockerForm(c echo.Context) error {
	return response.RenderFragment(c, adminpages.LockerForm(nil))
}

func (a *adminUI) LockerEditForm(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	l, err := a.s.lockers.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.LockerForm(l))
}

func (a *adminUI) LockerCreate(c echo.Context) error {
	var in lockers.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	l, err := a.s.lockers.Create(c.Request().Context(), in, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionCreate, "locker", strconv.FormatInt(l.ID, 10), "created locker "+l.Name)
	c.Response().Header().Set("HX-Trigger", response.ToastAnd("Locker created", "success", "close-modal"))
	c.Response().Header().Set("HX-Refresh", "true")
	return c.NoContent(200)
}

func (a *adminUI) LockerUpdate(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in lockers.UpdateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := a.s.lockers.Update(c.Request().Context(), id, in); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionUpdate, "locker", strconv.FormatInt(id, 10), "updated locker")
	c.Response().Header().Set("HX-Trigger", response.ToastAnd("Locker updated", "success", "close-modal"))
	c.Response().Header().Set("HX-Refresh", "true")
	return c.NoContent(200)
}

// LockerArchive flags a locker inactive so it leaves the active pickers/totals.
// History and balance are preserved.
func (a *adminUI) LockerArchive(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.lockers.SetActive(c.Request().Context(), id, false); err != nil {
		return err
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Locker archived", "success"))
	c.Response().Header().Set("HX-Refresh", "true")
	return c.NoContent(200)
}

// LockerTransferForm renders the locker→locker transfer modal, populated with
// the active lockers (and their live balances) to pick between.
func (a *adminUI) LockerTransferForm(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.lockers.List(ctx, true)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.LockerTransferForm(adminpages.LockerTransferData{
		Symbol:  a.symbol(ctx),
		Lockers: rows,
	}))
}

// LockerTransfer moves cash between two lockers atomically via cashflow.Move.
func (a *adminUI) LockerTransfer(c echo.Context) error {
	ctx := c.Request().Context()
	fromID, _ := strconv.ParseInt(c.FormValue("from_id"), 10, 64)
	toID, _ := strconv.ParseInt(c.FormValue("to_id"), 10, 64)
	if fromID == 0 || toID == 0 {
		return apperr.Validation("pick both a source and a destination locker")
	}
	amt, err := decimal.NewFromString(strings.TrimSpace(c.FormValue("amount")))
	if err != nil || !amt.IsPositive() {
		return apperr.Validation("enter a valid amount")
	}
	note := strings.TrimSpace(c.FormValue("note"))
	rec, err := a.s.cashflow.Move(ctx, cashflow.MoveInput{
		From:    cashflow.Locker(fromID),
		To:      cashflow.Locker(toID),
		Amount:  amt,
		Reason:  note,
		ActorID: middleware.CurrentUserID(c),
	})
	if err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionUpdate, "locker", strconv.FormatInt(fromID, 10), "transferred cash between lockers")
	return a.s.afterMoneyMove(c, rec)
}

// LockerAdjustForm renders the bank-charge / interest / manual-adjust modal.
func (a *adminUI) LockerAdjustForm(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	l, err := a.s.lockers.Get(ctx, id)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.LockerAdjustForm(*l, a.symbol(ctx)))
}

// LockerAdjust applies a bank charge, interest or manual adjustment to a locker
// via cashflow.Move (so it produces a CR- receipt). A bank charge also books an
// Expense (category "Bank charges") in the same transaction.
func (a *adminUI) LockerAdjust(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	amt, err := decimal.NewFromString(strings.TrimSpace(c.FormValue("amount")))
	if err != nil || !amt.IsPositive() {
		return apperr.Validation("enter a valid amount")
	}
	note := strings.TrimSpace(c.FormValue("note"))
	userID := middleware.CurrentUserID(c)

	// Map the chosen type to a money move: direction, ledger kind and receipt kind.
	var in cashflow.MoveInput
	bookExpense := false
	switch c.FormValue("type") {
	case "bank_charge":
		in = cashflow.MoveInput{From: cashflow.Locker(id), To: cashflow.External(), LockerKind: "bank_charge", ReceiptKind: "bank_charge"}
		bookExpense = true
	case "interest":
		in = cashflow.MoveInput{From: cashflow.External(), To: cashflow.Locker(id), LockerKind: "interest", ReceiptKind: "interest"}
	case "adjust_up":
		in = cashflow.MoveInput{From: cashflow.External(), To: cashflow.Locker(id), LockerKind: "adjust", ReceiptKind: "adjust"}
	case "adjust_down":
		in = cashflow.MoveInput{From: cashflow.Locker(id), To: cashflow.External(), LockerKind: "adjust", ReceiptKind: "adjust"}
	default:
		return apperr.Validation("pick an adjustment type")
	}
	in.Amount = amt
	in.Reason = note
	in.ActorID = userID

	var rec *cashflow.Receipt
	err = appdb.WithTx(ctx, a.db, func(tx *sqlx.Tx) error {
		if bookExpense {
			desc := note
			e, err := a.s.expenses.CreateInTx(ctx, tx, expenses.CreateInput{
				Category:    "Bank charges",
				Amount:      amt.StringFixed(2),
				Description: &desc,
			}, userID)
			if err != nil {
				return err
			}
			in.Ref = &cashflow.Ref{Kind: "expense", ID: e.ID}
		}
		r, err := a.s.cashflow.MoveTx(ctx, tx, in)
		rec = r
		return err
	})
	if err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionUpdate, "locker", strconv.FormatInt(id, 10), "locker adjustment ("+c.FormValue("type")+")")
	return a.s.afterMoneyMove(c, rec)
}

// LockerLedger shows one locker's movements on a full page, filterable by date.
// (A safe's ledger can get long, so this is a page rather than a modal.)
func (a *adminUI) LockerLedger(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	l, err := a.s.lockers.Get(ctx, id)
	if err != nil {
		return err
	}
	// Reuse the shared report quick-pick range (today / this week / this month …)
	// plus the exact-date inputs. ResolveRange returns a [from, to) half-open
	// range — to is exclusive, matching the ledger query.
	preset := c.QueryParam("preset")
	from, to, fromStr, toStr, err := reports.ResolveRange(preset, c.QueryParam("from"), c.QueryParam("to"))
	if err != nil {
		return apperr.Validation(err.Error())
	}
	entries, err := a.s.lockers.Ledger(ctx, id, lockers.LedgerFilter{From: &from, To: &to})
	if err != nil {
		return err
	}
	netInRange := decimal.Zero
	for _, e := range entries {
		netInRange = netInRange.Add(e.BalanceDelta)
	}
	return response.RenderPage(c, adminpages.LockerLedgerPage(adminpages.LockerLedgerData{
		Symbol:     a.symbol(ctx),
		UserName:   middleware.CurrentUserName(c),
		Locker:     *l,
		Entries:    entries,
		Preset:     preset,
		From:       fromStr,
		To:         toStr,
		NetInRange: netInRange,
	}))
}
