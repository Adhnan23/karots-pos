package repairs

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// adminUI holds the admin-side handlers. Bodies are filled in Tasks 5–9; the
// stubs keep the routes registered and the build green.
type adminUI struct{ p *Plugin }

func (a *adminUI) List(c echo.Context) error          { return c.NoContent(http.StatusOK) }
func (a *adminUI) NewForm(c echo.Context) error       { return c.NoContent(http.StatusOK) }
func (a *adminUI) Create(c echo.Context) error        { return c.NoContent(http.StatusOK) }
func (a *adminUI) Detail(c echo.Context) error        { return c.NoContent(http.StatusOK) }
func (a *adminUI) Update(c echo.Context) error        { return c.NoContent(http.StatusOK) }
func (a *adminUI) AddPart(c echo.Context) error       { return c.NoContent(http.StatusOK) }
func (a *adminUI) RemovePart(c echo.Context) error    { return c.NoContent(http.StatusOK) }
func (a *adminUI) AddCharge(c echo.Context) error     { return c.NoContent(http.StatusOK) }
func (a *adminUI) RemoveCharge(c echo.Context) error  { return c.NoContent(http.StatusOK) }
func (a *adminUI) TakeDeposit(c echo.Context) error   { return c.NoContent(http.StatusOK) }
func (a *adminUI) SetStatus(c echo.Context) error     { return c.NoContent(http.StatusOK) }
func (a *adminUI) Collect(c echo.Context) error       { return c.NoContent(http.StatusOK) }
func (a *adminUI) Cancel(c echo.Context) error        { return c.NoContent(http.StatusOK) }
func (a *adminUI) Report(c echo.Context) error        { return c.NoContent(http.StatusOK) }
func (a *adminUI) Receipts(c echo.Context) error      { return c.NoContent(http.StatusOK) }
func (a *adminUI) RepairReceipt(c echo.Context) error { return c.NoContent(http.StatusOK) }
func (a *adminUI) Suggest(c echo.Context) error       { return c.NoContent(http.StatusOK) }
