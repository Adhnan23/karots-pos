package recharge

import (
	"net/http"
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/escpos"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

// Bills renders the admin bill-payment / get-money page, seeded with every
// pickable cash location (all active lockers + open tills).
func (a *adminUI) Bills(c echo.Context) error {
	ctx := c.Request().Context()
	choices, err := a.cashLocationChoices(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, AdminBillsPage(middleware.CurrentUserName(c), a.symbol(ctx), choices))
}

// BankTx records an admin bill payment / get-money. Unlike the cashier flow it
// moves between two freely-picked piles (account side + physical-cash side), each
// validated against the offered choices before any money moves. Every leg commits
// in ONE cashflow transaction so an overdraw on any pile rolls the whole thing
// back; the picked source's own allow_negative setting decides whether it may go
// below zero. Records a session-less recharge_bill_tx row for the slip + Bills
// receipts tab, then follows the shop's print policy.
func (a *adminUI) BankTx(c echo.Context) error {
	ctx := c.Request().Context()
	uid := middleware.CurrentUserID(c)

	typ := c.FormValue("type")
	if typ != "billpay" && typ != "getmoney" {
		return apperr.BadRequest("invalid transaction type")
	}

	choices, err := a.cashLocationChoices(ctx)
	if err != nil {
		return err
	}
	accountVal := strings.TrimSpace(c.FormValue("account"))
	cashVal := strings.TrimSpace(c.FormValue("cash"))
	if !refillSourceAllowed(choices, accountVal) || !refillSourceAllowed(choices, cashVal) {
		return apperr.Validation("choose valid cash locations")
	}
	if accountVal == cashVal {
		return apperr.Validation("the account and cash sides must be different places")
	}
	account, err := parseLocation(accountVal)
	if err != nil {
		return err
	}
	cash, err := parseLocation(cashVal)
	if err != nil {
		return err
	}

	amt, err := money.Parse(c.FormValue("amount"))
	if err != nil || !amt.IsPositive() {
		return apperr.Validation("amount must be positive")
	}
	svc := decimal.Zero
	if v := strings.TrimSpace(c.FormValue("service_charge")); v != "" {
		svc, err = money.Parse(v)
		if err != nil || svc.IsNegative() {
			return apperr.Validation("service charge must be zero or more")
		}
	}
	ref := strings.TrimSpace(c.FormValue("reference"))
	note := strings.TrimSpace(c.FormValue("note"))

	reason := txLabel(typ)
	if ref != "" {
		reason += " #" + ref
	}
	if note != "" {
		reason += " - " + note
	}
	biller := "Bill payment"
	if ref != "" {
		biller = "Bill " + ref
	}

	legs := buildBankLegs(typ, account, cash, amt, svc, biller)

	// Capture the account-side pile label from cashflow's own leg labelling, so the
	// slip names the account exactly as the CR- receipts do.
	var accountName string
	if err := appdb.WithTx(ctx, a.p.core.DB, func(tx *sqlx.Tx) error {
		for i, l := range legs {
			rec, err := a.p.cashflow.MoveTx(ctx, tx, cashflow.MoveInput{
				From: l.From, To: l.To, Amount: l.Amount, Reason: reason,
				ReceiptKind: typ, Party: l.Party, ActorID: uid,
			})
			if err != nil {
				return err
			}
			// billpay leg 0 is account→External (FromLabel = account); getmoney leg 1
			// is External→account (ToLabel = account).
			if typ == "billpay" && i == 0 {
				accountName = rec.FromLabel
			}
			if typ == "getmoney" && i == 1 {
				accountName = rec.ToLabel
			}
		}
		return nil
	}); err != nil {
		return err
	}

	// A physical drawer on either side → pop it (best-effort, setting-gated).
	if account.Kind == cashflow.KindTill || cash.Kind == cashflow.KindTill {
		if cfg, cerr := a.p.core.Settings.Get(ctx); cerr == nil && cfg != nil {
			escpos.KickDrawer(ctx, *cfg)
		}
	}

	bankLockerID := int64(0)
	if account.Kind == cashflow.KindLocker {
		bankLockerID = account.ID
	}
	billID, err := a.p.store.RecordBillTx(ctx, BillTxInput{
		SessionID: nil, BankLockerID: bankLockerID, BankName: accountName, Type: typ,
		Amount: amt, ServiceCharge: svc, Reference: ref, Note: note, CreatedBy: uid,
	})
	if err != nil {
		return err
	}

	// Follow the shop's print policy for the bill slip (mirrors the admin Refill):
	// AskToPrint on → the shared Print/Skip prompt; off → best-effort print now.
	// The form is hx-swap="none", so drive the UI over HX-Trigger.
	msg := txLabel(typ) + " recorded — " + accountName
	reprintURL := "/admin/recharge/bill/" + strconv.FormatInt(billID, 10) + "/print"
	if cfg, cerr := a.p.core.Settings.Get(ctx); cerr == nil && cfg != nil && cfg.AskToPrint {
		c.Response().Header().Set("HX-Trigger", response.PrintPrompt(msg, reprintURL, false))
		return c.NoContent(http.StatusOK)
	}
	if t, terr := a.p.store.BillTxByID(ctx, billID); terr == nil {
		_ = a.p.reprintBill(ctx, t) // best-effort: a printer hiccup never fails the move
	}
	c.Response().Header().Set("HX-Trigger", response.Toast(msg, "success"))
	return c.NoContent(http.StatusOK)
}
