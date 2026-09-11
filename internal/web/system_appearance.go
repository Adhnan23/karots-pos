package web

import (
	"karots-pos/internal/escpos"
	"karots-pos/internal/features/settings"
	"karots-pos/internal/response"
	systempages "karots-pos/templates/pages/system"

	"github.com/labstack/echo/v4"
)

// systemUI serves vendor/system-user-only maintenance pages. Routes are gated by
// middleware.RequireSystemUser (404 for the shop admin).
type systemUI struct{ settings *settings.Service }

// AppearanceForm renders the locked appearance chooser with the current values.
func (h *systemUI) AppearanceForm(c echo.Context) error {
	cur, err := h.settings.Get(c.Request().Context())
	if err != nil {
		return err
	}
	return response.RenderPage(c, systempages.AppearancePanel(cur, false))
}

// AppearanceSave validates and persists skin/density/receipt-style (unknown
// values fall back to defaults in the service), then re-renders with the new
// look live (base.templ re-reads it via the appearance middleware).
func (h *systemUI) AppearanceSave(c echo.Context) error {
	cur, err := h.settings.SetAppearance(c.Request().Context(),
		c.FormValue("skin"), c.FormValue("density"), c.FormValue("receipt_style"),
		c.FormValue("skin_custom"), c.FormValue("skin_radius"))
	if err != nil {
		return err
	}
	return response.RenderPage(c, systempages.AppearancePanel(cur, true))
}

// ReceiptPreview renders a sample receipt as plain text for the chosen style
// (real Header/Footer output, control codes stripped) so the picker shows how
// each style prints. Uses the shop's real name/address with the style overridden.
func (h *systemUI) ReceiptPreview(c echo.Context) error {
	cur, err := h.settings.Get(c.Request().Context())
	if err != nil {
		return err
	}
	cfg := *cur
	cfg.ReceiptStyle = settings.ValidReceiptStyle(c.QueryParam("receipt_style"))
	return response.RenderFragment(c, systempages.ReceiptPreview(escpos.SamplePreview(cfg)))
}
