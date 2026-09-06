package aicatalog

import (
	"net/http"

	"karots-pos/internal/middleware"
	"karots-pos/internal/response"

	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

type adminUI struct{ p *Plugin }

func (a *adminUI) Page(c echo.Context) error {
	ctx := c.Request().Context()
	cfg, err := a.p.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, Page(PageData{
		UserName:      middleware.CurrentUserName(c),
		Provider:      cfg.Provider,
		BaseURL:       cfg.BaseURL,
		Model:         cfg.Model,
		HasKey:        cfg.APIKey != "",
		DefaultMarkup: cfg.DefaultMarkup.String(),
	}))
}

func (a *adminUI) SaveSettings(c echo.Context) error {
	ctx := c.Request().Context()
	cur, err := a.p.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	cur.Provider = c.FormValue("provider")
	cur.BaseURL = c.FormValue("base_url")
	cur.Model = c.FormValue("model")
	// Only overwrite the key when a new one is typed (the form shows a
	// placeholder, never the stored key), so saving other fields doesn't wipe it.
	if k := c.FormValue("api_key"); k != "" {
		cur.APIKey = k
	}
	if m, e := decimal.NewFromString(c.FormValue("default_markup")); e == nil && m.GreaterThan(decimal.NewFromInt(1)) {
		cur.DefaultMarkup = m
	}
	if err := a.p.store.SaveSettings(ctx, cur); err != nil {
		return err
	}
	return c.Redirect(http.StatusSeeOther, "/admin/ai-catalog")
}

func (a *adminUI) TestKey(c echo.Context) error {
	cfg, err := a.p.store.GetSettings(c.Request().Context())
	if err != nil {
		return err
	}
	if err := NewClient(cfg).Ping(c.Request().Context()); err != nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("AI test failed: "+err.Error(), "error"))
		return response.NoContent(c)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("AI key works ✓", "success"))
	return response.NoContent(c)
}

// --- stepper stubs, implemented in Tasks 4-5 ---
func (a *adminUI) Identify(c echo.Context) error { return response.NoContent(c) }
func (a *adminUI) Pick(c echo.Context) error     { return response.NoContent(c) }
func (a *adminUI) Category(c echo.Context) error { return response.NoContent(c) }
func (a *adminUI) Barcode(c echo.Context) error  { return response.NoContent(c) }
func (a *adminUI) Price(c echo.Context) error    { return response.NoContent(c) }
func (a *adminUI) Qty(c echo.Context) error      { return response.NoContent(c) }
func (a *adminUI) Save(c echo.Context) error     { return response.NoContent(c) }
