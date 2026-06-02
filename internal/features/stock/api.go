package stock

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

func (h *APIHandler) Movements(c echo.Context) error {
	var productID *int64
	if v := c.QueryParam("product_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return apperr.BadRequest("invalid product_id")
		}
		productID = &id
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	rows, err := h.svc.Movements(c.Request().Context(), productID, c.QueryParam("type"), limit)
	if err != nil {
		return err
	}
	return response.OK(c, rows)
}

func (h *APIHandler) Adjust(c echo.Context) error {
	var in AdjustInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := h.svc.Adjust(c.Request().Context(), in, middleware.CurrentUserID(c)); err != nil {
		return err
	}
	return response.NoContent(c)
}

func (h *APIHandler) Damage(c echo.Context) error {
	var in DamageInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := h.svc.Damage(c.Request().Context(), in, middleware.CurrentUserID(c)); err != nil {
		return err
	}
	return response.NoContent(c)
}

func RegisterAPI(e *echo.Echo, db *sqlx.DB, cfg *config.Config) {
	api := NewAPIHandler(NewService(db))
	g := e.Group("/api/stock", middleware.JWTAuth(cfg.JWTSecret))
	g.GET("/movements", api.Movements)
	g.POST("/adjust", api.Adjust, middleware.RequireRole("admin", "manager"))
	g.POST("/damage", api.Damage, middleware.RequireRole("admin", "manager"))
}
