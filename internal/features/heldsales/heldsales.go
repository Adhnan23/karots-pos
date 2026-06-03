// Package heldsales stores parked/suspended carts so a cashier can hold the
// current sale, serve someone else, and resume it later. The cart payload is
// opaque JSON owned by the terminal; the server only totals/labels it for the
// "held" list and replays it verbatim on resume.
package heldsales

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/config"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

type HeldSale struct {
	ID         int64           `db:"id"          json:"id"`
	CashierID  int64           `db:"cashier_id"  json:"cashier_id"`
	Label      *string         `db:"label"       json:"label,omitempty"`
	SaleType   string          `db:"sale_type"   json:"sale_type"`
	CustomerID *int64          `db:"customer_id" json:"customer_id,omitempty"`
	Discount   decimal.Decimal `db:"discount"    json:"discount"`
	Cart       json.RawMessage `db:"cart"        json:"cart"`
	ItemCount  int             `db:"item_count"  json:"item_count"`
	Total      decimal.Decimal `db:"total"       json:"total"`
	CreatedAt  time.Time       `db:"created_at"  json:"created_at"`
}

type Input struct {
	Label      string          `json:"label"`
	SaleType   string          `json:"sale_type"`
	CustomerID *int64          `json:"customer_id"`
	Discount   string          `json:"discount"`
	Cart       json.RawMessage `json:"cart" validate:"required"`
	ItemCount  int             `json:"item_count"`
	Total      string          `json:"total"`
}

type Repository struct{ q appdb.Queryer }

func NewRepository(q appdb.Queryer) *Repository { return &Repository{q: q} }

func (r *Repository) List(ctx context.Context, cashierID int64) ([]HeldSale, error) {
	var rows []HeldSale
	err := r.q.SelectContext(ctx, &rows,
		`SELECT * FROM held_sales WHERE cashier_id = $1 ORDER BY created_at DESC`, cashierID)
	return rows, err
}

func (r *Repository) Create(ctx context.Context, h HeldSale) (*HeldSale, error) {
	var out HeldSale
	err := r.q.GetContext(ctx, &out, `
		INSERT INTO held_sales (cashier_id, label, sale_type, customer_id, discount, cart, item_count, total)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING *`,
		h.CashierID, h.Label, h.SaleType, h.CustomerID, h.Discount, h.Cart, h.ItemCount, h.Total)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete removes a held sale, scoped to its owner so a cashier can't drop
// another cashier's parked cart.
func (r *Repository) Delete(ctx context.Context, id, cashierID int64) error {
	res, err := r.q.ExecContext(ctx, `DELETE FROM held_sales WHERE id = $1 AND cashier_id = $2`, id, cashierID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperr.NotFound("held sale")
	}
	return nil
}

type Service struct {
	db   *sqlx.DB
	repo *Repository
}

func NewService(db *sqlx.DB) *Service { return &Service{db: db, repo: NewRepository(db)} }

func (s *Service) List(ctx context.Context, cashierID int64) ([]HeldSale, error) {
	rows, err := s.repo.List(ctx, cashierID)
	if err != nil {
		return nil, apperr.Internal("failed to list held sales", err)
	}
	return rows, nil
}

func (s *Service) Hold(ctx context.Context, cashierID int64, in Input) (*HeldSale, error) {
	if len(in.Cart) == 0 {
		return nil, apperr.Validation("nothing to hold")
	}
	discount, err := money.Parse(in.Discount)
	if err != nil {
		discount = decimal.Zero
	}
	total, err := money.Parse(in.Total)
	if err != nil {
		total = decimal.Zero
	}
	saleType := in.SaleType
	if saleType == "" {
		saleType = "retail"
	}
	var label *string
	if in.Label != "" {
		label = &in.Label
	}
	h := HeldSale{
		CashierID: cashierID, Label: label, SaleType: saleType, CustomerID: in.CustomerID,
		Discount: discount, Cart: in.Cart, ItemCount: in.ItemCount, Total: total,
	}
	out, err := s.repo.Create(ctx, h)
	if err != nil {
		return nil, apperr.Internal("failed to hold sale", err)
	}
	return out, nil
}

func (s *Service) Delete(ctx context.Context, id, cashierID int64) error {
	return s.repo.Delete(ctx, id, cashierID)
}

type APIHandler struct{ svc *Service }

func NewAPIHandler(svc *Service) *APIHandler { return &APIHandler{svc: svc} }

func (h *APIHandler) List(c echo.Context) error {
	rows, err := h.svc.List(c.Request().Context(), middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	return response.OK(c, rows)
}

func (h *APIHandler) Hold(c echo.Context) error {
	var in Input
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	out, err := h.svc.Hold(c.Request().Context(), middleware.CurrentUserID(c), in)
	if err != nil {
		return err
	}
	return response.Created(c, out)
}

func (h *APIHandler) Delete(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := h.svc.Delete(c.Request().Context(), id, middleware.CurrentUserID(c)); err != nil {
		return err
	}
	return response.NoContent(c)
}

// RegisterAPI mounts the held-sales endpoints (all authenticated roles — the
// cashier terminal owns this feature).
func RegisterAPI(e *echo.Echo, db *sqlx.DB, cfg *config.Config) {
	api := NewAPIHandler(NewService(db))
	g := e.Group("/api/held-sales", middleware.JWTAuth(cfg.JWTSecret))
	g.GET("", api.List)
	g.POST("", api.Hold)
	g.DELETE("/:id", api.Delete)
}
