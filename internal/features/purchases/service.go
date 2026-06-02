package purchases

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
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

type Service struct {
	db   *sqlx.DB
	repo *Repository
}

func NewService(db *sqlx.DB) *Service { return &Service{db: db, repo: NewRepository(db)} }

type ItemInput struct {
	ProductID    int64  `json:"product_id"    validate:"required,gt=0"`
	Quantity     string `json:"quantity"      validate:"required"`
	CostPrice    string `json:"cost_price"    validate:"required"`
	SellingPrice string `json:"selling_price"`
	ExpiryDate   string `json:"expiry_date"`
}

type CreateInput struct {
	SupplierID int64       `json:"supplier_id" validate:"required,gt=0"`
	InvoiceNo  *string     `json:"invoice_no"`
	Discount   string      `json:"discount"`
	PaidAmount string      `json:"paid_amount"`
	DueDate    string      `json:"due_date"`
	Notes      *string     `json:"notes"`
	Items      []ItemInput `json:"items" validate:"required,min=1,dive"`
}

func (s *Service) Create(ctx context.Context, in CreateInput, userID int64) (*Detail, error) {
	discount, err := money.Parse(in.Discount)
	if err != nil || discount.IsNegative() {
		return nil, apperr.Validation("discount must be a non-negative amount")
	}
	paid, err := money.Parse(in.PaidAmount)
	if err != nil || paid.IsNegative() {
		return nil, apperr.Validation("paid amount must be a non-negative amount")
	}
	var dueDate *time.Time
	if in.DueDate != "" {
		d, err := time.Parse("2006-01-02", in.DueDate)
		if err != nil {
			return nil, apperr.Validation("due date must be YYYY-MM-DD")
		}
		dueDate = &d
	}

	var detail *Detail
	err = appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		repo := NewRepository(tx)
		stk := stock.NewRepository(tx)
		sup := suppliers.NewRepository(tx)

		subtotal := decimal.Zero
		lines := make([]PurchaseItem, 0, len(in.Items))
		for _, it := range in.Items {
			qty, err := money.Parse(it.Quantity)
			if err != nil || !qty.IsPositive() {
				return apperr.Validation("quantity must be greater than zero")
			}
			cost, err := money.Parse(it.CostPrice)
			if err != nil || cost.IsNegative() {
				return apperr.Validation("cost price is invalid")
			}
			selling, err := money.Parse(it.SellingPrice)
			if err != nil || selling.IsNegative() {
				return apperr.Validation("selling price is invalid")
			}
			var expiry *time.Time
			if it.ExpiryDate != "" {
				e, err := time.Parse("2006-01-02", it.ExpiryDate)
				if err != nil {
					return apperr.Validation("expiry date must be YYYY-MM-DD")
				}
				expiry = &e
			}
			lineSub := qty.Mul(cost).Round(2)
			subtotal = subtotal.Add(lineSub)
			lines = append(lines, PurchaseItem{
				ProductID: it.ProductID, Quantity: qty, CostPrice: cost,
				SellingPrice: selling, ExpiryDate: expiry, Subtotal: lineSub,
			})
		}

		total := subtotal.Sub(discount)
		if total.IsNegative() {
			return apperr.Validation("discount exceeds purchase subtotal")
		}
		status := "received"
		if paid.GreaterThanOrEqual(total) {
			status = "paid"
		} else if paid.IsPositive() {
			status = "partial"
		}

		purchaseID, err := repo.InsertPurchase(ctx, purchaseRow{
			SupplierID: in.SupplierID, InvoiceNo: in.InvoiceNo, Status: status,
			Subtotal: subtotal, Discount: discount, Total: total, Paid: paid,
			DueDate: dueDate, ReceivedBy: userID, Notes: in.Notes,
		})
		if err != nil {
			return mapErr(err)
		}

		for _, ln := range lines {
			ln.PurchaseID = purchaseID
			itemID, err := repo.InsertItemReturningID(ctx, purchaseID, ln)
			if err != nil {
				return apperr.Internal("failed to save purchase item", err)
			}
			if err := stk.Increment(ctx, ln.ProductID, ln.Quantity); err != nil {
				return apperr.Internal("failed to update stock", err)
			}
			// Create the batch (carries expiry + cost) for FEFO depletion later.
			if _, err := stk.InsertBatch(ctx, stock.NewBatch{
				ProductID: ln.ProductID, PurchaseItemID: &itemID, ExpiryDate: ln.ExpiryDate,
				Quantity: ln.Quantity, CostPrice: ln.CostPrice, Source: "purchase",
			}); err != nil {
				return apperr.Internal("failed to create stock batch", err)
			}
			if ln.ExpiryDate != nil {
				if err := repo.MarkHasExpiry(ctx, ln.ProductID); err != nil {
					return apperr.Internal("failed to flag product expiry", err)
				}
			}
			ref := "purchase"
			pid := purchaseID
			if err := stk.InsertMovement(ctx, stock.MovementInput{
				ProductID: ln.ProductID, Type: stock.MovePurchase, Quantity: ln.Quantity,
				ReferenceID: &pid, ReferenceType: &ref, UserID: userID,
			}); err != nil {
				return apperr.Internal("failed to record stock movement", err)
			}
			if err := repo.RefreshProductPricing(ctx, ln.ProductID, ln.CostPrice, ln.SellingPrice); err != nil {
				return apperr.Internal("failed to refresh product pricing", err)
			}
		}

		if owed := total.Sub(paid); owed.IsPositive() {
			if err := sup.AddBalance(ctx, in.SupplierID, owed); err != nil {
				return apperr.Internal("failed to update supplier balance", err)
			}
		}

		d, err := s.loadDetail(ctx, repo, purchaseID)
		if err != nil {
			return err
		}
		detail = d
		return nil
	})
	if err != nil {
		return nil, err
	}
	return detail, nil
}

func mapErr(err error) error {
	return apperr.Internal("failed to save purchase", err)
}

func (s *Service) Get(ctx context.Context, id int64) (*Detail, error) {
	return s.loadDetail(ctx, s.repo, id)
}

func (s *Service) loadDetail(ctx context.Context, repo *Repository, id int64) (*Detail, error) {
	p, err := repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("purchase")
		}
		return nil, apperr.Internal("failed to load purchase", err)
	}
	items, err := repo.Items(ctx, id)
	if err != nil {
		return nil, apperr.Internal("failed to load purchase items", err)
	}
	return &Detail{Purchase: *p, Items: items}, nil
}

func (s *Service) List(ctx context.Context) ([]Purchase, error) {
	rows, err := s.repo.List(ctx, 100)
	if err != nil {
		return nil, apperr.Internal("failed to list purchases", err)
	}
	return rows, nil
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

func (h *APIHandler) Get(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	d, err := h.svc.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.OK(c, d)
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
	g := e.Group("/api/purchases", middleware.JWTAuth(cfg.JWTSecret), middleware.RequireRole("admin", "manager"))
	g.GET("", api.List)
	g.GET("/:id", api.Get)
	g.POST("", api.Create)
}
