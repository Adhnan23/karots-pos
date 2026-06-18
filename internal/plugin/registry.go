package plugin

import "github.com/labstack/echo/v4"

// Registry is handed to each plugin's Setup. It exposes the Core API, the scoped
// route Mux (with override semantics), the underlying echo instance for advanced
// needs, and the additive UI hooks (see hooks.go).
type Registry struct {
	Core Core
	Mux  *Mux
	echo *echo.Echo
}

// NewRegistry builds a registry from the core API, a Mux and the echo instance.
// The web layer calls this once during RegisterUI.
func NewRegistry(core Core, mux *Mux, e *echo.Echo) *Registry {
	return &Registry{Core: core, Mux: mux, echo: e}
}

// Echo exposes the raw echo instance for needs the scoped Mux doesn't cover
// (custom middleware, websocket upgrades, etc.). Prefer the scoped groups.
func (r *Registry) Echo() *echo.Echo { return r.echo }

// Public/Cashier/Admin are convenience accessors for the Mux's scoped groups.
func (r *Registry) Public() Group  { return r.Mux.Public() }
func (r *Registry) Cashier() Group { return r.Mux.Cashier() }
func (r *Registry) Admin() Group   { return r.Mux.Admin() }
