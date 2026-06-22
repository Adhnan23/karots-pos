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
	// OrderedQty carries the originally-ordered amount through a receive so the
	// draft's planned qty is preserved alongside the actually-received quantity.
	OrderedQty string `json:"ordered_qty"`
}

type CreateInput struct {
	SupplierID   int64       `json:"supplier_id" validate:"required,gt=0"`
	InvoiceNo    *string     `json:"invoice_no"`
	Discount     string      `json:"discount"`
	PaidAmount   string      `json:"paid_amount"`
	DueDate      string      `json:"due_date"`
	ExpectedDate string      `json:"expected_date"`
	Notes        *string     `json:"notes"`
	Items        []ItemInput `json:"items" validate:"required,min=1,dive"`
}

// parseDate parses an optional YYYY-MM-DD date (nil when blank).
func parseDate(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, apperr.Validation("date must be YYYY-MM-DD")
	}
	return &d, nil
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

		lines, subtotal, err := parseLines(in.Items)
		if err != nil {
			return err
		}

		total := subtotal.Sub(discount)
		if total.IsNegative() {
			return apperr.Validation("discount exceeds purchase subtotal")
		}
		status := receivedStatus(paid, total)

		purchaseID, err := repo.InsertPurchase(ctx, purchaseRow{
			SupplierID: in.SupplierID, InvoiceNo: in.InvoiceNo, Status: status,
			Subtotal: subtotal, Discount: discount, Total: total, Paid: paid,
			DueDate: dueDate, ReceivedBy: userID, Notes: in.Notes,
		})
		if err != nil {
			return mapErr(err)
		}

		if err := applyReceivedLines(ctx, repo, stk, sup, purchaseID, in.SupplierID, lines, total.Sub(paid), userID); err != nil {
			return err
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

// receivedStatus picks paid/partial/received from the amount paid vs the total.
func receivedStatus(paid, total decimal.Decimal) string {
	switch {
	case paid.GreaterThanOrEqual(total):
		return "paid"
	case paid.IsPositive():
		return "partial"
	default:
		return "received"
	}
}

// parseLines validates item inputs into purchase lines and returns their subtotal.
func parseLines(items []ItemInput) ([]PurchaseItem, decimal.Decimal, error) {
	subtotal := decimal.Zero
	lines := make([]PurchaseItem, 0, len(items))
	for _, it := range items {
		qty, err := money.Parse(it.Quantity)
		if err != nil || !qty.IsPositive() {
			return nil, decimal.Zero, apperr.Validation("quantity must be greater than zero")
		}
		cost, err := money.Parse(it.CostPrice)
		if err != nil || cost.IsNegative() {
			return nil, decimal.Zero, apperr.Validation("cost price is invalid")
		}
		selling, err := money.Parse(it.SellingPrice)
		if err != nil || selling.IsNegative() {
			return nil, decimal.Zero, apperr.Validation("selling price is invalid")
		}
		var expiry *time.Time
		if it.ExpiryDate != "" {
			e, err := time.Parse("2006-01-02", it.ExpiryDate)
			if err != nil {
				return nil, decimal.Zero, apperr.Validation("expiry date must be YYYY-MM-DD")
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
	return lines, subtotal, nil
}

// applyReceivedLines inserts purchase item rows and posts their inventory effects
// (stock increment, FEFO batch, movement, expiry flag, pricing refresh), then the
// supplier payable. Shared by instant GRNs (Create) and draft receipts (Receive).
// Each line's OrderedQty is preserved as supplied by the caller.
func applyReceivedLines(ctx context.Context, repo *Repository, stk *stock.Repository, sup *suppliers.Repository, purchaseID, supplierID int64, lines []PurchaseItem, owed decimal.Decimal, userID int64) error {
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
	if owed.IsPositive() {
		if err := sup.AddBalance(ctx, supplierID, owed); err != nil {
			return apperr.Internal("failed to update supplier balance", err)
		}
	}
	return nil
}

func mapErr(err error) error {
	return apperr.Internal("failed to save purchase", err)
}

func notFoundOr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFound("purchase")
	}
	return apperr.Internal("failed to load purchase", err)
}

// insertDraftLines saves planned lines on a draft (ordered_qty = quantity, no
// inventory effect).
func insertDraftLines(ctx context.Context, repo *Repository, purchaseID int64, lines []PurchaseItem) error {
	for _, ln := range lines {
		oq := ln.Quantity
		ln.OrderedQty = &oq
		if err := repo.InsertItem(ctx, purchaseID, ln); err != nil {
			return apperr.Internal("failed to save draft line", err)
		}
	}
	return nil
}

// createDraftTx inserts one draft purchase (status='draft', no stock/payable
// effects) within the given transaction and returns its id.
func createDraftTx(ctx context.Context, tx *sqlx.Tx, in CreateInput, userID int64) (int64, error) {
	repo := NewRepository(tx)
	discount, err := money.Parse(in.Discount)
	if err != nil || discount.IsNegative() {
		discount = decimal.Zero
	}
	dueDate, err := parseDate(in.DueDate)
	if err != nil {
		return 0, err
	}
	expected, err := parseDate(in.ExpectedDate)
	if err != nil {
		return 0, err
	}
	lines, subtotal, err := parseLines(in.Items)
	if err != nil {
		return 0, err
	}
	total := subtotal.Sub(discount)
	if total.IsNegative() {
		return 0, apperr.Validation("discount exceeds purchase subtotal")
	}
	id, err := repo.InsertPurchase(ctx, purchaseRow{
		SupplierID: in.SupplierID, InvoiceNo: in.InvoiceNo, Status: "draft",
		Subtotal: subtotal, Discount: discount, Total: total, Paid: decimal.Zero,
		DueDate: dueDate, ExpectedDate: expected, ReceivedBy: userID, Notes: in.Notes,
	})
	if err != nil {
		return 0, mapErr(err)
	}
	if err := insertDraftLines(ctx, repo, id, lines); err != nil {
		return 0, err
	}
	return id, nil
}

// CreateDraft saves a single Purchase Order as a draft (no inventory effect).
func (s *Service) CreateDraft(ctx context.Context, in CreateInput, userID int64) (*Detail, error) {
	var detail *Detail
	err := appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		id, err := createDraftTx(ctx, tx, in, userID)
		if err != nil {
			return err
		}
		d, err := s.loadDetail(ctx, NewRepository(tx), id)
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

// ReorderLineInput is one product+supplier+qty picked on the low-stock report.
type ReorderLineInput struct {
	ProductID  int64  `json:"product_id"`
	SupplierID int64  `json:"supplier_id"`
	Quantity   string `json:"quantity"`
	CostPrice  string `json:"cost_price"`
}

// ReorderPOInput is the payload the low-stock PO builder posts.
type ReorderPOInput struct {
	Lines []ReorderLineInput `json:"lines"`
}

// CreateDraftsFromReorder groups the picked lines by supplier and creates one
// draft Purchase Order per supplier, all in a single transaction. Returns the
// new draft IDs in supplier-first-seen order (for the print step).
func (s *Service) CreateDraftsFromReorder(ctx context.Context, in ReorderPOInput, userID int64) ([]int64, error) {
	if len(in.Lines) == 0 {
		return nil, apperr.Validation("select at least one product to order")
	}
	order := make([]int64, 0)
	bySup := make(map[int64][]ItemInput)
	for _, l := range in.Lines {
		if l.SupplierID <= 0 {
			return nil, apperr.Validation("each ordered line needs a supplier")
		}
		if l.ProductID <= 0 {
			return nil, apperr.Validation("invalid product on an ordered line")
		}
		if _, ok := bySup[l.SupplierID]; !ok {
			order = append(order, l.SupplierID)
		}
		cost := l.CostPrice
		if cost == "" {
			cost = "0"
		}
		bySup[l.SupplierID] = append(bySup[l.SupplierID], ItemInput{
			ProductID: l.ProductID, Quantity: l.Quantity, CostPrice: cost,
		})
	}
	ids := make([]int64, 0, len(order))
	err := appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		for _, supID := range order {
			id, err := createDraftTx(ctx, tx, CreateInput{
				SupplierID: supID, Discount: "0", PaidAmount: "0", Items: bySup[supID],
			}, userID)
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// UpdateDraft replaces a draft's lines and header. Only drafts are editable.
func (s *Service) UpdateDraft(ctx context.Context, id int64, in CreateInput, userID int64) (*Detail, error) {
	discount, err := money.Parse(in.Discount)
	if err != nil || discount.IsNegative() {
		discount = decimal.Zero
	}
	dueDate, err := parseDate(in.DueDate)
	if err != nil {
		return nil, err
	}
	expected, err := parseDate(in.ExpectedDate)
	if err != nil {
		return nil, err
	}
	var detail *Detail
	err = appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		repo := NewRepository(tx)
		cur, err := repo.FindByID(ctx, id)
		if err != nil {
			return notFoundOr(err)
		}
		if cur.Status != "draft" {
			return apperr.Validation("only draft purchase orders can be edited")
		}
		lines, subtotal, err := parseLines(in.Items)
		if err != nil {
			return err
		}
		total := subtotal.Sub(discount)
		if total.IsNegative() {
			return apperr.Validation("discount exceeds purchase subtotal")
		}
		if err := repo.DeleteItems(ctx, id); err != nil {
			return apperr.Internal("failed to clear draft lines", err)
		}
		if err := insertDraftLines(ctx, repo, id, lines); err != nil {
			return err
		}
		if err := repo.UpdateHeader(ctx, id, purchaseRow{
			InvoiceNo: in.InvoiceNo, Status: "draft", Subtotal: subtotal,
			Discount: discount, Total: total, Paid: decimal.Zero,
			DueDate: dueDate, ExpectedDate: expected, ReceivedBy: userID, Notes: in.Notes,
		}); err != nil {
			return apperr.Internal("failed to update draft", err)
		}
		d, err := s.loadDetail(ctx, repo, id)
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

// ReceiveInput is the payload the receive screen posts for a draft.
type ReceiveInput struct {
	InvoiceNo  *string     `json:"invoice_no"`
	Discount   string      `json:"discount"`
	PaidAmount string      `json:"paid_amount"`
	DueDate    string      `json:"due_date"`
	Notes      *string     `json:"notes"`
	Items      []ItemInput `json:"items" validate:"required,min=1,dive"`
	// KeepRemainder, when true, spins the still-unreceived quantities (ordered −
	// received, where positive) into a new draft PO so the rest stays on order.
	KeepRemainder bool `json:"keep_remainder"`
}

// parseReceiveLines parses the received lines, carrying each line's ordered_qty.
func parseReceiveLines(items []ItemInput) ([]PurchaseItem, decimal.Decimal, error) {
	lines, subtotal, err := parseLines(items)
	if err != nil {
		return nil, decimal.Zero, err
	}
	for i := range lines {
		if items[i].OrderedQty == "" {
			continue
		}
		oq, err := money.Parse(items[i].OrderedQty)
		if err == nil && !oq.IsNegative() {
			lines[i].OrderedQty = &oq
		}
	}
	return lines, subtotal, nil
}

// Receive turns a draft into a received purchase: it rewrites the lines with the
// actually-received quantities (overstock allowed), records invoice/paid/due, and
// posts all the inventory + payable effects. Only drafts can be received.
func (s *Service) Receive(ctx context.Context, id int64, in ReceiveInput, userID int64) (*Detail, error) {
	discount, err := money.Parse(in.Discount)
	if err != nil || discount.IsNegative() {
		return nil, apperr.Validation("discount must be a non-negative amount")
	}
	paid, err := money.Parse(in.PaidAmount)
	if err != nil || paid.IsNegative() {
		return nil, apperr.Validation("paid amount must be a non-negative amount")
	}
	dueDate, err := parseDate(in.DueDate)
	if err != nil {
		return nil, err
	}
	var detail *Detail
	err = appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		repo := NewRepository(tx)
		stk := stock.NewRepository(tx)
		sup := suppliers.NewRepository(tx)
		cur, err := repo.FindByID(ctx, id)
		if err != nil {
			return notFoundOr(err)
		}
		if cur.Status != "draft" {
			return apperr.Validation("this purchase has already been received")
		}
		lines, subtotal, err := parseReceiveLines(in.Items)
		if err != nil {
			return err
		}
		total := subtotal.Sub(discount)
		if total.IsNegative() {
			return apperr.Validation("discount exceeds purchase subtotal")
		}
		notes := in.Notes
		if notes == nil {
			notes = cur.Notes
		}
		// Drop the planned lines, then re-insert as received lines (which posts the
		// stock/batch/movement/pricing effects via applyReceivedLines).
		if err := repo.DeleteItems(ctx, id); err != nil {
			return apperr.Internal("failed to clear draft lines", err)
		}
		if err := repo.UpdateHeader(ctx, id, purchaseRow{
			InvoiceNo: in.InvoiceNo, Status: receivedStatus(paid, total), Subtotal: subtotal,
			Discount: discount, Total: total, Paid: paid, DueDate: dueDate,
			ExpectedDate: cur.ExpectedDate, ReceivedBy: userID, Notes: notes,
		}); err != nil {
			return apperr.Internal("failed to update purchase", err)
		}
		if err := applyReceivedLines(ctx, repo, stk, sup, id, cur.SupplierID, lines, total.Sub(paid), userID); err != nil {
			return err
		}
		// Partial receipt: carry the still-unreceived quantities into a new draft PO.
		if in.KeepRemainder {
			rem := make([]ItemInput, 0, len(lines))
			for _, ln := range lines {
				if ln.OrderedQty == nil {
					continue // an extra item that wasn't ordered — nothing left to order
				}
				short := ln.OrderedQty.Sub(ln.Quantity)
				if short.IsPositive() {
					rem = append(rem, ItemInput{
						ProductID: ln.ProductID, Quantity: short.String(),
						CostPrice: ln.CostPrice.String(), SellingPrice: ln.SellingPrice.String(),
					})
				}
			}
			if len(rem) > 0 {
				if _, err := createDraftTx(ctx, tx, CreateInput{
					SupplierID: cur.SupplierID, Discount: "0", PaidAmount: "0",
					Notes: cur.Notes, Items: rem,
				}, userID); err != nil {
					return err
				}
			}
		}
		d, err := s.loadDetail(ctx, repo, id)
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

// DeleteDraft removes a draft purchase order (only while still a draft).
func (s *Service) DeleteDraft(ctx context.Context, id int64) error {
	ok, err := s.repo.DeleteDraft(ctx, id)
	if err != nil {
		return apperr.Internal("failed to delete draft", err)
	}
	if !ok {
		return apperr.Validation("only draft purchase orders can be deleted")
	}
	return nil
}

// ListDrafts returns open Purchase Orders (drafts), newest first.
func (s *Service) ListDrafts(ctx context.Context) ([]Purchase, error) {
	rows, err := s.repo.ListByStatus(ctx, true, 200)
	if err != nil {
		return nil, apperr.Internal("failed to list draft purchases", err)
	}
	return rows, nil
}

// ListReceived returns received purchase history, newest first.
func (s *Service) ListReceived(ctx context.Context) ([]Purchase, error) {
	rows, err := s.repo.ListByStatus(ctx, false, 200)
	if err != nil {
		return nil, apperr.Internal("failed to list purchases", err)
	}
	return rows, nil
}

// GetMany loads several purchase details (for the multi-supplier PO print).
func (s *Service) GetMany(ctx context.Context, ids []int64) ([]Detail, error) {
	out := make([]Detail, 0, len(ids))
	for _, id := range ids {
		d, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, nil
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
