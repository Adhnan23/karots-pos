package plugin

import "github.com/labstack/echo/v4"

// Scope groups routes by the middleware they run under: Public (none), Cashier
// (jwt + pinGuard) and Admin (jwt + pinGuard + RequireRole). The web layer owns
// the actual middleware; the Mux only records which scope a route belongs to.
type Scope int

const (
	Public Scope = iota
	Cashier
	Admin
)

type routeKey struct {
	scope  Scope
	method string
	path   string
}

type route struct {
	scope      Scope
	method     string
	path       string
	handler    echo.HandlerFunc
	middleware []echo.MiddlewareFunc
}

// Mux is a scoped route table with last-write-wins semantics: registering the
// same scope+method+path again replaces the handler. Core registers its routes
// first, then plugins register theirs (SetupAll), so a plugin route overrides
// the core one. Every route is mounted onto echo exactly once (Mount), which
// avoids echo's duplicate-route ambiguity.
type Mux struct {
	routes map[routeKey]route
	order  []routeKey // registration order, for stable mounting
}

// NewMux returns an empty Mux.
func NewMux() *Mux { return &Mux{routes: map[routeKey]route{}} }

func (m *Mux) add(scope Scope, method, path string, h echo.HandlerFunc, mw ...echo.MiddlewareFunc) {
	k := routeKey{scope, method, path}
	if _, exists := m.routes[k]; !exists {
		m.order = append(m.order, k)
	}
	m.routes[k] = route{scope, method, path, h, mw}
}

// Group is a scope-bound view of the Mux with the familiar echo verb methods.
type Group struct {
	mux   *Mux
	scope Scope
}

func (g Group) GET(path string, h echo.HandlerFunc, mw ...echo.MiddlewareFunc) {
	g.mux.add(g.scope, "GET", path, h, mw...)
}
func (g Group) POST(path string, h echo.HandlerFunc, mw ...echo.MiddlewareFunc) {
	g.mux.add(g.scope, "POST", path, h, mw...)
}
func (g Group) PUT(path string, h echo.HandlerFunc, mw ...echo.MiddlewareFunc) {
	g.mux.add(g.scope, "PUT", path, h, mw...)
}
func (g Group) DELETE(path string, h echo.HandlerFunc, mw ...echo.MiddlewareFunc) {
	g.mux.add(g.scope, "DELETE", path, h, mw...)
}

// Public/Cashier/Admin return scope-bound groups.
func (m *Mux) Public() Group  { return Group{m, Public} }
func (m *Mux) Cashier() Group { return Group{m, Cashier} }
func (m *Mux) Admin() Group   { return Group{m, Admin} }

// Mount registers every collected route onto the matching echo group. Call once,
// after core and all plugins have registered. The provided groups already carry
// their scope middleware (jwt, pinGuard, RequireRole) from the web layer.
func (m *Mux) Mount(public, cashier, admin *echo.Group) {
	for _, k := range m.order {
		r := m.routes[k]
		g := admin
		switch r.scope {
		case Public:
			g = public
		case Cashier:
			g = cashier
		}
		g.Add(r.method, r.path, r.handler, r.middleware...)
	}
}
