package productgroups

import (
	"strconv"

	"karots-pos/internal/apperr"
	"karots-pos/internal/config"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

type APIHandler struct{ svc *Service }

func NewAPIHandler(svc *Service) *APIHandler { return &APIHandler{svc: svc} }

// Top returns the top-level menu groups for the till's default (non-search) view.
func (h *APIHandler) Top(c echo.Context) error {
	groups, err := h.svc.Children(c.Request().Context(), nil)
	if err != nil {
		return err
	}
	return response.OK(c, map[string]any{"groups": groups})
}

// View returns one group's subgroups + linked products + breadcrumb path.
func (h *APIHandler) View(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	g, err := h.svc.Get(ctx, id)
	if err != nil {
		return err
	}
	children, err := h.svc.Children(ctx, &id)
	if err != nil {
		return err
	}
	prods, err := h.svc.Products(ctx, id)
	if err != nil {
		return err
	}
	crumb, err := h.svc.Breadcrumb(ctx, id)
	if err != nil {
		return err
	}
	return response.OK(c, map[string]any{
		"group": g, "breadcrumb": crumb, "children": children, "products": prods,
	})
}

// RegisterAPI mounts the cashier-facing read endpoints. Any signed-in till user
// may read the menu; management lives in the admin UI.
func RegisterAPI(e *echo.Echo, db *sqlx.DB, cfg *config.Config) {
	api := NewAPIHandler(NewService(db))
	jwt := middleware.JWTAuth(cfg.JWTSecret)

	g := e.Group("/api/groups", jwt)
	g.GET("", api.Top)
	g.GET("/:id", api.View)
}
