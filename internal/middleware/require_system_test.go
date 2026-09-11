package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"karots-pos/internal/apperr"

	"github.com/labstack/echo/v4"
)

// reqWithFlags builds an echo context whose request context carries the given
// UserFlags, mimicking what JWTAuth stashes.
func reqWithFlags(f UserFlags) echo.Context {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/system/appearance", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxFlagsKey, f))
	return e.NewContext(req, httptest.NewRecorder())
}

func TestRequireSystemUserBlocksNonSystem(t *testing.T) {
	h := RequireSystemUser()(func(c echo.Context) error { return c.String(http.StatusOK, "ok") })

	// Shop admin (IsSystem false) → 404, invisible.
	err := h(reqWithFlags(UserFlags{IsSystem: false}))
	if ae, ok := apperr.As(err); !ok || ae.Status != http.StatusNotFound {
		t.Fatalf("non-system: want 404 apperr, got %v", err)
	}

	// System/support account → passes through.
	if err := h(reqWithFlags(UserFlags{IsSystem: true})); err != nil {
		t.Fatalf("system user: want pass, got %v", err)
	}
}
