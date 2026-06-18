package plugin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func get(t *testing.T, e *echo.Echo, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// A plugin route on the same admin path as a core route must override the core
// handler, and a brand-new plugin path must be reachable. This is the contract
// the recharge plugin relies on (override drawer open/close, add Reload).
func TestMuxOverrideAndAdd(t *testing.T) {
	e := echo.New()
	ag := e.Group("/admin")
	ag.GET("/x", func(c echo.Context) error { return c.String(200, "core") })

	m := NewMux()
	m.Admin().GET("/x", func(c echo.Context) error { return c.String(200, "plugin") }) // override
	m.Admin().GET("/y", func(c echo.Context) error { return c.String(200, "added") })  // additive
	m.Mount(e.Group(""), e.Group("/cashier"), ag)

	if code, body := get(t, e, "/admin/x"); body != "plugin" {
		t.Fatalf("override failed: got %q (code %d), want \"plugin\"", body, code)
	}
	if code, body := get(t, e, "/admin/y"); body != "added" {
		t.Fatalf("additive route failed: got %q (code %d), want \"added\"", body, code)
	}
}

// When two plugins register the same scope+method+path, the last registration
// wins (Mux dedups before mounting, so echo never sees a duplicate).
func TestMuxLastWriteWins(t *testing.T) {
	e := echo.New()
	m := NewMux()
	m.Admin().GET("/z", func(c echo.Context) error { return c.String(200, "first") })
	m.Admin().GET("/z", func(c echo.Context) error { return c.String(200, "second") })
	m.Mount(e.Group(""), e.Group("/cashier"), e.Group("/admin"))

	if code, body := get(t, e, "/admin/z"); body != "second" {
		t.Fatalf("last-write-wins failed: got %q (code %d), want \"second\"", body, code)
	}
}
