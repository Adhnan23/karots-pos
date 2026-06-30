package web

import (
	"net/http"
	"strconv"

	"karots-pos/internal/features/theme"
	"karots-pos/internal/response"
	"karots-pos/templates/layouts"
	adminpages "karots-pos/templates/pages/admin"

	"github.com/labstack/echo/v4"
)

// Appearance renders the theme management section (swatch list + create form).
func (h *adminUI) Appearance(c echo.Context) error {
	ctx := c.Request().Context()
	themes, err := h.s.theme.List(ctx)
	if err != nil {
		return err
	}
	active, err := h.s.theme.Active(ctx)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.AppearanceSection(themes, active.ID))
}

// ThemeActivate sets the active theme and refreshes the page so the new
// CSS variables take effect everywhere (HX-Refresh triggers a full reload).
func (h *adminUI) ThemeActivate(c echo.Context) error {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if err := h.s.theme.SetActive(c.Request().Context(), id); err != nil {
		return err
	}
	c.Response().Header().Set("HX-Refresh", "true")
	return c.NoContent(http.StatusOK)
}

// ThemeCreate adds a custom theme then re-renders the section.
func (h *adminUI) ThemeCreate(c echo.Context) error {
	var in theme.Input
	if err := c.Bind(&in); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid form")
	}
	if in.Accent != nil && *in.Accent == "" {
		in.Accent = nil
	}
	if _, err := h.s.theme.Create(c.Request().Context(), in); err != nil {
		return err
	}
	return h.Appearance(c)
}

// ThemeDelete removes a custom theme then re-renders the section.
func (h *adminUI) ThemeDelete(c echo.Context) error {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if err := h.s.theme.Delete(c.Request().Context(), id); err != nil {
		return err
	}
	return h.Appearance(c)
}

// ThemeSwitchFragment renders just the quick-switch dropdown (lazy-loaded by the
// admin shell).
func (h *adminUI) ThemeSwitchFragment(c echo.Context) error {
	ctx := c.Request().Context()
	themes, err := h.s.theme.List(ctx)
	if err != nil {
		return err
	}
	active, err := h.s.theme.Active(ctx)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, layouts.ThemeSwitch(themes, active.ID))
}
