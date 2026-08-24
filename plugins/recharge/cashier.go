package recharge

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/auth"
	"karots-pos/internal/escpos"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/cashregister"
	"karots-pos/internal/features/customers"
	"karots-pos/internal/features/lockers"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"
	"karots-pos/templates/shared"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

type cashierUI struct{ p *Plugin }

// menuNode is one entry in the cashier menu-node protocol: a folder drills
// further in via ChildrenURL, an "amount" leaf opens an inline amount step
// that POSTs AddURL, and a "detail" leaf opens an inline HTML fragment at
// DetailURL. See internal/plugin/hooks.go (CashierMenuRoot) for the root hook
// this subtree hangs off.
type menuNode struct {
	Kind        string         `json:"kind"` // "folder" | "leaf"
	Name        string         `json:"name"`
	Emoji       string         `json:"emoji,omitempty"`
	ChildrenURL string         `json:"children_url,omitempty"` // folder
	Action      string         `json:"action,omitempty"`       // leaf: "amount" | "detail"
	AddURL      string         `json:"add_url,omitempty"`      // amount leaf
	DetailURL   string         `json:"detail_url,omitempty"`   // detail leaf
	Hint        string         `json:"hint,omitempty"`         // small muted sub-line on the card (e.g. live balance)
	Meta        map[string]any `json:"meta,omitempty"`
}

// reloadDeviceNode builds the amount leaf for one device's reload balance row.
// Meta carries the carrier/device ids the client echoes back to MenuReloadAdd
// unchanged.
func reloadDeviceNode(carrierID int64, d DeviceBalanceRow, symbol string) menuNode {
	label := d.Label
	if d.Number != "" {
		label += " · " + d.Number
	}
	return menuNode{
		Kind: "leaf", Name: "Reload — " + label, Action: "amount",
		AddURL: "/cashier/recharge/menu/reload",
		Hint:   "Float " + money.Format(symbol, d.Balance),
		Meta:   map[string]any{"carrier_id": carrierID, "device_id": d.ID},
	}
}

// parseAmount validates an amount-step string, shared by every menu "amount"
// leaf handler.
func parseAmount(s string) (decimal.Decimal, error) {
	v, err := money.Parse(s)
	if err != nil || !v.IsPositive() {
		return decimal.Zero, apperr.Validation("enter an amount greater than zero")
	}
	return v, nil
}

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
	Banks         []lockers.Locker
	// LogoutMode is set when the page was reached via /cashier/recharge?logout=1 —
	// the user tried to log out with a float still open. The page then shows a
	// banner and, once the last float is closed, routes on to /logout.
	LogoutMode bool
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
		LogoutMode:    c.QueryParam("logout") == "1",
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
	if d.Banks, err = h.p.bankLockers(ctx); err != nil {
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
	if _, err := h.saveFloats(c, "opening_", h.p.store.SaveOpening); err != nil {
		return err
	}
	return h.reconFragment(c, response.Toast("Opening floats saved", "success"))
}

// SaveClosing stores each device's counted closing float and reveals bonus/loss.
// When the close was triggered from a logout (logout=1) and no float remains open
// under the till session, it sends the user on to /logout so the original sign-out
// completes instead of leaving them on the recon page.
func (h *cashierUI) SaveClosing(c echo.Context) error {
	sess, err := h.saveFloats(c, "closing_", h.p.store.SaveClosing)
	if err != nil {
		return err
	}
	if c.FormValue("logout") == "1" {
		open, err := h.p.store.HasOpenFloat(c.Request().Context(), sess.ID)
		if err == nil && !open {
			c.Response().Header().Set("HX-Redirect", "/logout")
			return c.NoContent(http.StatusOK)
		}
	}
	return h.reconFragment(c, response.Toast("Closing floats saved", "success"))
}

func (h *cashierUI) saveFloats(c echo.Context, prefix string, save func(context.Context, int64, int64, decimal.Decimal) error) (*cashregister.Session, error) {
	ctx := c.Request().Context()
	sess, err := h.requireSession(c)
	if err != nil {
		return nil, err
	}
	form, err := c.FormParams()
	if err != nil {
		return nil, apperr.BadRequest("invalid form")
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
			return nil, err
		}
	}
	return sess, nil
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
	// This form handles deposit / withdrawal only. wallet_in & reload flow through
	// the sale path; billpay is a bank-card operation (see BankTx); topup (buying
	// supplier float) moved to the cashier Suppliers section (see Refill).
	if !ok || typ == "wallet_in" || typ == "reload" || typ == "billpay" || typ == "topup" {
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
	// Optional service charge — always collected in cash on top of the principal.
	svc := decimal.Zero
	if v := strings.TrimSpace(c.FormValue("service_charge")); v != "" {
		svc, err = money.Parse(v)
		if err != nil || svc.IsNegative() {
			return apperr.Validation("service charge must be zero or more")
		}
	}
	// Optional cash-given (deposit only) so the slip prints the change, like a sale.
	cashGiven := decimal.Zero
	if v := strings.TrimSpace(c.FormValue("cash_given")); v != "" {
		if cashGiven, err = money.Parse(v); err != nil || cashGiven.IsNegative() {
			return apperr.Validation("cash given must be zero or more")
		}
	}
	if typ != "deposit" { // only a cash-in has a tender; withdrawal hands cash out
		cashGiven = decimal.Zero
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

	// A bank card holds no tracked float: the cash still moves, but there is no
	// float to decrease and no overdraw to guard against.
	tracksFloat, err := h.p.store.DeviceTracksFloat(ctx, deviceID)
	if err != nil {
		return err
	}

	// Hard-block: a deposit/bill-pay that would push the device float below zero
	// (skipped for bank cards, which have no float balance).
	if tracksFloat && decreasesFloat(typ) {
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

	// 1b) The service charge is extra cash into the drawer (shop earnings).
	if svc.IsPositive() {
		if _, err := h.p.core.CashRegister.PayIn(ctx, uid, cashregister.MovementInput{Amount: svc.String(), Reason: reason + " service charge"}); err != nil {
			return err
		}
	}

	// 2) Ledger (deposit / withdrawal only — topup moved to the Suppliers refill).
	txID, err := h.p.store.RecordTransaction(ctx, TxInput{
		SessionID: sess.ID, CarrierID: carrierID, DeviceID: deviceID, Type: typ,
		Amount: amt, Reference: ref, Note: note, CreatedBy: uid,
		Untracked: !tracksFloat, ServiceCharge: svc, CashGiven: cashGiven,
	})
	if err != nil {
		return err
	}

	// Cash crossed the physical drawer (deposit in, withdrawal out) — pop it, like
	// bill-pay does. Best-effort and setting-gated. This path never used to kick.
	if kind.cashSign != 0 {
		if cfg, cerr := h.p.core.Settings.Get(ctx); cerr == nil && cfg != nil {
			escpos.KickDrawer(ctx, *cfg)
		}
	}

	msg := carrier + " " + txLabel(typ) + " recorded"
	// 3) Deposit / withdrawal print a slip under the print policy.
	if typ == "deposit" || typ == "withdrawal" {
		return h.printPolicy(c, "/cashier/recharge/tx/"+strconv.FormatInt(txID, 10)+"/print",
			func(ctx context.Context) error {
				t, err := h.p.store.TxByID(ctx, txID)
				if err != nil {
					return err
				}
				return h.p.reprintTx(ctx, t)
			}, msg)
	}
	return h.reconFragment(c, response.Toast(msg, "success"))
}

// Devices lists active devices with their live float balance for the dynamic
// pickers (reload popup, wallet tender, tx form). With carrier_id it narrows to
// one carrier; without, it returns every carrier's devices (the flat wallet
// picker + checkout overdraw map). Requires an open drawer — the balance is
// relative to the current session.
func (h *cashierUI) Devices(c echo.Context) error {
	// Balances are session-scoped, but the POS reload panel fetches this on page
	// load — possibly before the drawer is open. Return an empty list (not a 409)
	// in that case so the panel shows nothing quietly; it re-fetches on the
	// register-opened event. The reload action itself still requires a session.
	sess, err := h.p.core.CashRegister.Current(c.Request().Context(), middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	if sess == nil {
		return c.JSON(http.StatusOK, map[string]any{"data": []any{}})
	}
	carrierID, _ := strconv.ParseInt(c.QueryParam("carrier_id"), 10, 64) // 0 = all
	purpose := c.QueryParam("for")                                       // "recharge" | "money" | "" (all)
	rows, err := h.p.store.DevicesWithBalance(c.Request().Context(), sess.ID, carrierID, purpose)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

// symbol resolves the shop currency symbol for fragment renders ("" on error).
func (h *cashierUI) symbol(ctx context.Context) string {
	if cfg, err := h.p.core.Settings.Get(ctx); err == nil && cfg != nil {
		return cfg.CurrencySymbol
	}
	return ""
}

// TxView renders one float-transaction slip as the shared thermal receipt page
// (the View link on the cashier "Reload" receipts tab) — identical shell to the
// core Cash / Credit receipt views, only the print/switch URL base differs.
func (h *cashierUI) TxView(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	t, err := h.p.store.TxByID(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := h.p.core.Settings.Get(ctx)
	if err != nil {
		return err
	}
	base := "/cashier/recharge/tx/" + strconv.FormatInt(t.ID, 10)
	thermal := shared.ThermalFrom(cfg.ReceiptWidth, c.QueryParam("size"), "Slip "+floatNo(t.ID), base, base+"/print")
	return response.RenderPage(c, TxSlipPage(*cfg, thermal, t))
}

// BillView renders one bill-payment / get-money slip as the shared thermal receipt
// page (the View link on the cashier "Bills" receipts tab).
func (h *cashierUI) BillView(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	t, err := h.p.store.BillTxByID(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := h.p.core.Settings.Get(ctx)
	if err != nil {
		return err
	}
	base := "/cashier/recharge/bill/" + strconv.FormatInt(t.ID, 10)
	thermal := shared.ThermalFrom(cfg.ReceiptWidth, c.QueryParam("size"), "Slip "+billNo(t.ID), base, base+"/print")
	return response.RenderPage(c, BillSlipPage(*cfg, thermal, t))
}

// ReceiptsBill renders the "Bills" receipts tab (bill payments / get-money) for the
// cashier Receipts page, with cashier-scoped reprint links.
func (h *cashierUI) ReceiptsBill(c echo.Context) error {
	ctx := c.Request().Context()
	f, preset, fromStr, toStr, err := receiptsRange(c)
	if err != nil {
		return err
	}
	rows, err := h.p.store.BillLedger(ctx, f)
	if err != nil {
		return err
	}
	vm := ReceiptsTabVM{
		Symbol: h.symbol(ctx), Preset: preset, From: fromStr, To: toStr,
		Action:      "/cashier/recharge/receipts/bill",
		ReprintBase: "/cashier/recharge/bill/", ViewBase: "/cashier/recharge/bill/",
	}
	return response.RenderFragment(c, BillReceiptsTab(vm, rows))
}

// ReceiptsFloat renders the "Reload" receipts tab (float deposit/withdrawal/top-up).
func (h *cashierUI) ReceiptsFloat(c echo.Context) error {
	ctx := c.Request().Context()
	f, preset, fromStr, toStr, err := receiptsRange(c)
	if err != nil {
		return err
	}
	rows, err := h.p.store.Ledger(ctx, f)
	if err != nil {
		return err
	}
	vm := ReceiptsTabVM{
		Symbol: h.symbol(ctx), Preset: preset, From: fromStr, To: toStr,
		Action:      "/cashier/recharge/receipts/recharge",
		ReprintBase: "/cashier/recharge/tx/", ViewBase: "/cashier/recharge/tx/",
	}
	return response.RenderFragment(c, FloatReceiptsTab(vm, rows))
}

// Banks lists the active core kind="bank" lockers with their live balance for the
// cashier's bill-pay / get-money picker. A "bank" is a plain core locker managed
// under Money → Cash Lockers — the plugin only reads & moves them.
func (h *cashierUI) Banks(c echo.Context) error {
	rows, err := h.p.bankLockers(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

// accountRow is one selectable source for a bill-pay / get-money: either a core
// bank locker or a money-usable device float. The client picks by (kind, id).
type accountRow struct {
	Kind    string          `json:"kind"` // "bank" | "device"
	ID      int64           `json:"id"`
	Name    string          `json:"name"`
	Balance decimal.Decimal `json:"balance"`
}

// Accounts lists every account a cashier can pay a bill from / receive money into:
// the active cashier-accessible bank lockers PLUS the money-usable (for_money)
// devices with their live float. Device balances are session-scoped, so with no
// open drawer only banks are returned.
func (h *cashierUI) Accounts(c echo.Context) error {
	ctx := c.Request().Context()
	banks, err := h.p.bankLockers(ctx)
	if err != nil {
		return err
	}
	out := make([]accountRow, 0, len(banks))
	for _, b := range banks {
		out = append(out, accountRow{Kind: "bank", ID: b.ID, Name: b.Name, Balance: b.Balance})
	}
	if sess, _ := h.p.core.CashRegister.Current(ctx, middleware.CurrentUserID(c)); sess != nil {
		devs, err := h.p.store.DevicesWithBalance(ctx, sess.ID, 0, "money")
		if err != nil {
			return err
		}
		for _, d := range devs {
			out = append(out, accountRow{Kind: "device", ID: d.ID, Name: d.Carrier + " · " + d.Label, Balance: d.Balance})
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

// moneyDevice loads one money-usable device with its live session float, or a
// validation error if the id isn't an active for_money device (guards a forged
// account id the picker filter wouldn't have offered).
func (h *cashierUI) moneyDevice(ctx context.Context, sessionID, deviceID int64) (*DeviceBalanceRow, error) {
	rows, err := h.p.store.DevicesWithBalance(ctx, sessionID, 0, "money")
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].ID == deviceID {
			return &rows[i], nil
		}
	}
	return nil, apperr.Validation("choose a valid account")
}

// BankTx records a bill payment or a get-money done by the cashier against a core
// bank locker, moving every leg through cashflow.Move (so each leg gets a CR-
// receipt and shows in core Cash Flow / net position):
//
//	billpay  — bank locker → External(biller) for the bill (bank down, overdraw-
//	           guarded), then External(customer) → Till for bill + service charge
//	           (cash in). The shop keeps the service charge.
//	getmoney — Till → External(customer) for the amount (cash out, drawer-guarded),
//	           External → bank locker for the amount (bank up), then a service
//	           charge is extra cash into the drawer.
//
// All legs commit in ONE transaction (cashflow.MoveTx over a shared tx) so a
// drawer/bank overdraw rolls the whole thing back — never a partial money move.
func (h *cashierUI) BankTx(c echo.Context) error {
	ctx := c.Request().Context()
	uid := middleware.CurrentUserID(c)
	sess, err := h.requireSession(c)
	if err != nil {
		return err
	}

	typ := c.FormValue("type")
	if typ != "billpay" && typ != "getmoney" {
		return apperr.BadRequest("invalid transaction type")
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
	// Optional cash-given (cash bill-pay only) so the slip prints the change.
	cashGiven := decimal.Zero
	if v := strings.TrimSpace(c.FormValue("cash_given")); v != "" {
		if cashGiven, err = money.Parse(v); err != nil || cashGiven.IsNegative() {
			return apperr.Validation("cash given must be zero or more")
		}
	}
	// On credit: the whole total goes on a customer's account instead of cash.
	// ponytail: no credit-limit check here and the debt won't itemize on the core
	// customer Statement (which reconstructs from sales/returns/payments) — the
	// balance and Credit Collection settlement are correct. Add a statement
	// contributor hook + limit guard if bills-on-credit grow beyond a courtesy.
	onCredit := c.FormValue("on_credit") != ""
	custID, _ := strconv.ParseInt(c.FormValue("customer_id"), 10, 64)
	if onCredit && custID <= 0 {
		return apperr.Validation("choose a customer to put this on their account")
	}
	if typ != "billpay" || onCredit { // no customer cash tender on cash-out or credit
		cashGiven = decimal.Zero
	}
	ref := strings.TrimSpace(c.FormValue("reference"))
	note := strings.TrimSpace(c.FormValue("note"))
	total := amt.Add(svc)

	reason := txLabel(typ)
	if ref != "" {
		reason += " #" + ref
	}
	if note != "" {
		reason += " - " + note
	}

	// Resolve the account. Every flow moves an account EXCEPT a credit get-money,
	// which is a pure cash advance (cash out now, customer owes) with no account.
	acctKind := c.FormValue("account_kind")
	acctID, _ := strconv.ParseInt(c.FormValue("account_id"), 10, 64)
	var (
		bankID, deviceID int64
		acctName         = "On account"
		bank             *lockers.Locker
		dev              *DeviceBalanceRow
	)
	if !(onCredit && typ == "getmoney") {
		switch acctKind {
		case "bank":
			if bank, err = h.p.lockers.Get(ctx, acctID); err != nil || !bankUsableByCashier(bank) {
				return apperr.Validation("choose a valid account")
			}
			bankID, acctName = bank.ID, bank.Name
		case "device":
			if dev, err = h.moneyDevice(ctx, sess.ID, acctID); err != nil {
				return err
			}
			deviceID, acctName = dev.ID, dev.Carrier+" · "+dev.Label
		default:
			return apperr.Validation("choose an account")
		}
	}
	reason = acctName + " " + reason

	// Move the money. Bank accounts move through core cashflow (CR- receipts);
	// device floats move through the device ledger + cash register. Credit replaces
	// the customer cash leg with a charge to their account.
	switch {
	case bankID != 0:
		err = h.moveBillBank(ctx, uid, bankID, typ, amt, svc, total, onCredit, custID, reason, ref)
	case deviceID != 0:
		err = h.moveBillDevice(ctx, uid, sess.ID, dev, typ, amt, svc, total, onCredit, custID, reason)
	default: // credit get-money: cash advance, no account
		err = h.moveCashAdvance(ctx, uid, amt, total, custID, reason)
	}
	if err != nil {
		return err
	}

	// Kick the drawer only when physical cash actually crossed it (a credit bill-pay
	// takes no cash in). Best-effort and setting-gated.
	if !(onCredit && typ == "billpay") {
		if cfg, cerr := h.p.core.Settings.Get(ctx); cerr == nil && cfg != nil {
			escpos.KickDrawer(ctx, *cfg)
		}
	}

	// Log the customer-facing detail (balance-free) for the slip reprint and the
	// "Bill" receipts tab. The money itself lives in the ledgers moved above.
	billID, err := h.p.store.RecordBillTx(ctx, BillTxInput{
		SessionID: &sess.ID, BankLockerID: bankID, DeviceID: deviceID, CustomerID: creditID(onCredit, custID),
		BankName: acctName, Type: typ, Amount: amt, ServiceCharge: svc, CashGiven: cashGiven,
		Reference: ref, Note: note, CreatedBy: uid,
	})
	if err != nil {
		return err
	}

	msg := acctName + " " + txLabel(typ) + " recorded"
	return h.printPolicy(c, "/cashier/recharge/bill/"+strconv.FormatInt(billID, 10)+"/print",
		func(ctx context.Context) error {
			t, err := h.p.store.BillTxByID(ctx, billID)
			if err != nil {
				return err
			}
			return h.p.reprintBill(ctx, t)
		}, msg)
}

// creditID returns the customer id only when the bill was actually put on credit,
// so a customer merely selected (but paid cash) isn't stamped as owing it.
func creditID(onCredit bool, custID int64) int64 {
	if onCredit {
		return custID
	}
	return 0
}

// moveBillBank moves a bill-pay / get-money against a core bank locker via cashflow
// (every leg a CR- receipt). On credit the customer-cash leg is dropped and the
// total is charged to the customer's account inside the SAME tx as the bank leg,
// so a failure rolls both back.
func (h *cashierUI) moveBillBank(ctx context.Context, uid, bankID int64, typ string, amt, svc, total decimal.Decimal, onCredit bool, custID int64, reason, ref string) error {
	biller := "Bill payment"
	if ref != "" {
		biller = "Bill " + ref
	}
	legs := bankBillLegs(typ, onCredit, cashflow.Locker(bankID), cashflow.Till(uid), amt, svc, biller)
	return appdb.WithTx(ctx, h.p.core.DB, func(tx *sqlx.Tx) error {
		for _, l := range legs {
			if _, err := h.p.cashflow.MoveTx(ctx, tx, cashflow.MoveInput{
				From: l.From, To: l.To, Amount: l.Amount, Reason: reason,
				ReceiptKind: typ, Party: l.Party, ActorID: uid,
			}); err != nil {
				return err
			}
		}
		if onCredit {
			return customers.NewRepository(tx).AddBalance(ctx, custID, total)
		}
		return nil
	})
}

// moveBillDevice moves a bill-pay / get-money against a money-usable device float:
// the float goes down (bill paid) or up (customer funded), mirrored by drawer cash
// (or a customer-account charge on credit). A tracked device is overdraw-guarded on
// bill-pay. Sequential like the other device money moves (Tx), not one tx.
func (h *cashierUI) moveBillDevice(ctx context.Context, uid, sessID int64, dev *DeviceBalanceRow, typ string, amt, svc, total decimal.Decimal, onCredit bool, custID int64, reason string) error {
	carrierID, err := h.p.store.CarrierOfDevice(ctx, dev.ID)
	if err != nil {
		return err
	}
	// billpay decreases the float (e-money to the biller); get-money increases it
	// (customer's e-money funds the device). Reuse the existing float tx types.
	floatType := "billpay"
	if typ == "getmoney" {
		floatType = "withdrawal"
	}
	// Overdraw guard on a bill-pay from a tracked float (get-money only adds float).
	if dev.TracksFloat && typ == "billpay" {
		over, err := h.p.store.wouldOverdraw(ctx, sessID, dev.ID, amt)
		if err != nil {
			return err
		}
		if over {
			return apperr.Conflict("not enough float on this device")
		}
	}
	// Float ledger row (untracked = bank card: cash moves, no float delta).
	if _, err := h.p.store.RecordTransaction(ctx, TxInput{
		SessionID: sessID, CarrierID: carrierID, DeviceID: dev.ID, Type: floatType,
		Amount: amt, Note: reason, CreatedBy: uid, Untracked: !dev.TracksFloat,
	}); err != nil {
		return err
	}
	// Customer side: cash across the drawer, or a charge to their account on credit.
	if onCredit {
		return h.p.customers.AddBalance(ctx, custID, total)
	}
	if typ == "billpay" {
		_, err = h.p.core.CashRegister.PayIn(ctx, uid, cashregister.MovementInput{Amount: total.String(), Reason: reason})
		return err
	}
	// get-money cash: hand out the principal, keep the service charge as extra cash.
	if _, err := h.p.core.CashRegister.Withdraw(ctx, uid, cashregister.MovementInput{Amount: amt.String(), Reason: reason}); err != nil {
		return err
	}
	if svc.IsPositive() {
		_, err = h.p.core.CashRegister.PayIn(ctx, uid, cashregister.MovementInput{Amount: svc.String(), Reason: reason + " service charge"})
	}
	return err
}

// moveCashAdvance handles a get-money put on credit with no account chosen: hand
// the customer cash now (drawer-guarded), they owe the total on their account.
func (h *cashierUI) moveCashAdvance(ctx context.Context, uid int64, amt, total decimal.Decimal, custID int64, reason string) error {
	if _, err := h.p.core.CashRegister.Withdraw(ctx, uid, cashregister.MovementInput{Amount: amt.String(), Reason: reason}); err != nil {
		return err
	}
	return h.p.customers.AddBalance(ctx, custID, total)
}

// printPolicy applies the shop's "ask before printing" policy to a recharge slip,
// mirroring the core money flows: ON → fire the shared Print / Skip prompt pointing
// at reprintURL (the client POSTs it to reprint on demand); OFF → print the slip
// now, server-side, best-effort. Either way it re-renders the recon body so live
// balances refresh. The printed artifact is the recharge slip (clean money-receipt
// format, no signature) — never the background CR- receipt.
func (h *cashierUI) printPolicy(c echo.Context, reprintURL string, printNow func(context.Context) error, msg string) error {
	ctx := c.Request().Context()
	cfg, err := h.p.core.Settings.Get(ctx)
	if err == nil && cfg != nil && cfg.AskToPrint {
		return h.reconFragment(c, response.PrintPrompt(msg, reprintURL, false))
	}
	if printNow != nil {
		_ = printNow(ctx) // best-effort: a printer hiccup never fails the transaction
	}
	return h.reconFragment(c, response.Toast(msg, "success"))
}

// TxPrint reprints a deposit / withdrawal slip (the shared Print/Skip prompt or a
// manual reprint from the Recharge receipts tab). Best-effort like BillPrint.
func (h *cashierUI) TxPrint(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	t, err := h.p.store.TxByID(ctx, id)
	if err != nil {
		return err
	}
	if err := h.p.reprintTx(ctx, t); err != nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("Could not reach the printer", "error"))
		return response.NoContent(c)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Slip sent to printer", "success"))
	return response.NoContent(c)
}

// BillPrint reprints a bill-payment / get-money slip (the shared Print/Skip prompt
// or a manual reprint from the Bill receipts tab). Best-effort: a printer problem
// is a warning toast, not a 500.
func (h *cashierUI) BillPrint(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	t, err := h.p.store.BillTxByID(ctx, id)
	if err != nil {
		return err
	}
	if err := h.p.reprintBill(ctx, t); err != nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("Could not reach the printer", "error"))
		return response.NoContent(c)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Slip sent to printer", "success"))
	return response.NoContent(c)
}

// bankLockers returns the active core kind="bank" lockers the owner marked
// cashier-accessible, with live balances, for the cashier bill-pay / get-money
// picker. A "bank" is a plain core locker managed under Money → Cash Lockers; the
// plugin only reads & moves them. Banks with cashier_access off are hidden — the
// cashier literally cannot pick them (and BankTx re-checks on POST).
func (p *Plugin) bankLockers(ctx context.Context) ([]lockers.Locker, error) {
	all, err := p.lockers.List(ctx, true)
	if err != nil {
		return nil, err
	}
	banks := make([]lockers.Locker, 0, len(all))
	for i := range all {
		if bankUsableByCashier(&all[i]) {
			banks = append(banks, all[i])
		}
	}
	return banks, nil
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

// ReverseReloadForm renders the reload-reversal modal: the reload's amount/device
// plus the Failed vs Wrong-number choice.
func (h *cashierUI) ReverseReloadForm(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		return apperr.BadRequest("invalid reload id")
	}
	t, err := h.p.store.TxByID(ctx, id)
	if err != nil {
		return apperr.NotFound("reload")
	}
	if t.Type != "reload" {
		return apperr.BadRequest("only reloads can be reversed")
	}
	if t.Reversed {
		return apperr.Conflict("this reload was already reversed")
	}
	return response.RenderFragment(c, ReverseReloadModal(h.symbol(ctx), t))
}

// ReverseReload refunds the customer and reverses the float. Failed → float
// returns; wrong-number → float stays gone (the loss is already carried by the
// un-recovered refill expense). Requires an open drawer (the refund is
// drawer-guarded). The original sale is never touched — this posts compensating
// moves only.
func (h *cashierUI) ReverseReload(c echo.Context) error {
	ctx := c.Request().Context()
	uid := middleware.CurrentUserID(c)
	if _, err := h.requireSession(c); err != nil {
		return err
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		return apperr.BadRequest("invalid reload id")
	}
	mode := c.FormValue("mode") // "failed" | "wrong_number"
	if mode != "failed" && mode != "wrong_number" {
		return apperr.Validation("choose what happened")
	}
	orig, err := h.p.store.TxByID(ctx, id)
	if err != nil {
		return apperr.NotFound("reload")
	}
	if orig.Type != "reload" {
		return apperr.BadRequest("only reloads can be reversed")
	}
	// Pre-guard BEFORE moving any cash, so a double-submit can't leave an orphan
	// refund withdrawal when the DB reversal would reject it.
	if orig.Reversed {
		return apperr.Conflict("this reload was already reversed")
	}
	reason := orig.Carrier + " reload reversal #" + strconv.FormatInt(id, 10)
	// 1) Refund the customer from the drawer (guards against the drawer balance).
	if _, err := h.p.core.CashRegister.Withdraw(ctx, uid, cashregister.MovementInput{
		Amount: orig.Amount.String(), Reason: reason,
	}); err != nil {
		return err
	}
	// 2) Reverse the float in one DB tx.
	if err := appdb.WithTx(ctx, h.p.core.DB, func(tx *sqlx.Tx) error {
		_, rerr := h.p.store.reverseReloadTx(ctx, tx, id, mode, uid)
		return rerr
	}); err != nil {
		return err
	}
	h.p.core.Audit.Record(ctx, uid, audit.ActionUpdate, "recharge_reload",
		strconv.FormatInt(id, 10), "reversed ("+mode+")")

	// Refresh the Reload receipts panel so the row shows "reversed".
	f, preset, fromStr, toStr, err := receiptsRange(c)
	if err != nil {
		return err
	}
	rows, err := h.p.store.Ledger(ctx, f)
	if err != nil {
		return err
	}
	vm := ReceiptsTabVM{
		Symbol: h.symbol(ctx), Preset: preset, From: fromStr, To: toStr,
		Action:      "/cashier/recharge/receipts/recharge",
		ReprintBase: "/cashier/recharge/tx/", ViewBase: "/cashier/recharge/tx/",
	}
	return response.RenderFragment(c, FloatReceiptsTab(vm, rows))
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

// --- Cashier menu-node protocol (plugin.CashierMenuRoot "Reload & Bills") ---
//
// The root card drills into 3 branches: Reload (carrier → device → amount),
// Bills and Float transactions (both plain HTML detail fragments hosting the
// existing recon forms). See internal/plugin/hooks.go for the node JSON shape
// and static/js/app.js's fetchNodes/openNode/confirmAmount/openDetail for how
// the client walks it.

// MenuRoot returns the three recharge branches for the cashier menu.
// MenuRoot is the cashier-menu entry for reloads: it lists carriers directly, so
// tapping the menu card drills straight into carrier → device → amount. Bill
// payments and float open/close/transactions deliberately stay on the dedicated
// "Reload & Bills" page (recharge nav tab), where their forms are server-rendered
// and HTMX-processed on page load — no fragile fragment injection in the menu.
// MenuRoot is the top of the "Reload & Bills" cashier-menu card: three leaves —
// Reload (drills carrier → device → amount), Bill payment & cash, and Float
// transactions (both inline detail fragments). Replaces the old dedicated
// Reload & Bills top-nav tab; every flow it hosted is reachable from here.
func (h *cashierUI) MenuRoot(c echo.Context) error {
	nodes := []menuNode{
		{Kind: "folder", Name: "Reload", Emoji: "📶", ChildrenURL: "/cashier/recharge/menu/reload/carriers"},
		{Kind: "leaf", Action: "detail", Name: "Bill payment & cash", Emoji: "🧾", DetailURL: "/cashier/recharge/menu/bill"},
		{Kind: "leaf", Action: "detail", Name: "Float transactions", Emoji: "💱", DetailURL: "/cashier/recharge/menu/float"},
	}
	return c.JSON(http.StatusOK, map[string]any{"nodes": nodes})
}

// MenuReloadCarriers lists every carrier as a folder into its devices.
func (h *cashierUI) MenuReloadCarriers(c echo.Context) error {
	cs, err := h.p.store.Carriers(c.Request().Context())
	if err != nil {
		return err
	}
	nodes := make([]menuNode, 0, len(cs))
	for _, cr := range cs {
		nodes = append(nodes, menuNode{
			Kind: "folder", Name: cr.Name, Emoji: "📶",
			ChildrenURL: "/cashier/recharge/menu/reload/devices?carrier=" + strconv.FormatInt(cr.ID, 10),
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"nodes": nodes})
}

// MenuReloadDevices lists a carrier's reload-purpose devices with their live
// balance as amount leaves. Balances are session-scoped: with no open drawer
// there is nothing meaningful to show, so it returns an empty list rather
// than erroring (mirrors the existing Devices handler).
func (h *cashierUI) MenuReloadDevices(c echo.Context) error {
	ctx := c.Request().Context()
	carrierID, _ := strconv.ParseInt(c.QueryParam("carrier"), 10, 64)
	sess, err := h.p.core.CashRegister.Current(ctx, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	if sess == nil {
		return c.JSON(http.StatusOK, map[string]any{"nodes": []menuNode{}})
	}
	rows, err := h.p.store.DevicesWithBalance(ctx, sess.ID, carrierID, "recharge")
	if err != nil {
		return err
	}
	sym := h.symbol(ctx)
	nodes := make([]menuNode, 0, len(rows))
	for _, r := range rows {
		nodes = append(nodes, reloadDeviceNode(carrierID, r, sym))
	}
	return c.JSON(http.StatusOK, map[string]any{"nodes": nodes})
}

// MenuReloadAdd validates the amount against the device's live reload balance
// (the server-side overdraw guard, replacing the old client-side check in the
// removed ReloadPanel) and returns the cart line for the carrier's hidden
// service product. It does NOT record any float transaction — that still
// happens at checkout: the returned line carries deviceId, addServiceLine
// (app.js) stores it as recharge_device_id on the cart line, and the sale's
// completion posts /cashier/recharge/reload once the sale commits.
func (h *cashierUI) MenuReloadAdd(c echo.Context) error {
	ctx := c.Request().Context()
	var in struct {
		Amount string `json:"amount"`
		Meta   struct {
			CarrierID int64 `json:"carrier_id"`
			DeviceID  int64 `json:"device_id"`
		} `json:"meta"`
	}
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request")
	}
	amt, err := parseAmount(in.Amount)
	if err != nil {
		return err
	}
	sess, err := h.requireSession(c)
	if err != nil {
		return err
	}
	rows, err := h.p.store.DevicesWithBalance(ctx, sess.ID, in.Meta.CarrierID, "recharge")
	if err != nil {
		return err
	}
	var row *DeviceBalanceRow
	for i := range rows {
		if rows[i].ID == in.Meta.DeviceID {
			row = &rows[i]
			break
		}
	}
	if row == nil {
		return apperr.Validation("unknown device")
	}
	if amt.GreaterThan(row.Balance) {
		return apperr.Validation("amount exceeds this device's reload balance")
	}
	cs, err := h.p.store.Carriers(ctx)
	if err != nil {
		return err
	}
	var carrier *Carrier
	for i := range cs {
		if cs[i].ID == in.Meta.CarrierID {
			carrier = &cs[i]
			break
		}
	}
	if carrier == nil {
		return apperr.Validation("unknown carrier")
	}
	return c.JSON(http.StatusOK, map[string]any{"line": map[string]any{
		"id":       carrier.ProductID,
		"name":     carrier.Name + " Recharge",
		"price":    amt,
		"deviceId": in.Meta.DeviceID,
	}})
}

// MenuBill renders the existing bill-payment / get-money form (bankTxForm) as
// an inline cashier-menu detail fragment. It is a money move that posts
// directly to /cashier/recharge/bank-tx and toasts — not a cart line.
func (h *cashierUI) MenuBill(c echo.Context) error {
	d, err := h.reconData(c)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, BillMenuForm(d))
}

// MenuFloat renders the existing deposit/withdrawal/top-up form (txForm) as an
// inline cashier-menu detail fragment. Like MenuBill, it posts directly to
// /cashier/recharge/tx and toasts — not a cart line.
func (h *cashierUI) MenuFloat(c echo.Context) error {
	d, err := h.reconData(c)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, FloatMenuForm(d))
}

// DrawerOpenFields renders the opening-float inputs for the core till Open dialog
// (registered as a DrawerSection.OpenFormURL). No session exists yet, so it lists
// devices with blank opening overrides (the server carries the last close forward).
func (h *cashierUI) DrawerOpenFields(c echo.Context) error {
	d, err := h.reconData(c)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, openFloatFields(d))
}

// DrawerCloseFields renders the closing-float count inputs for the core till Close
// dialog (registered as a DrawerSection.CloseFormURL), showing each open device's
// expected balance. Requires the still-open till session.
func (h *cashierUI) DrawerCloseFields(c echo.Context) error {
	d, err := h.reconData(c)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, closeFloatFields(d))
}
