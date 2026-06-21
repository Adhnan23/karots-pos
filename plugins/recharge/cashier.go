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
		if d.Rows, err = h.p.store.Reconciliation(ctx, sess.ID); err != nil {
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
	if !ok || typ == "wallet_in" || typ == "reload" { // wallet_in/reload only via the sale path
		return apperr.BadRequest("invalid transaction type")
	}
	deviceID, err := strconv.ParseInt(c.FormValue("device_id"), 10, 64)
	if err != nil || deviceID == 0 {
		return apperr.Validation("choose a device")
	}
	amt, err := money.Parse(c.FormValue("amount"))
	if err != nil || !amt.IsPositive() {
		return apperr.Validation("amount must be positive")
	}
	ref := strings.TrimSpace(c.FormValue("reference"))
	note := strings.TrimSpace(c.FormValue("note"))

	// The device is the unit of float — derive the carrier from it so they can't
	// disagree.
	carrierID, err := h.p.store.CarrierOfDevice(ctx, deviceID)
	if err != nil {
		return err
	}
	if carrierID == 0 {
		return apperr.Validation("unknown device")
	}
	carrier := h.carrierName(ctx, carrierID)
	if carrier == "" {
		return apperr.Validation("unknown carrier")
	}

	// Hard-block: a deposit/bill-pay that would push the device float below zero.
	if decreasesFloat(typ) {
		over, err := h.p.store.wouldOverdraw(ctx, sess.ID, deviceID, amt)
		if err != nil {
			return err
		}
		if over {
			return apperr.Conflict("not enough float on this device")
		}
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

// Devices lists active devices with their live float balance for the dynamic
// pickers (reload popup, wallet tender, tx form). With carrier_id it narrows to
// one carrier; without, it returns every carrier's devices (the flat wallet
// picker + checkout overdraw map). Requires an open drawer — the balance is
// relative to the current session.
func (h *cashierUI) Devices(c echo.Context) error {
	sess, err := h.requireSession(c)
	if err != nil {
		return err
	}
	carrierID, _ := strconv.ParseInt(c.QueryParam("carrier_id"), 10, 64) // 0 = all
	rows, err := h.p.store.DevicesWithBalance(c.Request().Context(), sess.ID, carrierID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

// Reload records the float decrease for an airtime sale, attributed to a specific
// device. Posted by the POS after the core sale commits (the cash was collected
// by the sale's payment, so this ledger row is cash-neutral). The overdraw
// hard-block runs client-side before checkout.
func (h *cashierUI) Reload(c echo.Context) error {
	ctx := c.Request().Context()
	uid := middleware.CurrentUserID(c)
	sess, err := h.requireSession(c)
	if err != nil {
		return err
	}
	var in struct {
		SaleID   int64  `json:"sale_id"`
		DeviceID int64  `json:"device_id"`
		Amount   string `json:"amount"`
	}
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	amt, err := money.Parse(in.Amount)
	if err != nil || !amt.IsPositive() {
		return apperr.Validation("amount must be positive")
	}
	carrierID, saleID, err := h.deviceTender(ctx, in.DeviceID, in.SaleID)
	if err != nil {
		return err
	}
	if _, err := h.p.store.RecordTransaction(ctx, TxInput{
		SessionID: sess.ID, CarrierID: carrierID, DeviceID: in.DeviceID, Type: "reload",
		Amount: amt, SaleID: saleID, CreatedBy: uid,
	}); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// Wallet credits a device's float when a product sale was paid by a wallet
// transfer (eZ Cash / mCash). Posted by the POS after checkout. No cash drawer
// movement — the e-money landed in the device float, not the till.
func (h *cashierUI) Wallet(c echo.Context) error {
	ctx := c.Request().Context()
	uid := middleware.CurrentUserID(c)
	sess, err := h.requireSession(c)
	if err != nil {
		return err
	}
	var in struct {
		SaleID   int64  `json:"sale_id"`
		DeviceID int64  `json:"device_id"`
		Amount   string `json:"amount"`
	}
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	amt, err := money.Parse(in.Amount)
	if err != nil || !amt.IsPositive() {
		return apperr.Validation("amount must be positive")
	}
	carrierID, saleID, err := h.deviceTender(ctx, in.DeviceID, in.SaleID)
	if err != nil {
		return err
	}
	if _, err := h.p.store.RecordTransaction(ctx, TxInput{
		SessionID: sess.ID, CarrierID: carrierID, DeviceID: in.DeviceID, Type: "wallet_in",
		Amount: amt, SaleID: saleID, CreatedBy: uid,
	}); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// deviceTender validates a device-attributed post from the POS (reload/wallet):
// it resolves the carrier from the device and normalises the optional sale id.
func (h *cashierUI) deviceTender(ctx context.Context, deviceID, sale int64) (carrierID int64, saleID *int64, err error) {
	if deviceID == 0 {
		return 0, nil, apperr.Validation("device is required")
	}
	carrierID, err = h.p.store.CarrierOfDevice(ctx, deviceID)
	if err != nil {
		return 0, nil, err
	}
	if carrierID == 0 {
		return 0, nil, apperr.Validation("unknown device")
	}
	if sale != 0 {
		saleID = &sale
	}
	return carrierID, saleID, nil
}

func (h *cashierUI) carrierName(ctx context.Context, id int64) string {
	var n string
	_ = h.p.store.db.GetContext(ctx, &n, `SELECT name FROM recharge_carriers WHERE id = $1`, id)
	return n
}
