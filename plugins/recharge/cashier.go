package recharge

import (
	"net/http"

	"github.com/labstack/echo/v4"
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
