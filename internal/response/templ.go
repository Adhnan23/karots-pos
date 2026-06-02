package response

import (
	"net/http"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
)

// RenderPage renders a full HTML page (a component that wraps itself in the base
// layout). It sets the content type and a 200 status.
func RenderPage(c echo.Context, component templ.Component) error {
	return render(c, http.StatusOK, component)
}

// RenderFragment renders a bare Templ component for an HTMX partial swap.
// Optional trigger strings are emitted as HX-Trigger headers so the client can
// fire events (toasts, modal close, etc.).
func RenderFragment(c echo.Context, component templ.Component, trigger ...string) error {
	if len(trigger) > 0 && trigger[0] != "" {
		c.Response().Header().Set("HX-Trigger", trigger[0])
	}
	return render(c, http.StatusOK, component)
}

// RenderFragmentStatus is RenderFragment with an explicit status, used by the
// error handler to return error fragments with the right code while still
// letting HTMX swap the content (see hx-swap response handling).
func RenderFragmentStatus(c echo.Context, status int, component templ.Component, trigger ...string) error {
	if len(trigger) > 0 && trigger[0] != "" {
		c.Response().Header().Set("HX-Trigger", trigger[0])
	}
	return render(c, status, component)
}

func render(c echo.Context, status int, component templ.Component) error {
	c.Response().Header().Set(echo.HeaderContentType, echo.MIMETextHTMLCharsetUTF8)
	c.Response().WriteHeader(status)
	return component.Render(c.Request().Context(), c.Response().Writer)
}
