package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// TestRequireSupplierAccess pins who may reach the supplier counter routes.
// Admins and managers always pass; a cashier passes only when the owner has
// switched the per-user flag on.
func TestRequireSupplierAccess(t *testing.T) {
	cases := []struct {
		name    string
		role    string
		flag    bool
		allowed bool
	}{
		{"admin without flag", "admin", false, true},
		{"manager without flag", "manager", false, true},
		{"plain cashier", "cashier", false, false},
		{"trusted cashier", "cashier", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/cashier/suppliers", nil)
			req = req.WithContext(context.WithValue(req.Context(), ctxFlagsKey, UserFlags{CanHandleSuppliers: tc.flag}))
			c := e.NewContext(req, httptest.NewRecorder())
			c.Set(ctxRole, tc.role)
			c.Set(ctxCanSuppliers, tc.flag)

			called := false
			h := RequireSupplierAccess()(func(echo.Context) error {
				called = true
				return nil
			})
			err := h(c)

			if tc.allowed && (err != nil || !called) {
				t.Fatalf("expected the request through, got err=%v called=%v", err, called)
			}
			if !tc.allowed && (err == nil || called) {
				t.Fatalf("expected a refusal, got err=%v called=%v", err, called)
			}
		})
	}
}

// TestCanHandleSuppliersCtx proves the flag survives into the request context,
// which is what templates read to decide whether to draw the Suppliers tab.
func TestCanHandleSuppliersCtx(t *testing.T) {
	base := context.Background()
	if CanHandleSuppliersCtx(base) {
		t.Fatal("a bare context must not grant supplier access")
	}
	withFlag := context.WithValue(base, ctxFlagsKey, UserFlags{CanHandleSuppliers: true})
	if !CanHandleSuppliersCtx(withFlag) {
		t.Fatal("the stashed flag was not read back")
	}
}
