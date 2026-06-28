package products

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

func (h *APIHandler) List(c echo.Context) error {
	var q ListQuery
	if err := c.Bind(&q); err != nil {
		return apperr.BadRequest("invalid query parameters")
	}
	q.Normalize()
	rows, total, err := h.svc.List(c.Request().Context(), q)
	if err != nil {
		return err
	}
	return response.Paged(c, rows, response.NewPageMeta(q.Page, q.Limit, total))
}

func (h *APIHandler) Get(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	p, err := h.svc.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.OK(c, p)
}

func (h *APIHandler) GetByBarcode(c echo.Context) error {
	p, err := h.svc.GetByBarcode(c.Request().Context(), c.Param("code"))
	if err != nil {
		return err
	}
	return response.OK(c, p)
}

// GenerateBarcode returns a fresh, unused EAN-13 for the "Generate" button next
// to barcode inputs (product form + label pages).
func (h *APIHandler) GenerateBarcode(c echo.Context) error {
	code, err := h.svc.GenerateBarcode(c.Request().Context())
	if err != nil {
		return err
	}
	return response.OK(c, map[string]string{"barcode": code})
}

func (h *APIHandler) Create(c echo.Context) error {
	var in CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	p, err := h.svc.Create(c.Request().Context(), in)
	if err != nil {
		return err
	}
	return response.Created(c, p)
}

func (h *APIHandler) Update(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in UpdateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	p, err := h.svc.Update(c.Request().Context(), id, in)
	if err != nil {
		return err
	}
	return response.OK(c, p)
}

func (h *APIHandler) Delete(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := h.svc.Delete(c.Request().Context(), id); err != nil {
		return err
	}
	return response.NoContent(c)
}

func RegisterAPI(e *echo.Echo, db *sqlx.DB, cfg *config.Config) {
	api := NewAPIHandler(NewService(db))
	jwt := middleware.JWTAuth(cfg.JWTSecret)
	manage := middleware.RequireRole("admin", "manager")

	g := e.Group("/api/products", jwt)
	g.GET("", api.List)
	g.GET("/:id", api.Get)
	g.GET("/barcode/generate", api.GenerateBarcode)
	g.GET("/barcode/:code", api.GetByBarcode)
	g.POST("", api.Create, manage)
	g.PUT("/:id", api.Update, manage)
	g.DELETE("/:id", api.Delete, manage)
}
