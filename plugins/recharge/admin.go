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
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"
	"karots-pos/templates/layouts"

	"github.com/labstack/echo/v4"
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
	return response.RenderFragment(c, CarrierRows(cs), response.Toast(name+" added", "success"))
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
	return response.RenderPage(c, ReportPage(middleware.CurrentUserName(c), a.symbol(ctx), balances, blocks))
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
	return response.RenderPage(c, TxSlipPage(middleware.CurrentUserName(c), a.symbol(ctx), t))
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
	// A bank card is a money source with no float to track: it is always
	// money-only (never airtime) and skips reconciliation/refill.
	bankCard := c.FormValue("bank_card") != ""
	forRecharge := c.FormValue("for_recharge") != "" && !bankCard
	forMoney := c.FormValue("for_money") != "" || bankCard
	if !forRecharge && !forMoney {
		return apperr.Validation("choose at least one use (recharge and/or money transfer)")
	}
	if _, err := a.p.store.CreateDevice(ctx, carrierID, label, strings.TrimSpace(c.FormValue("number")), forRecharge, forMoney, !bankCard); err != nil {
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
	return response.RenderFragment(c, CarrierRows(cs))
}
