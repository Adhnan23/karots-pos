// Package units manages units of measure (kg, pcs, ltr, ...).
package units

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"karots-pos/internal/apperr"
	"karots-pos/internal/config"
	"karots-pos/internal/db"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

type Unit struct {
	ID           int64  `db:"id"            json:"id"`
	Name         string `db:"name"          json:"name"`
	Abbreviation string `db:"abbreviation"  json:"abbreviation"`
	AllowDecimal bool   `db:"allow_decimal" json:"allow_decimal"`
}

type Input struct {
	Name         string `json:"name"          form:"name"          validate:"required,min=1,max=30"`
	Abbreviation string `json:"abbreviation"  form:"abbreviation"  validate:"required,min=1,max=10"`
	AllowDecimal bool   `json:"allow_decimal" form:"allow_decimal"`
}

type Repository struct{ db db.Queryer }

func NewRepository(q db.Queryer) *Repository { return &Repository{db: q} }

func (r *Repository) List(ctx context.Context) ([]Unit, error) {
	var rows []Unit
	err := r.db.SelectContext(ctx, &rows, `SELECT * FROM units ORDER BY name`)
	return rows, err
}

func (r *Repository) Create(ctx context.Context, name, abbr string, allowDecimal bool) (*Unit, error) {
	var u Unit
	err := r.db.GetContext(ctx, &u,
		`INSERT INTO units (name, abbreviation, allow_decimal) VALUES ($1, $2, $3) RETURNING *`, name, abbr, allowDecimal)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *Repository) Update(ctx context.Context, id int64, name, abbr string, allowDecimal bool) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE units SET name = $1, abbreviation = $2, allow_decimal = $3 WHERE id = $4`, name, abbr, allowDecimal, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) FindByID(ctx context.Context, id int64) (*Unit, error) {
	var u Unit
	err := r.db.GetContext(ctx, &u, `SELECT * FROM units WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *Repository) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM units WHERE id = $1`, id)
	return err
}

type Service struct{ repo *Repository }

func NewService(q db.Queryer) *Service { return &Service{repo: NewRepository(q)} }

func (s *Service) List(ctx context.Context) ([]Unit, error) {
	rows, err := s.repo.List(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to list units", err)
	}
	return rows, nil
}

func (s *Service) Get(ctx context.Context, id int64) (*Unit, error) {
	u, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("unit")
		}
		return nil, apperr.Internal("failed to load unit", err)
	}
	return u, nil
}

func (s *Service) Create(ctx context.Context, in Input) (*Unit, error) {
	u, err := s.repo.Create(ctx, in.Name, in.Abbreviation, in.AllowDecimal)
	if err != nil {
		return nil, apperr.Conflict("a unit with that name or abbreviation already exists")
	}
	return u, nil
}

func (s *Service) Update(ctx context.Context, id int64, in Input) error {
	err := s.repo.Update(ctx, id, in.Name, in.Abbreviation, in.AllowDecimal)
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFound("unit")
	}
	if err != nil {
		return apperr.Conflict("a unit with that name or abbreviation already exists")
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return apperr.Conflict("unit is in use and cannot be deleted")
	}
	return nil
}

type APIHandler struct{ svc *Service }

func NewAPIHandler(svc *Service) *APIHandler { return &APIHandler{svc: svc} }

func (h *APIHandler) List(c echo.Context) error {
	rows, err := h.svc.List(c.Request().Context())
	if err != nil {
		return err
	}
	return response.OK(c, rows)
}

func (h *APIHandler) Create(c echo.Context) error {
	var in Input
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	u, err := h.svc.Create(c.Request().Context(), in)
	if err != nil {
		return err
	}
	return response.Created(c, u)
}

func (h *APIHandler) Update(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in Input
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := h.svc.Update(c.Request().Context(), id, in); err != nil {
		return err
	}
	return response.NoContent(c)
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

func RegisterAPI(e *echo.Echo, sqlxDB *sqlx.DB, cfg *config.Config) {
	api := NewAPIHandler(NewService(sqlxDB))
	g := e.Group("/api/units", middleware.JWTAuth(cfg.JWTSecret))
	g.GET("", api.List)
	g.POST("", api.Create, middleware.RequireRole("admin", "manager"))
	g.PUT("/:id", api.Update, middleware.RequireRole("admin", "manager"))
	g.DELETE("/:id", api.Delete, middleware.RequireRole("admin", "manager"))
}
