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
	"karots-pos/internal/features/products"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"

	"github.com/labstack/echo/v4"
)

type adminUI struct{ p *Plugin }

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
	_ = w.Write([]string{"When", "Carrier", "Device", "Type", "Amount", "Cash", "Float", "Reference"})
	for _, t := range rows {
		_ = w.Write([]string{
			t.CreatedAt.Format("2006-01-02 15:04"),
			t.Carrier, t.Device, t.Type,
			t.Amount.StringFixed(2), t.CashDelta.StringFixed(2), t.FloatDelta.StringFixed(2),
			refText(t.Reference),
		})
	}
	w.Flush()
	return w.Error()
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
	if _, err := a.p.store.CreateDevice(ctx, carrierID, label, strings.TrimSpace(c.FormValue("number"))); err != nil {
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
