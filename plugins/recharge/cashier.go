package recharge

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/auth"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/cashregister"
	"karots-pos/internal/features/expenses"
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
	// Float devices handle deposit / withdrawal / topup only. wallet_in & reload flow
	// through the sale path; billpay is now a bank-card operation (see CardTx).
	if !ok || typ == "wallet_in" || typ == "reload" || typ == "billpay" {
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
	txID, err := h.p.store.RecordTransaction(ctx, TxInput{
		SessionID: sess.ID, CarrierID: carrierID, DeviceID: deviceID, Type: typ,
		Amount: amt, ExpenseID: expenseID, Reference: ref, Note: note, CreatedBy: uid,
		Untracked: !tracksFloat, ServiceCharge: svc,
	})
	if err != nil {
		return err
	}

	msg := carrier + " " + txLabel(typ) + " recorded"
	// 4) Cash-handling types (deposit / withdrawal) print a slip under the print
	// policy; a top-up just books the expense with no customer slip.
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
	bankID, err := strconv.ParseInt(c.FormValue("bank_locker_id"), 10, 64)
	if err != nil || bankID == 0 {
		return apperr.Validation("choose a bank")
	}
	bank, err := h.p.lockers.Get(ctx, bankID)
	if err != nil || bank == nil || !bank.IsActive || bank.Kind != lockers.KindBank {
		return apperr.Validation("choose a valid bank")
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

	reason := bank.Name + " " + txLabel(typ)
	if ref != "" {
		reason += " #" + ref
	}
	if note != "" {
		reason += " - " + note
	}

	// The External counterparty is labelled per leg on the (background) CR- receipts:
	// the bank↔biller leg names the biller, the till↔customer legs name the customer.
	biller := "Bill payment"
	if ref != "" {
		biller = "Bill " + ref
	}
	till := cashflow.Till(uid)
	bankLoc := cashflow.Locker(bankID)
	ext := cashflow.External()
	type leg struct {
		from, to cashflow.Location
		amount   decimal.Decimal
		party    string
	}
	var legs []leg
	switch typ {
	case "billpay":
		// bank down (guarded) first, then cash in (bill + service charge).
		legs = append(legs, leg{bankLoc, ext, amt, biller})
		legs = append(legs, leg{ext, till, amt.Add(svc), "Customer"})
	case "getmoney":
		// cash out (guarded) first, then bank up, then the service-charge cash-in.
		legs = append(legs, leg{till, ext, amt, "Customer"})
		legs = append(legs, leg{ext, bankLoc, amt, "Customer"})
		if svc.IsPositive() {
			legs = append(legs, leg{ext, till, svc, "Customer"})
		}
	}

	// All legs in ONE transaction so a drawer/bank overdraw rolls everything back.
	if err := appdb.WithTx(ctx, h.p.core.DB, func(tx *sqlx.Tx) error {
		for _, l := range legs {
			if _, err := h.p.cashflow.MoveTx(ctx, tx, cashflow.MoveInput{
				From: l.from, To: l.to, Amount: l.amount, Reason: reason,
				ReceiptKind: typ, Party: l.party, ActorID: uid,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}

	// Log the customer-facing detail (balance-free) so the slip can be reprinted and
	// the movement lists in the "Bill" receipts tab. The money/receipts live in core.
	billID, err := h.p.store.RecordBillTx(ctx, BillTxInput{
		SessionID: &sess.ID, BankLockerID: bankID, BankName: bank.Name, Type: typ,
		Amount: amt, ServiceCharge: svc, Reference: ref, Note: note, CreatedBy: uid,
	})
	if err != nil {
		return err
	}

	msg := bank.Name + " " + txLabel(typ) + " recorded"
	return h.printPolicy(c, "/cashier/recharge/bill/"+strconv.FormatInt(billID, 10)+"/print",
		func(ctx context.Context) error {
			t, err := h.p.store.BillTxByID(ctx, billID)
			if err != nil {
				return err
			}
			return h.p.reprintBill(ctx, t)
		}, msg)
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

// bankLockers returns the active core kind="bank" lockers (the shop's bank
// accounts) with live balances, for the cashier bill-pay / get-money picker.
func (p *Plugin) bankLockers(ctx context.Context) ([]lockers.Locker, error) {
	all, err := p.lockers.List(ctx, true)
	if err != nil {
		return nil, err
	}
	banks := make([]lockers.Locker, 0, len(all))
	for _, l := range all {
		if l.Kind == lockers.KindBank {
			banks = append(banks, l)
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
