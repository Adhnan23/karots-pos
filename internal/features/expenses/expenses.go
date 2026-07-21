// Package expenses records shop running costs (rent, salaries, utilities, ...).
package expenses

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

type Expense struct {
	ID          int64           `db:"id"           json:"id"`
	Category    string          `db:"category"     json:"category"`
	Amount      decimal.Decimal `db:"amount"       json:"amount"`
	Description *string         `db:"description"  json:"description,omitempty"`
	PaidBy      *int64          `db:"paid_by"      json:"paid_by,omitempty"`
	ExpenseDate time.Time       `db:"expense_date" json:"expense_date"`
	CreatedAt   time.Time       `db:"created_at"   json:"created_at"`
	PaidByName  *string         `db:"paid_by_name" json:"paid_by_name,omitempty"`
	// ServiceProductID attributes an operating cost (a toner change, a machine
	// repair) to the service it belongs to. It is NOT per-unit consumption and
	// never enters COGS: the core P&L still counts it once, as an expense.
	ServiceProductID *int64  `db:"service_product_id" json:"service_product_id,omitempty"`
	ServiceName      *string `db:"service_name"       json:"service_name,omitempty"`
}

type CreateInput struct {
	Category    string  `json:"category"     form:"category"     validate:"required,min=1,max=80"`
	Amount      string  `json:"amount"       form:"amount"       validate:"required"`
	Description *string `json:"description"  form:"description"`
	ExpenseDate string  `json:"expense_date" form:"expense_date"`
	// ServiceProductID is optional; 0 or absent means "not attributed".
	ServiceProductID int64 `json:"service_product_id" form:"service_product_id"`
}

// Filter narrows the expense list by date range.
type Filter struct {
	From *time.Time
	To   *time.Time
}

type Repository struct{ q db.Queryer }

func NewRepository(q db.Queryer) *Repository { return &Repository{q: q} }

func (r *Repository) List(ctx context.Context, f Filter) ([]Expense, error) {
	var rows []Expense
	err := r.q.SelectContext(ctx, &rows, `
		SELECT e.*, u.name AS paid_by_name, sp.name AS service_name
		FROM expenses e
		LEFT JOIN users u ON u.id = e.paid_by
		LEFT JOIN products sp ON sp.id = e.service_product_id
		WHERE ($1::date IS NULL OR e.expense_date >= $1)
		  AND ($2::date IS NULL OR e.expense_date <= $2)
		ORDER BY e.expense_date DESC, e.id DESC
		LIMIT 500`, f.From, f.To)
	return rows, err
}

func (r *Repository) Create(ctx context.Context, category string, amount decimal.Decimal, desc *string, paidBy *int64, date time.Time, serviceID *int64) (*Expense, error) {
	var e Expense
	err := r.q.GetContext(ctx, &e, `
		INSERT INTO expenses (category, amount, description, paid_by, expense_date, service_product_id)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING *, NULL::varchar AS paid_by_name, NULL::varchar AS service_name`,
		category, amount, desc, paidBy, date, serviceID)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (r *Repository) FindByID(ctx context.Context, id int64) (*Expense, error) {
	var e Expense
	err := r.q.GetContext(ctx, &e, `
		SELECT e.*, u.name AS paid_by_name, sp.name AS service_name
		FROM expenses e
		LEFT JOIN users u ON u.id = e.paid_by
		LEFT JOIN products sp ON sp.id = e.service_product_id
		WHERE e.id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (r *Repository) Update(ctx context.Context, id int64, category string, amount decimal.Decimal, desc *string, date time.Time, serviceID *int64) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE expenses SET category=$1, amount=$2, description=$3, expense_date=$4, service_product_id=$5
		WHERE id=$6`,
		category, amount, desc, date, serviceID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) Delete(ctx context.Context, id int64) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM expenses WHERE id = $1`, id)
	return err
}

// TotalBetween sums expenses in [from, to]; used by the finance report.
func (r *Repository) TotalBetween(ctx context.Context, from, to time.Time) (decimal.Decimal, error) {
	var total decimal.Decimal
	err := r.q.GetContext(ctx, &total,
		`SELECT COALESCE(SUM(amount),0) FROM expenses WHERE expense_date >= $1 AND expense_date <= $2`,
		from, to)
	return total, err
}

type Service struct{ repo *Repository }

func NewService(db *sqlx.DB) *Service { return &Service{repo: NewRepository(db)} }

func (s *Service) List(ctx context.Context, f Filter) ([]Expense, error) {
	rows, err := s.repo.List(ctx, f)
	if err != nil {
		return nil, apperr.Internal("failed to list expenses", err)
	}
	return rows, nil
}

// parseCreate validates and normalizes a create form into its stored fields.
func parseCreate(in CreateInput, userID int64) (category string, amt decimal.Decimal, desc *string, paidBy *int64, date time.Time, err error) {
	amt, perr := money.Parse(in.Amount)
	if perr != nil || !amt.IsPositive() {
		return "", amt, nil, nil, date, apperr.Validation("amount must be greater than zero")
	}
	date = time.Now()
	if strings.TrimSpace(in.ExpenseDate) != "" {
		date, perr = time.Parse("2006-01-02", in.ExpenseDate)
		if perr != nil {
			return "", amt, nil, nil, date, apperr.Validation("date must be YYYY-MM-DD")
		}
	}
	if userID > 0 {
		paidBy = &userID
	}
	return strings.TrimSpace(in.Category), amt, in.Description, paidBy, date, nil
}

// serviceRef turns the form's 0-means-none into a nullable reference.
func serviceRef(id int64) *int64 {
	if id <= 0 {
		return nil
	}
	return &id
}

func (s *Service) Create(ctx context.Context, in CreateInput, userID int64) (*Expense, error) {
	category, amt, desc, paidBy, date, err := parseCreate(in, userID)
	if err != nil {
		return nil, err
	}
	e, err := s.repo.Create(ctx, category, amt, desc, paidBy, date, serviceRef(in.ServiceProductID))
	if err != nil {
		return nil, apperr.Internal("failed to record expense", err)
	}
	return e, nil
}

// CreateInTx records an expense over an existing transaction, so the caller can
// book the matching cash move (cashflow.MoveTx) atomically in the same tx.
func (s *Service) CreateInTx(ctx context.Context, q db.Queryer, in CreateInput, userID int64) (*Expense, error) {
	category, amt, desc, paidBy, date, err := parseCreate(in, userID)
	if err != nil {
		return nil, err
	}
	e, err := NewRepository(q).Create(ctx, category, amt, desc, paidBy, date, serviceRef(in.ServiceProductID))
	if err != nil {
		return nil, apperr.Internal("failed to record expense", err)
	}
	return e, nil
}

func (s *Service) Get(ctx context.Context, id int64) (*Expense, error) {
	e, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("expense")
		}
		return nil, apperr.Internal("failed to load expense", err)
	}
	return e, nil
}

func (s *Service) Update(ctx context.Context, id int64, in CreateInput) error {
	amt, err := money.Parse(in.Amount)
	if err != nil || !amt.IsPositive() {
		return apperr.Validation("amount must be greater than zero")
	}
	date := time.Now()
	if strings.TrimSpace(in.ExpenseDate) != "" {
		date, err = time.Parse("2006-01-02", in.ExpenseDate)
		if err != nil {
			return apperr.Validation("date must be YYYY-MM-DD")
		}
	}
	err = s.repo.Update(ctx, id, strings.TrimSpace(in.Category), amt, in.Description, date, serviceRef(in.ServiceProductID))
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFound("expense")
	}
	if err != nil {
		return apperr.Internal("failed to update expense", err)
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return apperr.Internal("failed to delete expense", err)
	}
	return nil
}

type APIHandler struct{ svc *Service }

func NewAPIHandler(svc *Service) *APIHandler { return &APIHandler{svc: svc} }

func (h *APIHandler) List(c echo.Context) error {
	rows, err := h.svc.List(c.Request().Context(), Filter{})
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
	e, err := h.svc.Create(c.Request().Context(), in, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	return response.Created(c, e)
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
	g := e.Group("/api/expenses", middleware.JWTAuth(cfg.JWTSecret), middleware.RequireRole("admin", "manager"))
	g.GET("", api.List)
	g.POST("", api.Create)
	g.DELETE("/:id", api.Delete)
}
