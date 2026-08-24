package productplus

import "github.com/labstack/echo/v4"

// adminUI hosts the plugin's own admin field-manager page. Stubs here are filled
// in Task 5.
type adminUI struct{ p *Plugin }

func (a *adminUI) Page(c echo.Context) error        { return nil }
func (a *adminUI) Table(c echo.Context) error       { return nil }
func (a *adminUI) FieldForm(c echo.Context) error   { return nil }
func (a *adminUI) CreateField(c echo.Context) error { return nil }
func (a *adminUI) UpdateField(c echo.Context) error { return nil }
func (a *adminUI) SetActive(c echo.Context) error   { return nil }
