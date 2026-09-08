package repairs

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// cashierUI holds the cashier-menu handlers. Bodies are filled in Tasks 8–10;
// the stubs keep the routes registered and the build green.
type cashierUI struct{ p *Plugin }

func (h *cashierUI) MenuRoot(c echo.Context) error      { return c.NoContent(http.StatusOK) }
func (h *cashierUI) ApplyForm(c echo.Context) error     { return c.NoContent(http.StatusOK) }
func (h *cashierUI) Create(c echo.Context) error        { return c.NoContent(http.StatusOK) }
func (h *cashierUI) Detail(c echo.Context) error        { return c.NoContent(http.StatusOK) }
func (h *cashierUI) AddPart(c echo.Context) error       { return c.NoContent(http.StatusOK) }
func (h *cashierUI) AddCharge(c echo.Context) error     { return c.NoContent(http.StatusOK) }
func (h *cashierUI) TakeDeposit(c echo.Context) error   { return c.NoContent(http.StatusOK) }
func (h *cashierUI) Record(c echo.Context) error        { return c.NoContent(http.StatusOK) }
func (h *cashierUI) Receipts(c echo.Context) error      { return c.NoContent(http.StatusOK) }
func (h *cashierUI) RepairReceipt(c echo.Context) error { return c.NoContent(http.StatusOK) }
