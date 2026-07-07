package recharge

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/cashregister"
	"karots-pos/internal/features/expenses"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/reports"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"
	adminfragments "karots-pos/templates/fragments/admin"
	"karots-pos/templates/layouts"
	"karots-pos/templates/shared"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

type adminUI struct{ p *Plugin }

// receiptsRange reads the shared date-range query params for a receipts tab. With
// no preset/dates it returns an all-time filter (empty preset), matching the core
// receipts tabs; otherwise it resolves the preset to a [from, to) window.
func receiptsRange(c echo.Context) (LedgerFilter, string, string, string, error) {
	f := LedgerFilter{Limit: 500}
	preset := c.QueryParam("preset")
	from := c.QueryParam("from")
	to := c.QueryParam("to")
	if preset == "" && from == "" && to == "" {
		return f, "", "", "", nil
	}
	fr, t, fromStr, toStr, err := reports.ResolveRange(preset, from, to)
	if err != nil {
		return f, "", "", "", err
	}
	f.From, f.To = &fr, &t
	return f, preset, fromStr, toStr, nil
}

// Hub is the Reload & Bills landing page: it lists the section's sub-pages as
// cards (carriers/devices, report, ledger, float refills), mirroring the core
// admin section hubs so the sidebar link opens a navigable hub.
func (a *adminUI) Hub(c echo.Context) error {
	sec, _ := layouts.SectionByKey("Reload & Bills")
	return response.RenderPage(c, HubPage(middleware.CurrentUserName(c), sec))
}

// Carriers renders the carrier & device management page.
func (a *adminUI) Carriers(c echo.Context) error {
	ctx := c.Request().Context()
	cs, err := a.p.store.Carriers(ctx)
	if err != nil {
		return err
	}
	ds, err := a.p.store.Devices(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, CarriersPage(middleware.CurrentUserName(c), cs, ds))
}

// CarriersTable is the HTMX row fragment.
func (a *adminUI) CarriersTable(c echo.Context) error {
	cs, err := a.p.store.Carriers(c.Request().Context())
	if err != nil {
		return err
	}
	return response.RenderFragment(c, CarrierRows(cs))
}

// CarrierCreate adds a carrier and its hidden is_service product (the line that
// carries this carrier's recharge sales through the core sale path).
func (a *adminUI) CarrierCreate(c echo.Context) error {
	ctx := c.Request().Context()
	name := strings.TrimSpace(c.FormValue("name"))
	if name == "" {
		return apperr.Validation("carrier name is required")
	}
	exists, err := a.p.store.CarrierExists(ctx, name)
	if err != nil {
		return err
	}
	if exists {
		return apperr.Conflict("a carrier with that name already exists")
	}

	catID, unitID, err := a.p.store.serviceDefaults(ctx)
	if err != nil {
		return apperr.Internal("failed to resolve product defaults", err)
	}
	prod, err := a.p.core.Products.Create(ctx, products.CreateInput{
		Name:           name + " Recharge",
		CategoryID:     catID,
		UnitID:         unitID,
		CostPrice:      "0",
		SellingPrice:   "0",
		WholesalePrice: "0",
		TaxRate:        "0",
		IsService:      true,
	})
	if err != nil {
		return err
	}
	if _, err := a.p.store.CreateCarrier(ctx, name, prod.ID); err != nil {
		return err
	}

	cs, err := a.p.store.Carriers(ctx)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, CarrierRows(cs), response.ToastAnd(name+" added", "success", "carriers-changed"))
}

// DeviceForm re-renders the carrier-dependent "Add a device" form, so it refreshes
// (carrier dropdown / "add a carrier first" state) when carriers change without a
// full page reload.
func (a *adminUI) DeviceForm(c echo.Context) error {
	cs, err := a.p.store.Carriers(c.Request().Context())
	if err != nil {
		return err
	}
	return response.RenderFragment(c, DeviceAddForm(cs))
}

// SessionRecon pairs a cash session with its per-carrier recharge reconciliation.
type SessionRecon struct {
	Session cashregister.SessionRow
	Rows    []CarrierRecon
}

// LedgerForm holds the current ledger filter selections (for re-rendering the
// filter controls with the applied values).
type LedgerForm struct {
	From      string
	To        string
	CarrierID int64
	DeviceID  int64
	Type      string
}

// ledgerQuery encodes a LedgerForm back into a URL query string (for the CSV
// download link, so it carries the same filter the user is viewing).
func ledgerQuery(f LedgerForm) string {
	v := url.Values{}
	if f.From != "" {
		v.Set("from", f.From)
	}
	if f.To != "" {
		v.Set("to", f.To)
	}
	if f.CarrierID != 0 {
		v.Set("carrier_id", strconv.FormatInt(f.CarrierID, 10))
	}
	if f.DeviceID != 0 {
		v.Set("device_id", strconv.FormatInt(f.DeviceID, 10))
	}
	if f.Type != "" {
		v.Set("type", f.Type)
	}
	return v.Encode()
}

// Report renders the recharge admin report: live per-device float balances plus
// a per-session reconciliation for the last few cash-drawer sessions.
func (a *adminUI) Report(c echo.Context) error {
	ctx := c.Request().Context()
	balances, err := a.p.store.DeviceBalances(ctx)
	if err != nil {
		return err
	}
	sessions, err := a.p.core.CashRegister.RecentSessions(ctx, 8)
	if err != nil {
		return err
	}
	blocks := make([]SessionRecon, 0, len(sessions))
	for _, s := range sessions {
		rows, err := a.p.store.Reconciliation(ctx, s.ID)
		if err != nil {
			return err
		}
		blocks = append(blocks, SessionRecon{Session: s, Rows: rows})
	}

	// Range-scoped summary: service charge earned + value moved by type.
	preset := c.QueryParam("preset")
	from, to, fromStr, toStr, err := reports.ResolveRange(preset, c.QueryParam("from"), c.QueryParam("to"))
	if err != nil {
		return err
	}
	led, err := a.p.store.Ledger(ctx, LedgerFilter{From: &from, To: &to, Limit: 100000})
	if err != nil {
		return err
	}
	symbol := a.symbol(ctx)
	vm := ReportVM{
		UserName:      middleware.CurrentUserName(c),
		Symbol:        symbol,
		Preset:        preset,
		From:          fromStr,
		To:            toStr,
		Balances:      balances,
		Blocks:        blocks,
		ServiceEarned: sumServiceCharge(led),
		FloatOnHand:   sumBalances(balances),
		TypeBars:      typeValueBars(led, symbol),
	}
	return response.RenderPage(c, ReportPage(vm))
}

// sumBalances totals the live float across all tracked devices.
func sumBalances(bs []DeviceBalanceNow) decimal.Decimal {
	t := decimal.Zero
	for _, b := range bs {
		t = t.Add(b.Balance)
	}
	return t
}

// typeValueBars aggregates ledger value by transaction type into chart bars, in a
// stable, human order.
func typeValueBars(rows []TxRow, symbol string) []shared.ChartBar {
	order := []string{"deposit", "withdrawal", "billpay", "topup", "wallet_in", "reload", "refill"}
	sums := map[string]decimal.Decimal{}
	for _, r := range rows {
		sums[r.Type] = sums[r.Type].Add(r.Amount)
	}
	var bars []shared.ChartBar
	for _, t := range order {
		v, ok := sums[t]
		if !ok || v.IsZero() {
			continue
		}
		bars = append(bars, shared.ChartBar{
			Label: txLabel(t),
			Value: v.InexactFloat64(),
			Text:  money.Format(symbol, v),
		})
	}
	return bars
}

// symbol resolves the configured currency symbol (empty on error).
func (a *adminUI) symbol(ctx context.Context) string {
	if cfg, err := a.p.core.Settings.Get(ctx); err == nil {
		return cfg.CurrencySymbol
	}
	return ""
}

// ledgerFilter builds a LedgerFilter from the request query params.
func (a *adminUI) ledgerFilter(c echo.Context) LedgerFilter {
	f := LedgerFilter{Limit: 500}
	if v := c.QueryParam("from"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			f.From = &t
		}
	}
	if v := c.QueryParam("to"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			end := t.AddDate(0, 0, 1) // inclusive of the "to" day
			f.To = &end
		}
	}
	f.CarrierID, _ = strconv.ParseInt(c.QueryParam("carrier_id"), 10, 64)
	f.DeviceID, _ = strconv.ParseInt(c.QueryParam("device_id"), 10, 64)
	f.Type = c.QueryParam("type")
	return f
}

// Ledger renders the filterable money-movement ledger (date / carrier / device /
// type), with a CSV download of the same filtered set.
func (a *adminUI) Ledger(c echo.Context) error {
	ctx := c.Request().Context()
	f := a.ledgerFilter(c)
	rows, err := a.p.store.Ledger(ctx, f)
	if err != nil {
		return err
	}
	if c.QueryParam("format") == "csv" {
		return a.ledgerCSV(c, rows)
	}
	carriers, err := a.p.store.Carriers(ctx)
	if err != nil {
		return err
	}
	devices, err := a.p.store.Devices(ctx)
	if err != nil {
		return err
	}
	lf := LedgerForm{
		From:      c.QueryParam("from"),
		To:        c.QueryParam("to"),
		CarrierID: f.CarrierID,
		DeviceID:  f.DeviceID,
		Type:      f.Type,
	}
	return response.RenderPage(c, LedgerPage(middleware.CurrentUserName(c), a.symbol(ctx), lf, carriers, devices, rows))
}

// ledgerCSV streams the filtered ledger as a downloadable CSV attachment.
func (a *adminUI) ledgerCSV(c echo.Context, rows []TxRow) error {
	c.Response().Header().Set(echo.HeaderContentType, "text/csv; charset=utf-8")
	c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="recharge-ledger.csv"`)
	c.Response().WriteHeader(http.StatusOK)
	w := csv.NewWriter(c.Response())
	_ = w.Write([]string{"When", "Carrier", "Device", "Type", "Amount", "Service", "Cash", "Float", "Reference"})
	for _, t := range rows {
		_ = w.Write([]string{
			t.CreatedAt.Format("2006-01-02 15:04"),
			t.Carrier, t.Device, t.Type,
			t.Amount.StringFixed(2), t.ServiceCharge.StringFixed(2),
			t.CashDelta.StringFixed(2), t.FloatDelta.StringFixed(2),
			refText(t.Reference),
		})
	}
	w.Flush()
	return w.Error()
}

// TxView renders a single transaction slip as a printable HTML page (parity with
// the core sale receipt view).
func (a *adminUI) TxView(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	t, err := a.p.store.TxByID(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := a.p.core.Settings.Get(ctx)
	if err != nil {
		return err
	}
	base := "/admin/recharge/tx/" + strconv.FormatInt(t.ID, 10)
	thermal := shared.ThermalFrom(cfg.ReceiptWidth, c.QueryParam("size"), "Slip "+floatNo(t.ID), base, base+"/print")
	return response.RenderPage(c, TxSlipPage(*cfg, thermal, t))
}

// BillView renders a single bill-payment / get-money slip as a printable HTML
// page (the View link on the admin "Bills" receipts tab).
func (a *adminUI) BillView(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	t, err := a.p.store.BillTxByID(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := a.p.core.Settings.Get(ctx)
	if err != nil {
		return err
	}
	base := "/admin/recharge/bill/" + strconv.FormatInt(t.ID, 10)
	thermal := shared.ThermalFrom(cfg.ReceiptWidth, c.QueryParam("size"), "Slip "+billNo(t.ID), base, base+"/print")
	return response.RenderPage(c, BillSlipPage(*cfg, thermal, t))
}

// TxPrint reprints a transaction slip to the receipt printer.
func (a *adminUI) TxPrint(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	t, err := a.p.store.TxByID(ctx, id)
	if err != nil {
		return err
	}
	// A reprint is best-effort: an offline/unconfigured printer reports a warning
	// toast rather than a 500 (the ledger row is unaffected).
	if err := a.p.reprintTx(ctx, t); err != nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("Could not reach the printer", "error"))
		return response.NoContent(c)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Slip sent to printer", "success"))
	return response.NoContent(c)
}

// BillPrint reprints a bill-payment / get-money slip from the admin Receipts tab.
// Best-effort like TxPrint.
func (a *adminUI) BillPrint(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	t, err := a.p.store.BillTxByID(ctx, id)
	if err != nil {
		return err
	}
	if err := a.p.reprintBill(ctx, t); err != nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("Could not reach the printer", "error"))
		return response.NoContent(c)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Slip sent to printer", "success"))
	return response.NoContent(c)
}

// ReceiptsBill renders the admin "Bills" receipts tab with admin-scoped reprints.
func (a *adminUI) ReceiptsBill(c echo.Context) error {
	ctx := c.Request().Context()
	f, preset, fromStr, toStr, err := receiptsRange(c)
	if err != nil {
		return err
	}
	rows, err := a.p.store.BillLedger(ctx, f)
	if err != nil {
		return err
	}
	vm := ReceiptsTabVM{
		Symbol: a.symbol(ctx), Preset: preset, From: fromStr, To: toStr,
		Action:      "/admin/recharge/receipts/bill",
		ReprintBase: "/admin/recharge/bill/", ViewBase: "/admin/recharge/bill/",
	}
	return response.RenderFragment(c, BillReceiptsTab(vm, rows))
}

// ReceiptsFloat renders the admin "Reload" receipts tab (float transactions).
func (a *adminUI) ReceiptsFloat(c echo.Context) error {
	ctx := c.Request().Context()
	f, preset, fromStr, toStr, err := receiptsRange(c)
	if err != nil {
		return err
	}
	rows, err := a.p.store.Ledger(ctx, f)
	if err != nil {
		return err
	}
	vm := ReceiptsTabVM{
		Symbol: a.symbol(ctx), Preset: preset, From: fromStr, To: toStr,
		Action:      "/admin/recharge/receipts/recharge",
		ReprintBase: "/admin/recharge/tx/", ViewBase: "/admin/recharge/tx/",
	}
	return response.RenderFragment(c, FloatReceiptsTab(vm, rows))
}

// parseLocation turns a LocationPicker value ("locker:3", "till:5") into a
// cashflow.Location for the refill cash source. Mirrors the core helper (the
// plugin can't import internal/web). External is not a pickable own-pile.
func parseLocation(v string) (cashflow.Location, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return cashflow.Location{}, apperr.Validation("pick where the cash comes from")
	}
	kind, idStr, ok := strings.Cut(v, ":")
	if !ok {
		return cashflow.Location{}, apperr.Validation("invalid cash location")
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return cashflow.Location{}, apperr.Validation("invalid cash location")
	}
	switch kind {
	case "locker":
		return cashflow.Locker(id), nil
	case "till":
		return cashflow.Till(id), nil
	}
	return cashflow.Location{}, apperr.Validation("invalid cash location")
}

// cashLocationChoices lists the pickable cash sources for the refill picker —
// active lockers (with live balance) and currently-open tills. Mirrors the core
// helper so the refill pays the supplier from a real tracked pile.
func (a *adminUI) cashLocationChoices(ctx context.Context) ([]adminfragments.LocationChoice, error) {
	sym := a.symbol(ctx)
	var out []adminfragments.LocationChoice
	lockerRows, err := a.p.lockers.List(ctx, true)
	if err != nil {
		return nil, err
	}
	for _, l := range lockerRows {
		out = append(out, adminfragments.LocationChoice{
			Value: "locker:" + strconv.FormatInt(l.ID, 10),
			Label: l.Name + " (" + money.Format(sym, l.Balance) + ")",
			Group: "Lockers",
		})
	}
	tills, err := a.p.core.CashRegister.OpenSessions(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range tills {
		out = append(out, adminfragments.LocationChoice{
			Value: "till:" + strconv.FormatInt(t.UserID, 10),
			Label: "Till — " + t.UserName,
			Group: "Tills",
		})
	}
	return out, nil
}

// Refills renders the dedicated supplier float-refill page: the refill form, the
// live device balances, and the history of past refills.
func (a *adminUI) Refills(c echo.Context) error {
	ctx := c.Request().Context()
	balances, err := a.p.store.DeviceBalances(ctx)
	if err != nil {
		return err
	}
	rows, err := a.p.store.Ledger(ctx, LedgerFilter{Type: "refill", Limit: 200})
	if err != nil {
		return err
	}
	choices, err := a.cashLocationChoices(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, RefillsPage(middleware.CurrentUserName(c), a.symbol(ctx), balances, rows, choices))
}

// Devices returns active devices with their current (session-independent) float
// balance for the admin refill picker, optionally narrowed to one carrier.
func (a *adminUI) Devices(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.p.store.DeviceBalances(ctx)
	if err != nil {
		return err
	}
	if carrierID, _ := strconv.ParseInt(c.QueryParam("carrier_id"), 10, 64); carrierID != 0 {
		out := make([]DeviceBalanceNow, 0, len(rows))
		for _, r := range rows {
			if r.CarrierID == carrierID {
				out = append(out, r)
			}
		}
		rows = out
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

// Refill records an admin supplier float top-up: it increases a device's float,
// books a shop expense, and pays the supplier from a picked cash source (a locker
// or an open till) via cashflow.Move — so the cash actually leaves a tracked pile
// (each leg gets a CR- receipt and shows in core Cash Flow). All three commit in
// ONE transaction, so an overdrawn source rolls the whole thing back.
//
// The device-float side attributes to the till that actually has this device's
// float open (so the working cashier sees the refill live, overdraw guard
// included); if no till has it open it is recorded session-less (0) and the
// opening-carry picks it up at the device's next opening (QA-013).
func (a *adminUI) Refill(c echo.Context) error {
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
	src, err := parseLocation(c.FormValue("source"))
	if err != nil {
		return err
	}
	carrierID, err := a.p.store.CarrierOfDevice(ctx, deviceID)
	if err != nil {
		return err
	}
	if carrierID == 0 {
		return apperr.Validation("unknown device")
	}
	ref := strings.TrimSpace(c.FormValue("reference"))
	note := strings.TrimSpace(c.FormValue("note"))

	supplier := a.p.store.CarrierName(ctx, carrierID)
	desc := supplier + " supplier float refill"
	reason := desc
	if ref != "" {
		reason += " #" + ref
	}
	sessionID, err := a.p.store.OpenDeviceSession(ctx, deviceID)
	if err != nil {
		return err
	}

	var rec *cashflow.Receipt
	var txID int64
	err = appdb.WithTx(ctx, a.p.core.DB, func(tx *sqlx.Tx) error {
		exp, err := a.p.core.Expenses.CreateInTx(ctx, tx, expenses.CreateInput{
			Category: "Float top-up", Amount: amt.String(), Description: &desc,
		}, uid)
		if err != nil {
			return err
		}
		// Pay the supplier from the picked source (overdraw-guarded).
		r, err := a.p.cashflow.MoveTx(ctx, tx, cashflow.MoveInput{
			From: src, To: cashflow.External(),
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
		// Increase the device float, linked to the expense.
		expID := exp.ID
		txID, err = a.p.store.RecordTransactionTx(ctx, tx, TxInput{
			SessionID: sessionID, CarrierID: carrierID, DeviceID: deviceID, Type: "refill",
			Amount: amt, ExpenseID: &expID, Reference: ref, Note: note, CreatedBy: uid,
		})
		return err
	})
	if err != nil {
		return err
	}

	balances, err := a.p.store.DeviceBalances(ctx)
	if err != nil {
		return err
	}

	// Follow the shop's print policy for the RL- refill slip, like every other money
	// move: AskToPrint on → the shared Print / Skip prompt (pointing at the slip's
	// reprint URL); off → best-effort print now. Either way the balance panel swaps
	// in with the new float.
	msg := "Float refilled — paid from " + rec.FromLabel
	reprintURL := "/admin/recharge/tx/" + strconv.FormatInt(txID, 10) + "/print"
	panel := BalancePanel(a.symbol(ctx), balances)
	if cfg, cerr := a.p.core.Settings.Get(ctx); cerr == nil && cfg != nil && cfg.AskToPrint {
		return response.RenderFragment(c, panel, response.PrintPrompt(msg, reprintURL, false))
	}
	if t, terr := a.p.store.TxByID(ctx, txID); terr == nil {
		_ = a.p.reprintTx(ctx, t) // best-effort: a printer hiccup never fails the refill
	}
	return response.RenderFragment(c, panel, response.Toast(msg, "success"))
}

// DeviceCreate adds a device under a carrier.
func (a *adminUI) DeviceCreate(c echo.Context) error {
	ctx := c.Request().Context()
	carrierID, err := strconv.ParseInt(c.FormValue("carrier_id"), 10, 64)
	if err != nil {
		return apperr.Validation("choose a carrier")
	}
	label := strings.TrimSpace(c.FormValue("label"))
	if label == "" {
		return apperr.Validation("device label is required")
	}
	// Devices always hold a tracked reload balance. (Bank cards are a separate
	// entity now — see CardCreate.)
	forRecharge := c.FormValue("for_recharge") != ""
	forMoney := c.FormValue("for_money") != ""
	if !forRecharge && !forMoney {
		return apperr.Validation("choose at least one use (recharge and/or money transfer)")
	}
	if _, err := a.p.store.CreateDevice(ctx, carrierID, label, strings.TrimSpace(c.FormValue("number")), forRecharge, forMoney, true); err != nil {
		return err
	}
	ds, err := a.p.store.Devices(ctx)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, DeviceRows(ds), response.Toast(label+" added", "success"))
}

// DeviceDelete retires a device (its history is preserved).
func (a *adminUI) DeviceDelete(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.p.store.DeactivateDevice(ctx, id); err != nil {
		return err
	}
	ds, err := a.p.store.Devices(ctx)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, DeviceRows(ds))
}

// CarrierDelete soft-deletes a carrier (sales history is preserved).
func (a *adminUI) CarrierDelete(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.p.store.DeactivateCarrier(ctx, id); err != nil {
		return err
	}
	cs, err := a.p.store.Carriers(ctx)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, CarrierRows(cs), response.Trigger(map[string]any{"carriers-changed": true}))
}
