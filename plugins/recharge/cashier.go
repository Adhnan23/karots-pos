package recharge

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/auth"
	"karots-pos/internal/features/cashregister"
	"karots-pos/internal/features/expenses"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"

	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

type cashierUI struct{ p *Plugin }

// Carriers returns the active carriers as JSON for the POS Reload popup.
func (h *cashierUI) Carriers(c echo.Context) error {
	cs, err := h.p.store.Carriers(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"data": cs})
}

// ReconData is the model for the cashier recharge reconciliation screen.
type ReconData struct {
	UserName      string
	Role          string
	ShowChangePin bool
	Symbol        string
	Session       *cashregister.Session
	Rows          []CarrierRecon
	Carriers      []Carrier
	Devices       []Device
}

func (h *cashierUI) showChangePin(c echo.Context) bool {
	if middleware.CurrentRole(c) != auth.RoleCashier {
		return true
	}
	cfg, err := h.p.core.Settings.Get(c.Request().Context())
	return err == nil && cfg.AllowCashierPinChange
}

// reconData gathers the full reconciliation model for the current cashier.
func (h *cashierUI) reconData(c echo.Context) (ReconData, error) {
	ctx := c.Request().Context()
	uid := middleware.CurrentUserID(c)
	sess, err := h.p.core.CashRegister.Current(ctx, uid)
	if err != nil {
		return ReconData{}, err
	}
	d := ReconData{
		UserName:      middleware.CurrentUserName(c),
		Role:          middleware.CurrentRole(c),
		ShowChangePin: h.showChangePin(c),
		Session:       sess,
	}
	if cfg, err := h.p.core.Settings.Get(ctx); err == nil {
		d.Symbol = cfg.CurrencySymbol
	}
	if sess != nil {
		if d.Rows, err = h.p.store.Reconciliation(ctx, sess.ID, uid, sess.OpenedAt); err != nil {
			return d, err
		}
	}
	if d.Carriers, err = h.p.store.Carriers(ctx); err != nil {
		return d, err
	}
	if d.Devices, err = h.p.store.Devices(ctx); err != nil {
		return d, err
	}
	return d, nil
}

// Recon renders the full reconciliation page.
func (h *cashierUI) Recon(c echo.Context) error {
	d, err := h.reconData(c)
	if err != nil {
		return err
	}
	return response.RenderPage(c, ReconPage(d))
}

// reconFragment re-renders the recon body (for HTMX swaps after an action).
func (h *cashierUI) reconFragment(c echo.Context, triggers ...string) error {
	d, err := h.reconData(c)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, ReconBody(d), triggers...)
}

// requireSession resolves the cashier's open drawer session or a 409.
func (h *cashierUI) requireSession(c echo.Context) (*cashregister.Session, error) {
	sess, err := h.p.core.CashRegister.Current(c.Request().Context(), middleware.CurrentUserID(c))
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, apperr.Conflict("open your cash drawer first")
	}
	return sess, nil
}

// SaveOpening stores each device's opening float at shift start.
func (h *cashierUI) SaveOpening(c echo.Context) error {
	return h.saveFloats(c, "opening_", h.p.store.SaveOpening, "Opening floats saved")
}

// SaveClosing stores each device's counted closing float and reveals bonus/loss.
func (h *cashierUI) SaveClosing(c echo.Context) error {
	return h.saveFloats(c, "closing_", h.p.store.SaveClosing, "Closing floats saved")
}

func (h *cashierUI) saveFloats(c echo.Context, prefix string, save func(context.Context, int64, int64, decimal.Decimal) error, msg string) error {
	ctx := c.Request().Context()
	sess, err := h.requireSession(c)
	if err != nil {
		return err
	}
	form, err := c.FormParams()
	if err != nil {
		return apperr.BadRequest("invalid form")
	}
	for key, vals := range form {
		id, ok := strings.CutPrefix(key, prefix)
		if !ok || len(vals) == 0 || strings.TrimSpace(vals[0]) == "" {
			continue
		}
		did, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			continue
		}
		amt, err := money.Parse(vals[0])
		if err != nil || amt.IsNegative() {
			continue
		}
		if err := save(ctx, sess.ID, did, amt); err != nil {
			return err
		}
	}
	return h.reconFragment(c, response.Toast(msg, "success"))
}

// Tx records a money transaction (deposit / withdrawal / bill-pay / topup): it
// mirrors the cash drawer, books a supplier expense for top-ups, writes the
// ledger row, and prints a slip for the cash-handling types.
func (h *cashierUI) Tx(c echo.Context) error {
	ctx := c.Request().Context()
	uid := middleware.CurrentUserID(c)
	sess, err := h.requireSession(c)
	if err != nil {
		return err
	}

	typ := c.FormValue("type")
	kind, ok := txKinds[typ]
	if !ok || typ == "wallet_in" { // wallet_in only via the sale path
		return apperr.BadRequest("invalid transaction type")
	}
	carrierID, err := strconv.ParseInt(c.FormValue("carrier_id"), 10, 64)
	if err != nil {
		return apperr.Validation("choose a carrier")
	}
	amt, err := money.Parse(c.FormValue("amount"))
	if err != nil || !amt.IsPositive() {
		return apperr.Validation("amount must be positive")
	}
	ref := strings.TrimSpace(c.FormValue("reference"))
	note := strings.TrimSpace(c.FormValue("note"))
	var deviceID *int64
	if v := strings.TrimSpace(c.FormValue("device_id")); v != "" {
		if id, e := strconv.ParseInt(v, 10, 64); e == nil {
			deviceID = &id
		}
	}

	carrier := h.carrierName(ctx, carrierID)
	if carrier == "" {
		return apperr.Validation("unknown carrier")
	}
	reason := carrier + " " + txLabel(typ)
	if ref != "" {
		reason += " #" + ref
	}

	// 1) Mirror the cash drawer (guards withdrawals against the drawer balance).
	switch kind.cashSign {
	case +1:
		if _, err := h.p.core.CashRegister.PayIn(ctx, uid, cashregister.MovementInput{Amount: amt.String(), Reason: reason}); err != nil {
			return err
		}
	case -1:
		if _, err := h.p.core.CashRegister.Withdraw(ctx, uid, cashregister.MovementInput{Amount: amt.String(), Reason: reason}); err != nil {
			return err
		}
	}

	// 2) A supplier float top-up is also a shop expense.
	var expenseID *int64
	if typ == "topup" {
		desc := carrier + " float top-up"
		exp, err := h.p.core.Expenses.Create(ctx, expenses.CreateInput{
			Category: "Float top-up", Amount: amt.String(), Description: &desc,
		}, uid)
		if err != nil {
			return err
		}
		expenseID = &exp.ID
	}

	// 3) Ledger.
	if _, err := h.p.store.RecordTransaction(ctx, TxInput{
		SessionID: sess.ID, CarrierID: carrierID, DeviceID: deviceID, Type: typ,
		Amount: amt, ExpenseID: expenseID, Reference: ref, Note: note, CreatedBy: uid,
	}); err != nil {
		return err
	}

	// 4) Print a slip for the cash-handling types.
	if typ == "deposit" || typ == "withdrawal" || typ == "billpay" {
		h.p.printSlip(ctx, typ, carrier, amt, ref)
	}

	return h.reconFragment(c, response.Toast(carrier+" "+txLabel(typ)+" recorded", "success"))
}

// Wallet credits a carrier's float when a product sale was paid by a wallet
// transfer (eZ Cash / mCash). Posted by the POS after checkout. No cash drawer
// movement — the e-money landed in the carrier float, not the till.
func (h *cashierUI) Wallet(c echo.Context) error {
	ctx := c.Request().Context()
	uid := middleware.CurrentUserID(c)
	sess, err := h.requireSession(c)
	if err != nil {
		return err
	}
	var in struct {
		SaleID    int64  `json:"sale_id"`
		CarrierID int64  `json:"carrier_id"`
		Amount    string `json:"amount"`
	}
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	amt, err := money.Parse(in.Amount)
	if err != nil || !amt.IsPositive() {
		return apperr.Validation("amount must be positive")
	}
	if in.CarrierID == 0 {
		return apperr.Validation("carrier is required")
	}
	var saleID *int64
	if in.SaleID != 0 {
		saleID = &in.SaleID
	}
	if _, err := h.p.store.RecordTransaction(ctx, TxInput{
		SessionID: sess.ID, CarrierID: in.CarrierID, Type: "wallet_in",
		Amount: amt, SaleID: saleID, CreatedBy: uid,
	}); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

func (h *cashierUI) carrierName(ctx context.Context, id int64) string {
	var n string
	_ = h.p.store.db.GetContext(ctx, &n, `SELECT name FROM recharge_carriers WHERE id = $1`, id)
	return n
}
