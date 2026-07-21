// Package purchasereturns records goods sent back to a supplier (a debit note):
// stock is reduced FEFO, the supplier payable is decreased, and the return is
// logged — all in one transaction.
package purchasereturns

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/config"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/stock"
	"karots-pos/internal/features/suppliers"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

type PurchaseReturn struct {
	ID         int64           `db:"id"          json:"id"`
	SupplierID int64           `db:"supplier_id" json:"supplier_id"`
	Reference  *string         `db:"reference"   json:"reference,omitempty"`
	Total      decimal.Decimal `db:"total"       json:"total"`
	Reason     *string         `db:"reason"      json:"reason,omitempty"`
	CreatedBy  int64           `db:"created_by"  json:"created_by"`
	CreatedAt  time.Time       `db:"created_at"  json:"created_at"`
	// joined
	SupplierName string `db:"supplier_name" json:"supplier_name"`
}

type Item struct {
	ID               int64           `db:"id"                 json:"id"`
	PurchaseReturnID int64           `db:"purchase_return_id" json:"purchase_return_id"`
	ProductID        int64           `db:"product_id"         json:"product_id"`
	Quantity         decimal.Decimal `db:"quantity"           json:"quantity"`
	CostPrice        decimal.Decimal `db:"cost_price"         json:"cost_price"`
	Subtotal         decimal.Decimal `db:"subtotal"           json:"subtotal"`
	ProductName      string          `db:"product_name"       json:"product_name"`
}

type Detail struct {
	Return PurchaseReturn `json:"return"`
	Items  []Item         `json:"items"`
}

type ItemInput struct {
	ProductID int64  `json:"product_id" validate:"required,gt=0"`
	Quantity  string `json:"quantity"   validate:"required"`
	CostPrice string `json:"cost_price" validate:"required"`
}

type CreateInput struct {
	SupplierID int64       `json:"supplier_id" validate:"required,gt=0"`
	Reference  *string     `json:"reference"`
	Reason     *string     `json:"reason"`
	Items      []ItemInput `json:"items" validate:"required,min=1,dive"`
}

type Repository struct{ q appdb.Queryer }

func NewRepository(q appdb.Queryer) *Repository { return &Repository{q: q} }

func (r *Repository) Insert(ctx context.Context, supplierID, userID int64, ref, reason *string, total decimal.Decimal) (int64, error) {
	var id int64
	err := r.q.GetContext(ctx, &id, `
		INSERT INTO purchase_returns (supplier_id, reference, total, reason, created_by)
		VALUES ($1,$2,$3,$4,$5) RETURNING id`, supplierID, ref, total, reason, userID)
	return id, err
}

func (r *Repository) InsertItem(ctx context.Context, prID, productID int64, qty, cost, subtotal decimal.Decimal) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO purchase_return_items (purchase_return_id, product_id, quantity, cost_price, subtotal)
		VALUES ($1,$2,$3,$4,$5)`, prID, productID, qty, cost, subtotal)
	return err
}

// List returns every purchase return, newest first.
//
// There is deliberately no row cap. It used to stop at the 100 most recent,
// which quietly put older returns out of reach with nothing on screen to say
// so; the page pages through the whole history instead. Returns are a
// low-volume table, so reading it whole costs little.
func (r *Repository) List(ctx context.Context) ([]PurchaseReturn, error) {
	var rows []PurchaseReturn
	err := r.q.SelectContext(ctx, &rows, `
		SELECT pr.*, s.name AS supplier_name
		FROM purchase_returns pr
		JOIN suppliers s ON s.id = pr.supplier_id
		ORDER BY pr.created_at DESC`)
	return rows, err
}

func (r *Repository) FindByID(ctx context.Context, id int64) (*PurchaseReturn, error) {
	var pr PurchaseReturn
	err := r.q.GetContext(ctx, &pr, `
		SELECT pr.*, s.name AS supplier_name
		FROM purchase_returns pr
		JOIN suppliers s ON s.id = pr.supplier_id
		WHERE pr.id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &pr, nil
}

func (r *Repository) Items(ctx context.Context, prID int64) ([]Item, error) {
	var rows []Item
	err := r.q.SelectContext(ctx, &rows, `
		SELECT pri.*, p.name AS product_name
		FROM purchase_return_items pri
		JOIN products p ON p.id = pri.product_id
		WHERE pri.purchase_return_id = $1 ORDER BY pri.id`, prID)
	return rows, err
}

type Service struct {
	db   *sqlx.DB
	repo *Repository
}

func NewService(db *sqlx.DB) *Service { return &Service{db: db, repo: NewRepository(db)} }

func (s *Service) List(ctx context.Context) ([]PurchaseReturn, error) {
	rows, err := s.repo.List(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to list purchase returns", err)
	}
	return rows, nil
}

// Get loads a debit note with its lines (for the detail view).
func (s *Service) Get(ctx context.Context, id int64) (*Detail, error) {
	pr, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("purchase return")
		}
		return nil, apperr.Internal("failed to load purchase return", err)
	}
	items, err := s.repo.Items(ctx, id)
	if err != nil {
		return nil, apperr.Internal("failed to load purchase return items", err)
	}
	return &Detail{Return: *pr, Items: items}, nil
}

// Create books a debit note: deplete stock FEFO, reduce supplier payable, log it.
func (s *Service) Create(ctx context.Context, in CreateInput, userID int64) (*Detail, error) {
	var detail *Detail
	err := appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		repo := NewRepository(tx)
		stk := stock.NewRepository(tx)
		sup := suppliers.NewRepository(tx)

		ref := "purchase_return"
		total := decimal.Zero
		type line struct {
			productID int64
			qty, cost, subtotal decimal.Decimal
		}
		lines := make([]line, 0, len(in.Items))
		for _, it := range in.Items {
			qty, err := money.Parse(it.Quantity)
			if err != nil || !qty.IsPositive() {
				return apperr.Validation("quantity must be greater than zero")
			}
			cost, err := money.Parse(it.CostPrice)
			if err != nil || cost.IsNegative() {
				return apperr.Validation("cost price is invalid")
			}
			ok, err := stk.DecrementGuarded(ctx, it.ProductID, qty)
			if err != nil {
				return apperr.Internal("failed to update stock", err)
			}
			if !ok {
				return apperr.Conflict("not enough stock to return for one of the items")
			}
			if _, err := stk.DepleteFEFO(ctx, it.ProductID, qty); err != nil {
				return apperr.Internal("failed to deplete batches", err)
			}
			sub := qty.Mul(cost).Round(2)
			total = total.Add(sub)
			lines = append(lines, line{productID: it.ProductID, qty: qty, cost: cost, subtotal: sub})
		}

		prID, err := repo.Insert(ctx, in.SupplierID, userID, in.Reference, in.Reason, total)
		if err != nil {
			return apperr.Internal("failed to save purchase return", err)
		}
		for _, ln := range lines {
			if err := repo.InsertItem(ctx, prID, ln.productID, ln.qty, ln.cost, ln.subtotal); err != nil {
				return apperr.Internal("failed to save return item", err)
			}
			id := prID
			if err := stk.InsertMovement(ctx, stock.MovementInput{
				ProductID: ln.productID, Type: stock.MovePurchaseReturn, Quantity: ln.qty.Neg(),
				ReferenceID: &id, ReferenceType: &ref, UserID: userID,
			}); err != nil {
				return apperr.Internal("failed to record movement", err)
			}
		}
		// Returning goods reduces what we owe the supplier.
		if total.IsPositive() {
			if err := sup.AddBalance(ctx, in.SupplierID, total.Neg()); err != nil {
				return apperr.Internal("failed to adjust supplier balance", err)
			}
		}

		items, err := repo.Items(ctx, prID)
		if err != nil {
			return apperr.Internal("failed to load return items", err)
		}
		detail = &Detail{Return: PurchaseReturn{ID: prID, SupplierID: in.SupplierID, Total: total}, Items: items}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return detail, nil
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
	d, err := h.svc.Create(c.Request().Context(), in, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	return response.Created(c, d)
}

func RegisterAPI(e *echo.Echo, db *sqlx.DB, cfg *config.Config) {
	api := NewAPIHandler(NewService(db))
	g := e.Group("/api/purchase-returns", middleware.JWTAuth(cfg.JWTSecret), middleware.RequireRole("admin", "manager"))
	g.GET("", api.List)
	g.POST("", api.Create)
}
