// Package suppliers manages wholesale suppliers and their outstanding payables.
package suppliers

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
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

type Supplier struct {
	ID                 int64           `db:"id"                  json:"id"`
	Name               string          `db:"name"                json:"name"`
	ContactPerson      *string         `db:"contact_person"      json:"contact_person,omitempty"`
	Phone              *string         `db:"phone"               json:"phone,omitempty"`
	Address            *string         `db:"address"             json:"address,omitempty"`
	CreditDays         int             `db:"credit_days"         json:"credit_days"`
	OutstandingBalance decimal.Decimal `db:"outstanding_balance" json:"outstanding_balance"`
	IsActive           bool            `db:"is_active"           json:"is_active"`
	CreatedAt          time.Time       `db:"created_at"          json:"created_at"`
}

type CreateInput struct {
	Name          string  `json:"name"           form:"name"           validate:"required,min=2,max=150"`
	ContactPerson *string `json:"contact_person" form:"contact_person"`
	Phone         *string `json:"phone"          form:"phone"          validate:"omitempty,max=15"`
	Address       *string `json:"address"        form:"address"`
	CreditDays    int     `json:"credit_days"    form:"credit_days"    validate:"gte=0"`
}

type UpdateInput = CreateInput

type Repository struct{ q db.Queryer }

func NewRepository(q db.Queryer) *Repository { return &Repository{q: q} }

func (r *Repository) List(ctx context.Context) ([]Supplier, error) {
	var rows []Supplier
	err := r.q.SelectContext(ctx, &rows, `SELECT * FROM suppliers WHERE is_active = true ORDER BY name`)
	return rows, err
}

// OwingRow is a supplier we owe money, plus the date of their oldest unpaid
// purchase (an aging proxy).
type OwingRow struct {
	Supplier
	OldestUnpaid *time.Time `db:"oldest_unpaid" json:"oldest_unpaid,omitempty"`
}

// Owing lists active suppliers with an outstanding payable, biggest first.
func (r *Repository) Owing(ctx context.Context) ([]OwingRow, error) {
	var rows []OwingRow
	err := r.q.SelectContext(ctx, &rows, `
		SELECT s.*,
		       (SELECT MIN(pu.created_at) FROM purchases pu
		         WHERE pu.supplier_id = s.id AND pu.status <> 'draft' AND pu.total > pu.paid_amount) AS oldest_unpaid
		FROM suppliers s
		WHERE s.is_active = true AND s.outstanding_balance > 0
		ORDER BY s.outstanding_balance DESC`)
	return rows, err
}

func (r *Repository) FindByID(ctx context.Context, id int64) (*Supplier, error) {
	var s Supplier
	err := r.q.GetContext(ctx, &s, `SELECT * FROM suppliers WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *Repository) Create(ctx context.Context, in CreateInput) (*Supplier, error) {
	var s Supplier
	err := r.q.GetContext(ctx, &s, `
		INSERT INTO suppliers (name, contact_person, phone, address, credit_days)
		VALUES ($1,$2,$3,$4,$5) RETURNING *`,
		in.Name, in.ContactPerson, in.Phone, in.Address, in.CreditDays)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *Repository) Update(ctx context.Context, id int64, in UpdateInput) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE suppliers SET name=$1, contact_person=$2, phone=$3, address=$4, credit_days=$5
		WHERE id=$6`, in.Name, in.ContactPerson, in.Phone, in.Address, in.CreditDays, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) Deactivate(ctx context.Context, id int64) error {
	_, err := r.q.ExecContext(ctx, `UPDATE suppliers SET is_active=false WHERE id=$1`, id)
	return err
}

// AddBalance changes a supplier's payable (used inside the purchase tx; pass a
// negative delta when paying the supplier).
func (r *Repository) AddBalance(ctx context.Context, id int64, delta decimal.Decimal) error {
	_, err := r.q.ExecContext(ctx,
		`UPDATE suppliers SET outstanding_balance = outstanding_balance + $1 WHERE id=$2`, delta, id)
	return err
}

type Service struct {
	db   *sqlx.DB
	repo *Repository
}

func NewService(db *sqlx.DB) *Service { return &Service{db: db, repo: NewRepository(db)} }

func (s *Service) List(ctx context.Context) ([]Supplier, error) {
	rows, err := s.repo.List(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to list suppliers", err)
	}
	return rows, nil
}

// Owing lists suppliers with an outstanding payable (for the dues report).
func (s *Service) Owing(ctx context.Context) ([]OwingRow, error) {
	rows, err := s.repo.Owing(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to list supplier dues", err)
	}
	return rows, nil
}

func (s *Service) Get(ctx context.Context, id int64) (*Supplier, error) {
	sup, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("supplier")
		}
		return nil, apperr.Internal("failed to load supplier", err)
	}
	return sup, nil
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*Supplier, error) {
	in.Name = strings.TrimSpace(in.Name)
	sup, err := s.repo.Create(ctx, in)
	if err != nil {
		return nil, apperr.Internal("failed to create supplier", err)
	}
	return sup, nil
}

func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) error {
	err := s.repo.Update(ctx, id, in)
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFound("supplier")
	}
	if err != nil {
		return apperr.Internal("failed to update supplier", err)
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	if err := s.repo.Deactivate(ctx, id); err != nil {
		return apperr.Internal("failed to remove supplier", err)
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
	var in CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	sup, err := h.svc.Create(c.Request().Context(), in)
	if err != nil {
		return err
	}
	return response.Created(c, sup)
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

func RegisterAPI(e *echo.Echo, db *sqlx.DB, cfg *config.Config) {
	api := NewAPIHandler(NewService(db))
	g := e.Group("/api/suppliers", middleware.JWTAuth(cfg.JWTSecret), middleware.RequireRole("admin", "manager"))
	g.GET("", api.List)
	g.POST("", api.Create)
	g.PUT("/:id", api.Update)
	g.DELETE("/:id", api.Delete)
}
