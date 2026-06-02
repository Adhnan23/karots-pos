package sales

import (
	"strconv"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/config"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

type APIHandler struct{ svc *Service }

func NewAPIHandler(svc *Service) *APIHandler { return &APIHandler{svc: svc} }

func (h *APIHandler) Create(c echo.Context) error {
	var in CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	detail, err := h.svc.Create(c.Request().Context(), in, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	return response.Created(c, detail)
}

func (h *APIHandler) Get(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	detail, err := h.svc.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.OK(c, detail)
}

func (h *APIHandler) Return(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	detail, err := h.svc.Return(c.Request().Context(), id, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	return response.OK(c, detail)
}

func (h *APIHandler) PartialReturn(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in PartialReturnInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	detail, err := h.svc.PartialReturn(c.Request().Context(), id, in, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	return response.OK(c, detail)
}

func (h *APIHandler) List(c echo.Context) error {
	f := ListFilter{Status: c.QueryParam("status")}
	if v := c.QueryParam("cashier_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return apperr.BadRequest("invalid cashier_id")
		}
		f.CashierID = &id
	}
	if v := c.QueryParam("from"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return apperr.BadRequest("from must be YYYY-MM-DD")
		}
		f.From = &t
	}
	if v := c.QueryParam("to"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return apperr.BadRequest("to must be YYYY-MM-DD")
		}
		t = t.Add(24 * time.Hour)
		f.To = &t
	}
	rows, err := h.svc.List(c.Request().Context(), f)
	if err != nil {
		return err
	}
	return response.OK(c, rows)
}

func RegisterAPI(e *echo.Echo, db *sqlx.DB, cfg *config.Config) {
	api := NewAPIHandler(NewService(db))
	jwt := middleware.JWTAuth(cfg.JWTSecret)
	g := e.Group("/api/sales", jwt)
	g.POST("", api.Create)
	g.GET("/:id", api.Get)
	g.POST("/:id/return", api.Return, middleware.RequireRole("admin", "manager"))
	g.POST("/:id/partial-return", api.PartialReturn, middleware.RequireRole("admin", "manager"))
	g.GET("", api.List, middleware.RequireRole("admin", "manager"))
}
