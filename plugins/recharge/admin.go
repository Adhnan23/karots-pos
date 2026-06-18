package recharge

import (
	"strconv"
	"strings"

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

// Report renders the recharge admin report: recent ledger entries plus a
// per-session reconciliation for the last few cash-drawer sessions.
func (a *adminUI) Report(c echo.Context) error {
	ctx := c.Request().Context()
	txs, err := a.p.store.RecentTransactions(ctx, 100)
	if err != nil {
		return err
	}
	sessions, err := a.p.core.CashRegister.RecentSessions(ctx, 8)
	if err != nil {
		return err
	}
	blocks := make([]SessionRecon, 0, len(sessions))
	for _, s := range sessions {
		rows, err := a.p.store.Reconciliation(ctx, s.ID, s.UserID, s.OpenedAt)
		if err != nil {
			return err
		}
		blocks = append(blocks, SessionRecon{Session: s, Rows: rows})
	}
	symbol := ""
	if cfg, err := a.p.core.Settings.Get(ctx); err == nil {
		symbol = cfg.CurrencySymbol
	}
	return response.RenderPage(c, ReportPage(middleware.CurrentUserName(c), symbol, txs, blocks))
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
