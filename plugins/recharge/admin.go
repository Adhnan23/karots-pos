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
	"karots-pos/internal/features/cashregister"
	"karots-pos/internal/features/expenses"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/reports"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"
	"karots-pos/templates/layouts"
	"karots-pos/templates/shared"

	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

type adminUI struct{ p *Plugin }

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
	cards, err := a.p.store.ListBankCards(ctx, false)
	if err != nil {
		return err
	}
	return response.RenderPage(c, CarriersPage(middleware.CurrentUserName(c), cs, ds, cards))
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
	cards, err := a.p.store.ListBankCards(ctx, false)
	if err != nil {
		return err
	}
	cardLed, err := a.p.store.CardLedger(ctx, 50)
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
		Cards:         cards,
		CardLedger:    cardLed,
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
	return response.RenderPage(c, TxSlipPage(middleware.CurrentUserName(c), a.symbol(ctx), a.shopInfo(ctx), t))
}

// shopInfo resolves the shop header (name/address/phone) for the HTML slip view,
// matching the printed receipt. Empty on error.
func (a *adminUI) shopInfo(ctx context.Context) ShopInfo {
	cfg, err := a.p.core.Settings.Get(ctx)
	if err != nil || cfg == nil {
		return ShopInfo{}
	}
	s := ShopInfo{Name: cfg.ShopName}
	if cfg.Address != nil {
		s.Address = *cfg.Address
	}
	if cfg.Phone != nil {
		s.Phone = *cfg.Phone
	}
	return s
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
	return response.RenderPage(c, RefillsPage(middleware.CurrentUserName(c), a.symbol(ctx), balances, rows))
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

// Refill records an admin supplier float top-up: it increases a device's float
// and books a shop expense, WITHOUT touching any cash drawer (the admin pays the
// supplier directly). It attributes to the current open cash session if there is
// one (so it shows live and in that shift's reconciliation); otherwise it is
// recorded session-less and carried into the next session's opening.
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
	carrierID, err := a.p.store.CarrierOfDevice(ctx, deviceID)
	if err != nil {
		return err
	}
	if carrierID == 0 {
		return apperr.Validation("unknown device")
	}
	ref := strings.TrimSpace(c.FormValue("reference"))
	note := strings.TrimSpace(c.FormValue("note"))

	// Book the supplier purchase as a shop expense (no drawer movement).
	desc := a.p.store.CarrierName(ctx, carrierID) + " supplier float refill"
	exp, err := a.p.core.Expenses.Create(ctx, expenses.CreateInput{
		Category: "Float top-up", Amount: amt.String(), Description: &desc,
	}, uid)
	if err != nil {
		return err
	}

	// Attribute to the current open cash session if one exists; else session-less
	// (sessionID 0), which the opening-carry picks up for the next shift.
	var sessionID int64
	if sess, err := a.p.core.CashRegister.Current(ctx, uid); err == nil && sess != nil {
		sessionID = sess.ID
	}
	expID := exp.ID
	if _, err := a.p.store.RecordTransaction(ctx, TxInput{
		SessionID: sessionID, CarrierID: carrierID, DeviceID: deviceID, Type: "refill",
		Amount: amt, ExpenseID: &expID, Reference: ref, Note: note, CreatedBy: uid,
	}); err != nil {
		return err
	}

	balances, err := a.p.store.DeviceBalances(ctx)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, BalancePanel(a.symbol(ctx), balances), response.Toast("Float refilled", "success"))
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

// cardRowsFragment re-renders the bank-card table (the HTMX swap target after a
// card create/delete/adjust).
func (a *adminUI) cardRowsFragment(c echo.Context, triggers ...string) error {
	cards, err := a.p.store.ListBankCards(c.Request().Context(), false)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, CardRows(a.symbol(c.Request().Context()), cards), triggers...)
}

// CardCreate adds a bank card (a carrier-independent money source with a tracked
// balance, used for bill-pay / get-money at the till).
func (a *adminUI) CardCreate(c echo.Context) error {
	ctx := c.Request().Context()
	name := strings.TrimSpace(c.FormValue("name"))
	if name == "" {
		return apperr.Validation("bank card name is required")
	}
	exists, err := a.p.store.BankCardExists(ctx, name)
	if err != nil {
		return err
	}
	if exists {
		return apperr.Conflict("a bank card with that name already exists")
	}
	if _, err := a.p.store.CreateBankCard(ctx, name); err != nil {
		return err
	}
	return a.cardRowsFragment(c, response.Toast(name+" added", "success"))
}

// CardDelete retires a bank card (its history is preserved).
func (a *adminUI) CardDelete(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.p.store.DeactivateBankCard(ctx, id); err != nil {
		return err
	}
	return a.cardRowsFragment(c)
}

// CardAdjust is the admin's balance-only deposit/withdrawal on a bank card: it
// changes the tracked balance WITHOUT touching the cash drawer or booking an
// expense. (The admin manages the real bank balance; if they need the cash they
// take it from the cashier as a normal till withdrawal.)
func (a *adminUI) CardAdjust(c echo.Context) error {
	ctx := c.Request().Context()
	uid := middleware.CurrentUserID(c)
	cardID, err := strconv.ParseInt(c.FormValue("card_id"), 10, 64)
	if err != nil || cardID == 0 {
		return apperr.Validation("choose a bank card")
	}
	if a.p.store.BankCardName(ctx, cardID) == "" {
		return apperr.Validation("unknown bank card")
	}
	typ := c.FormValue("type")
	if typ != "deposit" && typ != "withdrawal" {
		return apperr.BadRequest("invalid adjustment type")
	}
	amt, err := money.Parse(c.FormValue("amount"))
	if err != nil || !amt.IsPositive() {
		return apperr.Validation("enter a valid amount")
	}
	// deposit: balance +.   withdrawal: balance − (guard against overdraw).
	balanceDelta := amt
	if typ == "withdrawal" {
		over, err := a.p.store.cardWouldOverdraw(ctx, cardID, amt)
		if err != nil {
			return err
		}
		if over {
			return apperr.Conflict("not enough balance on this card")
		}
		balanceDelta = amt.Neg()
	}
	if _, err := a.p.store.RecordCardTx(ctx, CardTxInput{
		CardID: cardID, Type: typ, Amount: amt, BalanceDelta: balanceDelta,
		Note: strings.TrimSpace(c.FormValue("note")), CreatedBy: uid,
	}); err != nil {
		return err
	}
	return a.cardRowsFragment(c, response.Toast("Balance updated", "success"))
}
