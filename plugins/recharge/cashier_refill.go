package recharge

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/expenses"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"
	adminfragments "karots-pos/templates/fragments/admin"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

// ============================ Cashier supplier float refill ============================
//
// Buying reload float from a carrier used to be the "Buy float from supplier"
// (topup) card on the cashier Float form, mixed in with deposit/withdrawal — so a
// cashier sometimes fired the wrong transaction — and it could only draw from the
// drawer. It now lives under the cashier Suppliers section (via the
// plugin.CashierSupplierAction hook) with a proper cash-source picker limited to
// what this cashier may touch. The money-move itself is the admin refill path.

// cashierRefillSources lists where a cashier may pay a refill from: their own open
// drawer, then the lockers the owner marked usable by cashiers. Mirrors core web's
// cashierCashSources so the counter refill obeys the same restriction as every
// other counter money-out (supplier pay, counter expense).
func (h *cashierUI) cashierRefillSources(ctx context.Context, userID int64, userName string) ([]adminfragments.LocationChoice, error) {
	sym := h.symbol(ctx)
	out := []adminfragments.LocationChoice{{
		Value: "till:" + strconv.FormatInt(userID, 10),
		Label: "My drawer — " + userName,
		Group: "Till",
	}}
	rows, err := h.p.lockers.ListForCashier(ctx)
	if err != nil {
		return nil, err
	}
	for _, l := range rows {
		out = append(out, adminfragments.LocationChoice{
			Value: "locker:" + strconv.FormatInt(l.ID, 10),
			Label: l.Name + " (" + money.Format(sym, l.Balance) + ")",
			Group: "Lockers",
		})
	}
	return out, nil
}

// refillSourceAllowed reports whether the posted cash-source value is one this
// cashier is actually allowed to pay from. Filtering the picker is not enough on
// its own — a cashier could hand-craft a "locker:N" for an off-limits safe — so
// the POST re-derives the allowed set and checks membership before any money
// moves (the core supplier counter learned this the hard way).
func refillSourceAllowed(choices []adminfragments.LocationChoice, value string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		return false
	}
	for _, c := range choices {
		if c.Value == v {
			return true
		}
	}
	return false
}

// RefillForm renders the counter refill modal, seeded with the cashier's allowed
// cash sources.
func (h *cashierUI) RefillForm(c echo.Context) error {
	sources, err := h.cashierRefillSources(c.Request().Context(),
		middleware.CurrentUserID(c), middleware.CurrentUserName(c))
	if err != nil {
		return err
	}
	return response.RenderFragment(c, CashierRefillForm(sources))
}

// Refill records a counter supplier float top-up: it increases a device's float,
// books a shop expense, and pays the supplier from the picked cash source — but
// only a source this cashier is allowed to touch (validated server-side). The
// three legs commit in ONE transaction, so an overdrawn source rolls it all back.
// This is the admin Refill path with the cashier source guard bolted on.
func (h *cashierUI) Refill(c echo.Context) error {
	ctx := c.Request().Context()
	uid := middleware.CurrentUserID(c)

	deviceID, err := strconv.ParseInt(c.FormValue("device_id"), 10, 64)
	if err != nil || deviceID == 0 {
		return apperr.Validation("choose a device")
	}
	amt, err := money.Parse(c.FormValue("amount"))
	if err != nil || !amt.IsPositive() {
		return apperr.Validation("enter a valid amount")
	}

	// The source picker is limited to this cashier's drawer + cashier-access
	// lockers; the POST re-checks membership so a forged locker id can't slip past.
	src := strings.TrimSpace(c.FormValue("source"))
	sources, err := h.cashierRefillSources(ctx, uid, middleware.CurrentUserName(c))
	if err != nil {
		return err
	}
	if !refillSourceAllowed(sources, src) {
		return apperr.Forbidden("you can't take cash from there")
	}
	from, err := parseLocation(src)
	if err != nil {
		return err
	}

	carrierID, err := h.p.store.CarrierOfDevice(ctx, deviceID)
	if err != nil {
		return err
	}
	if carrierID == 0 {
		return apperr.Validation("unknown device")
	}
	ref := strings.TrimSpace(c.FormValue("reference"))
	note := strings.TrimSpace(c.FormValue("note"))

	supplier := h.p.store.CarrierName(ctx, carrierID)
	desc := supplier + " supplier float refill"
	reason := desc
	if ref != "" {
		reason += " #" + ref
	}
	// Attribute the float side to the till that actually has this device's float
	// open (so the working cashier sees it live); session-less (0) if none.
	sessionID, err := h.p.store.OpenDeviceSession(ctx, deviceID)
	if err != nil {
		return err
	}

	var rec *cashflow.Receipt
	var txID int64
	err = appdb.WithTx(ctx, h.p.core.DB, func(tx *sqlx.Tx) error {
		exp, err := h.p.core.Expenses.CreateInTx(ctx, tx, expenses.CreateInput{
			Category: "Float top-up", Amount: amt.String(), Description: &desc,
		}, uid)
		if err != nil {
			return err
		}
		r, err := h.p.cashflow.MoveTx(ctx, tx, cashflow.MoveInput{
			From: from, To: cashflow.External(),
			Amount:      amt,
			Reason:      reason,
			ReceiptKind: "expense",
			Party:       supplier,
			Ref:         &cashflow.Ref{Kind: "expense", ID: exp.ID},
			ActorID:     uid,
		})
		if err != nil {
			return err
		}
		rec = r
		expID := exp.ID
		txID, err = h.p.store.RecordTransactionTx(ctx, tx, TxInput{
			SessionID: sessionID, CarrierID: carrierID, DeviceID: deviceID, Type: "refill",
			Amount: amt, ExpenseID: &expID, Reference: ref, Note: note, CreatedBy: uid,
		})
		return err
	})
	if err != nil {
		return err
	}

	// Follow the shop's print policy for the RL- refill slip, like the counter
	// expense: AskToPrint on → the shared Print / Skip prompt; off → best-effort
	// print now. The form is hx-swap="none", so we drive the UI over HX-Trigger.
	msg := "Float refilled — paid from " + rec.FromLabel
	reprintURL := "/cashier/recharge/tx/" + strconv.FormatInt(txID, 10) + "/print"
	if cfg, cerr := h.p.core.Settings.Get(ctx); cerr == nil && cfg != nil && cfg.AskToPrint {
		c.Response().Header().Set("HX-Trigger", response.PrintPrompt(msg, reprintURL, false))
		return c.NoContent(http.StatusOK)
	}
	if t, terr := h.p.store.TxByID(ctx, txID); terr == nil {
		_ = h.p.reprintTx(ctx, t) // best-effort: a printer hiccup never fails the refill
	}
	c.Response().Header().Set("HX-Trigger", response.Toast(msg, "success"))
	return c.NoContent(http.StatusOK)
}
