package recharge

import (
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/products"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"

	"github.com/labstack/echo/v4"
)

type adminUI struct{ p *Plugin }

// Carriers renders the carrier management page.
func (a *adminUI) Carriers(c echo.Context) error {
	cs, err := a.p.store.Carriers(c.Request().Context())
	if err != nil {
		return err
	}
	return response.RenderPage(c, CarriersPage(middleware.CurrentUserName(c), cs))
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
