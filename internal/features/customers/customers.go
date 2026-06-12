// Package customers manages registered customers, especially credit ("vade")
// buyers. Walk-in sales leave customer_id NULL.
package customers

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
	"karots-pos/internal/money"
	"karots-pos/internal/response"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

type Customer struct {
	ID                 int64           `db:"id"                  json:"id"`
	Name               string          `db:"name"                json:"name"`
	Phone              *string         `db:"phone"               json:"phone,omitempty"`
	Address            *string         `db:"address"             json:"address,omitempty"`
	CreditLimit        decimal.Decimal `db:"credit_limit"        json:"credit_limit"`
	OutstandingBalance decimal.Decimal `db:"outstanding_balance" json:"outstanding_balance"`
	LoyaltyPoints      int             `db:"loyalty_points"      json:"loyalty_points"`
	IsActive           bool            `db:"is_active"           json:"is_active"`
	CreatedAt          time.Time       `db:"created_at"          json:"created_at"`
}

// AvailableCredit is how much more this customer may borrow.
func (c Customer) AvailableCredit() decimal.Decimal {
	return c.CreditLimit.Sub(c.OutstandingBalance)
}

type CreateInput struct {
	Name        string  `json:"name"         form:"name"         validate:"required,min=2,max=100"`
	Phone       *string `json:"phone"        form:"phone"        validate:"omitempty,max=15"`
	Address     *string `json:"address"      form:"address"`
	CreditLimit string  `json:"credit_limit" form:"credit_limit"`
}

type UpdateInput = CreateInput

type PaymentInput struct {
	Amount string `json:"amount" form:"amount" validate:"required"`
}

type Repository struct{ q db.Queryer }

func NewRepository(q db.Queryer) *Repository { return &Repository{q: q} }

func (r *Repository) List(ctx context.Context, search string) ([]Customer, error) {
	var rows []Customer
	var s *string
	if strings.TrimSpace(search) != "" {
		s = &search
	}
	err := r.q.SelectContext(ctx, &rows, `
		SELECT * FROM customers
		WHERE is_active = true
		  AND ($1::text IS NULL OR name ILIKE '%' || $1 || '%' OR phone ILIKE '%' || $1 || '%')
		ORDER BY name LIMIT 100`, s)
	return rows, err
}

// OwingRow is a customer with an outstanding balance, plus the date of their
// oldest unpaid credit sale (a proxy for aging — the system tracks an aggregate
// balance, not per-invoice allocation).
type OwingRow struct {
	Customer
	OldestCredit *time.Time `db:"oldest_credit" json:"oldest_credit,omitempty"`
}

// Owing lists active customers who currently owe money, biggest balance first.
func (r *Repository) Owing(ctx context.Context) ([]OwingRow, error) {
	var rows []OwingRow
	err := r.q.SelectContext(ctx, &rows, `
		SELECT c.*,
		       (SELECT MIN(s.created_at) FROM sales s
		         WHERE s.customer_id = c.id AND s.status = 'credit') AS oldest_credit
		FROM customers c
		WHERE c.is_active = true AND c.outstanding_balance > 0
		ORDER BY c.outstanding_balance DESC`)
	return rows, err
}

func (r *Repository) FindByID(ctx context.Context, id int64) (*Customer, error) {
	var c Customer
	err := r.q.GetContext(ctx, &c, `SELECT * FROM customers WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *Repository) Create(ctx context.Context, name string, phone, address *string, limit decimal.Decimal) (*Customer, error) {
	var c Customer
	err := r.q.GetContext(ctx, &c, `
		INSERT INTO customers (name, phone, address, credit_limit)
		VALUES ($1,$2,$3,$4) RETURNING *`, name, phone, address, limit)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// AddBalance increments outstanding balance (used inside the sale tx for the
// credit portion). Pass a negative delta for repayments.
func (r *Repository) AddBalance(ctx context.Context, id int64, delta decimal.Decimal) error {
	_, err := r.q.ExecContext(ctx,
		`UPDATE customers SET outstanding_balance = outstanding_balance + $1 WHERE id = $2`,
		delta, id)
	return err
}

// ListAll includes inactive customers (for the admin list, so they can be
// reactivated). Active first, then by name. Search matches name/phone.
func (r *Repository) ListAll(ctx context.Context, search string) ([]Customer, error) {
	var rows []Customer
	var s *string
	if strings.TrimSpace(search) != "" {
		s = &search
	}
	err := r.q.SelectContext(ctx, &rows, `
		SELECT * FROM customers
		WHERE ($1::text IS NULL OR name ILIKE '%' || $1 || '%' OR phone ILIKE '%' || $1 || '%')
		ORDER BY is_active DESC, name LIMIT 100`, s)
	return rows, err
}

func (r *Repository) Deactivate(ctx context.Context, id int64) error {
	_, err := r.q.ExecContext(ctx, `UPDATE customers SET is_active=false WHERE id=$1`, id)
	return err
}

func (r *Repository) Reactivate(ctx context.Context, id int64) error {
	_, err := r.q.ExecContext(ctx, `UPDATE customers SET is_active=true WHERE id=$1`, id)
	return err
}

func (r *Repository) Update(ctx context.Context, id int64, name string, phone, address *string, limit decimal.Decimal) error {
	res, err := r.q.ExecContext(ctx,
		`UPDATE customers SET name=$1, phone=$2, address=$3, credit_limit=$4 WHERE id=$5 AND is_active=true`,
		name, phone, address, limit, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

type Service struct {
	db   *sqlx.DB
	repo *Repository
}

func NewService(db *sqlx.DB) *Service { return &Service{db: db, repo: NewRepository(db)} }

func (s *Service) List(ctx context.Context, search string) ([]Customer, error) {
	rows, err := s.repo.List(ctx, search)
	if err != nil {
		return nil, apperr.Internal("failed to list customers", err)
	}
	return rows, nil
}

// Owing lists customers with an outstanding balance (for the dues report).
func (s *Service) Owing(ctx context.Context) ([]OwingRow, error) {
	rows, err := s.repo.Owing(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to list customer dues", err)
	}
	return rows, nil
}

func (s *Service) Get(ctx context.Context, id int64) (*Customer, error) {
	c, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("customer")
		}
		return nil, apperr.Internal("failed to load customer", err)
	}
	return c, nil
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*Customer, error) {
	limit, err := money.Parse(in.CreditLimit)
	if err != nil || limit.IsNegative() {
		return nil, apperr.Validation("credit limit must be a non-negative amount")
	}
	c, err := s.repo.Create(ctx, strings.TrimSpace(in.Name), in.Phone, in.Address, limit)
	if err != nil {
		return nil, apperr.Internal("failed to create customer", err)
	}
	return c, nil
}

func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) error {
	limit, err := money.Parse(in.CreditLimit)
	if err != nil || limit.IsNegative() {
		return apperr.Validation("credit limit must be a non-negative amount")
	}
	err = s.repo.Update(ctx, id, strings.TrimSpace(in.Name), in.Phone, in.Address, limit)
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFound("customer")
	}
	if err != nil {
		return apperr.Internal("failed to update customer", err)
	}
	return nil
}

// ListAll returns active + inactive customers for the admin list.
func (s *Service) ListAll(ctx context.Context, search string) ([]Customer, error) {
	rows, err := s.repo.ListAll(ctx, search)
	if err != nil {
		return nil, apperr.Internal("failed to list customers", err)
	}
	return rows, nil
}

// Delete soft-deletes a customer (sets is_active=false). Sales/credit history is
// preserved; the customer just drops out of the active list and POS picker.
func (s *Service) Delete(ctx context.Context, id int64) error {
	if err := s.repo.Deactivate(ctx, id); err != nil {
		return apperr.Internal("failed to remove customer", err)
	}
	return nil
}

// Reactivate restores a soft-deleted customer.
func (s *Service) Reactivate(ctx context.Context, id int64) error {
	if err := s.repo.Reactivate(ctx, id); err != nil {
		return apperr.Internal("failed to reactivate customer", err)
	}
	return nil
}

// RecordPayment reduces a customer's outstanding credit balance.
func (s *Service) RecordPayment(ctx context.Context, id int64, in PaymentInput) error {
	amt, err := money.Parse(in.Amount)
	if err != nil || !amt.IsPositive() {
		return apperr.Validation("payment amount must be greater than zero")
	}
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	if err := s.repo.AddBalance(ctx, id, amt.Neg()); err != nil {
		return apperr.Internal("failed to record payment", err)
	}
	return nil
}

type APIHandler struct{ svc *Service }

func NewAPIHandler(svc *Service) *APIHandler { return &APIHandler{svc: svc} }

func (h *APIHandler) List(c echo.Context) error {
	rows, err := h.svc.List(c.Request().Context(), c.QueryParam("search"))
	if err != nil {
		return err
	}
	return response.OK(c, rows)
}

func (h *APIHandler) Get(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	cust, err := h.svc.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.OK(c, cust)
}

func (h *APIHandler) Create(c echo.Context) error {
	var in CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	cust, err := h.svc.Create(c.Request().Context(), in)
	if err != nil {
		return err
	}
	return response.Created(c, cust)
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

func (h *APIHandler) Payment(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in PaymentInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := h.svc.RecordPayment(c.Request().Context(), id, in); err != nil {
		return err
	}
	return response.NoContent(c)
}

func RegisterAPI(e *echo.Echo, db *sqlx.DB, cfg *config.Config) {
	api := NewAPIHandler(NewService(db))
	g := e.Group("/api/customers", middleware.JWTAuth(cfg.JWTSecret))
	g.GET("", api.List)
	g.GET("/:id", api.Get)
	g.POST("", api.Create, middleware.RequireRole("admin", "manager", "cashier"))
	g.PUT("/:id", api.Update, middleware.RequireRole("admin", "manager"))
	g.POST("/:id/payment", api.Payment, middleware.RequireRole("admin", "manager", "cashier"))
}
