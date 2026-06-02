// Package denominations manages the notes and coins the shop handles, so the
// cashier can count the drawer by piece count (how many of each) instead of
// typing a total. Admins add/remove/edit denominations as currency changes.
package denominations

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/config"
	"karots-pos/internal/db"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

type Denomination struct {
	ID        int64           `db:"id"         json:"id"`
	Value     decimal.Decimal `db:"value"      json:"value"`
	IsNote    bool            `db:"is_note"    json:"is_note"`
	IsActive  bool            `db:"is_active"  json:"is_active"`
	CreatedAt time.Time       `db:"created_at" json:"-"`
}

type Input struct {
	Value    string `json:"value"     form:"value"     validate:"required"`
	IsNote   bool   `json:"is_note"   form:"is_note"`
	IsActive bool   `json:"is_active" form:"is_active"`
}

type Repository struct{ db db.Queryer }

func NewRepository(q db.Queryer) *Repository { return &Repository{db: q} }

// List returns denominations; activeOnly restricts to those in circulation
// (used by the cashier counting grid). Ordered high → low for natural counting.
func (r *Repository) List(ctx context.Context, activeOnly bool) ([]Denomination, error) {
	var rows []Denomination
	q := `SELECT * FROM denominations`
	if activeOnly {
		q += ` WHERE is_active = true`
	}
	q += ` ORDER BY value DESC`
	err := r.db.SelectContext(ctx, &rows, q)
	return rows, err
}

func (r *Repository) FindByID(ctx context.Context, id int64) (*Denomination, error) {
	var d Denomination
	err := r.db.GetContext(ctx, &d, `SELECT * FROM denominations WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *Repository) Create(ctx context.Context, value decimal.Decimal, isNote, isActive bool) (*Denomination, error) {
	var d Denomination
	err := r.db.GetContext(ctx, &d,
		`INSERT INTO denominations (value, is_note, is_active) VALUES ($1, $2, $3) RETURNING *`,
		value, isNote, isActive)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *Repository) Update(ctx context.Context, id int64, value decimal.Decimal, isNote, isActive bool) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE denominations SET value = $1, is_note = $2, is_active = $3 WHERE id = $4`,
		value, isNote, isActive, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM denominations WHERE id = $1`, id)
	return err
}

type Service struct{ repo *Repository }

func NewService(q db.Queryer) *Service { return &Service{repo: NewRepository(q)} }

func (s *Service) List(ctx context.Context, activeOnly bool) ([]Denomination, error) {
	rows, err := s.repo.List(ctx, activeOnly)
	if err != nil {
		return nil, apperr.Internal("failed to list denominations", err)
	}
	return rows, nil
}

func (s *Service) Get(ctx context.Context, id int64) (*Denomination, error) {
	d, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("denomination")
		}
		return nil, apperr.Internal("failed to load denomination", err)
	}
	return d, nil
}

func (s *Service) Create(ctx context.Context, in Input) (*Denomination, error) {
	value, err := decimal.NewFromString(in.Value)
	if err != nil || !value.IsPositive() {
		return nil, apperr.Validation("value must be a positive amount")
	}
	d, err := s.repo.Create(ctx, value, in.IsNote, in.IsActive)
	if err != nil {
		return nil, apperr.Conflict("a denomination with that value already exists")
	}
	return d, nil
}

func (s *Service) Update(ctx context.Context, id int64, in Input) error {
	value, err := decimal.NewFromString(in.Value)
	if err != nil || !value.IsPositive() {
		return apperr.Validation("value must be a positive amount")
	}
	err = s.repo.Update(ctx, id, value, in.IsNote, in.IsActive)
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFound("denomination")
	}
	if err != nil {
		return apperr.Conflict("a denomination with that value already exists")
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return apperr.Internal("failed to delete denomination", err)
	}
	return nil
}

type APIHandler struct{ svc *Service }

func NewAPIHandler(svc *Service) *APIHandler { return &APIHandler{svc: svc} }

func (h *APIHandler) List(c echo.Context) error {
	// Cashiers counting the drawer want only active denominations; the admin
	// management screen asks for ?all=1 to see inactive ones too.
	rows, err := h.svc.List(c.Request().Context(), c.QueryParam("all") != "1")
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
	d, err := h.svc.Create(c.Request().Context(), in)
	if err != nil {
		return err
	}
	return response.Created(c, d)
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
	g := e.Group("/api/denominations", middleware.JWTAuth(cfg.JWTSecret))
	g.GET("", api.List)
	g.POST("", api.Create, middleware.RequireRole("admin", "manager"))
	g.PUT("/:id", api.Update, middleware.RequireRole("admin", "manager"))
	g.DELETE("/:id", api.Delete, middleware.RequireRole("admin", "manager"))
}
