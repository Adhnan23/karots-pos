package web

import (
	"karots-pos/internal/features/settings"
	"karots-pos/internal/middleware"

	"github.com/labstack/echo/v4"
)

// withAppearance loads the system-user-locked look (skin + density) from the
// settings singleton and stashes the validated values in the request context so
// base.templ can stamp them on <html>. Server-side (not the client dark-mode
// script) so there is no flash of the wrong skin. A blank/unknown value falls
// back to the default via the settings validators; a load error leaves the
// context defaults in place rather than failing the page.
//
// ponytail: one settings-singleton read per request in this group. Fine for a
// single-shop POS (indexed id=1 row, user-paced navigation); add a cached read
// if it ever shows up in a profile.
func withAppearance(svc *settings.Service) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if s, err := svc.Get(c.Request().Context()); err == nil {
				ctx := middleware.SetAppearanceCtx(c.Request().Context(),
					settings.ValidSkin(s.Skin), settings.ValidDensity(s.Density))
				c.SetRequest(c.Request().WithContext(ctx))
			}
			return next(c)
		}
	}
}
