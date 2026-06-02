package middleware

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/templates/shared"

	"github.com/labstack/echo/v4"
)

// ErrorHandler is installed as Echo's central HTTPErrorHandler. It normalizes
// any error into a status + code + message, then renders it in the shape the
// caller expects:
//   - HTMX request            -> HX-Trigger "showToast" event, no DOM swap
//     (HTMX does not swap error-status bodies by default, so a header-driven
//     toast is the reliable way to surface errors on partial requests)
//   - browser page navigation -> redirect to /login on 401, else an error page
//   - API / JSON client       -> JSON error envelope
func ErrorHandler(isProd bool) echo.HTTPErrorHandler {
	return func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}

		status, code, msg := classify(err, isProd)
		isHTMX := c.Request().Header.Get("HX-Request") == "true"
		isAPI := strings.HasPrefix(c.Path(), "/api") || wantsJSON(c)

		// Unauthenticated UI traffic is bounced to the login screen.
		if status == http.StatusUnauthorized && !isAPI {
			if isHTMX {
				c.Response().Header().Set("HX-Redirect", "/login")
				_ = c.NoContent(http.StatusOK)
				return
			}
			_ = c.Redirect(http.StatusSeeOther, "/login")
			return
		}

		if isHTMX {
			c.Response().Header().Set("HX-Reswap", "none")
			c.Response().Header().Set("HX-Trigger", toastTrigger(msg, "error"))
			_ = c.NoContent(http.StatusOK)
			return
		}

		if isAPI {
			_ = c.JSON(status, map[string]any{
				"success": false,
				"error":   map[string]string{"code": code, "message": msg},
			})
			return
		}

		// Full-page browser navigation that errored.
		c.Response().Header().Set(echo.HeaderContentType, echo.MIMETextHTMLCharsetUTF8)
		c.Response().WriteHeader(status)
		_ = shared.ErrorPage(status, msg).Render(c.Request().Context(), c.Response().Writer)
	}
}

// toastTrigger builds the JSON payload for an HX-Trigger "show-toast" event.
func toastTrigger(message, level string) string {
	b, _ := json.Marshal(map[string]any{
		"show-toast": map[string]string{"message": message, "level": level},
	})
	return string(b)
}

func classify(err error, isProd bool) (status int, code, msg string) {
	if ae, ok := apperr.As(err); ok {
		return ae.Status, ae.Code, ae.Message
	}
	var he *echo.HTTPError
	if errors.As(err, &he) {
		m := http.StatusText(he.Code)
		if s, ok := he.Message.(string); ok && s != "" {
			m = s
		}
		return he.Code, "HTTP_ERROR", m
	}
	if isProd {
		return http.StatusInternalServerError, "INTERNAL_ERROR", "Something went wrong"
	}
	return http.StatusInternalServerError, "INTERNAL_ERROR", err.Error()
}

func wantsJSON(c echo.Context) bool {
	return strings.Contains(c.Request().Header.Get(echo.HeaderAccept), echo.MIMEApplicationJSON)
}
