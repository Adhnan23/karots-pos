// Package sales records POS transactions. A sale is written atomically: stock
// is decremented under a guard (no oversell), movements are audited, and any
// credit portion updates the customer balance — all in one transaction.
package sales

import (
	"context"
	"fmt"
	"time"

	"karots-pos/internal/db"

	"github.com/shopspring/decimal"
)

type Sale struct {
	ID            int64           `db:"id"           json:"id"`
	ReceiptNo     string          `db:"receipt_no"   json:"receipt_no"`
	CustomerID    *int64          `db:"customer_id"  json:"customer_id,omitempty"`
	SaleType      string          `db:"sale_type"    json:"sale_type"`
	Subtotal      decimal.Decimal `db:"subtotal"       json:"subtotal"`
	Discount      decimal.Decimal `db:"discount"       json:"discount"`
	DiscountType  string          `db:"discount_type"  json:"discount_type"`  // bill discount: fixed|percent
	DiscountValue decimal.Decimal `db:"discount_value" json:"discount_value"` // entered value
	Tax           decimal.Decimal `db:"tax"          json:"tax"`
	Total         decimal.Decimal `db:"total"        json:"total"`
	PaidAmount    decimal.Decimal `db:"paid_amount"  json:"paid_amount"`
	ChangeGiven   decimal.Decimal `db:"change_given" json:"change_given"`
	Status        string          `db:"status"       json:"status"`
	CashierID     int64           `db:"cashier_id"   json:"cashier_id"`
	Notes         *string         `db:"notes"        json:"notes,omitempty"`
	CreatedAt     time.Time       `db:"created_at"   json:"created_at"`
	// joined
	CashierName  string  `db:"cashier_name"  json:"cashier_name"`
	CustomerName *string `db:"customer_name" json:"customer_name,omitempty"`
}

type SaleItem struct {
	ID            int64           `db:"id"           json:"id"`
	SaleID        int64           `db:"sale_id"      json:"sale_id"`
	ProductID     int64           `db:"product_id"   json:"product_id"`
	Quantity      decimal.Decimal `db:"quantity"     json:"quantity"`
	UnitPrice     decimal.Decimal `db:"unit_price"   json:"unit_price"`
	CostPrice     decimal.Decimal `db:"cost_price"   json:"cost_price"`
	Discount      decimal.Decimal `db:"discount"       json:"discount"`
	DiscountType  string          `db:"discount_type"  json:"discount_type"` // fixed|percent
	DiscountValue decimal.Decimal `db:"discount_value" json:"discount_value"`
	Subtotal      decimal.Decimal `db:"subtotal"     json:"subtotal"`
	ReturnedQty   decimal.Decimal `db:"returned_qty" json:"returned_qty"`
	Description   *string         `db:"description"  json:"description,omitempty"`
	// joined
	ProductName string `db:"product_name" json:"product_name"`
	UnitAbbr    string `db:"unit_abbr"    json:"unit_abbr"`
	IsService   bool   `db:"is_service"   json:"is_service"` // service line (e.g. recharge) — non-returnable, no stock
}

// ReturnableQty is how much of this line can still be sent back.
func (i SaleItem) ReturnableQty() decimal.Decimal { return i.Quantity.Sub(i.ReturnedQty) }

type Payment struct {
	ID        int64           `db:"id"        json:"id"`
	SaleID    int64           `db:"sale_id"   json:"sale_id"`
	Method    string          `db:"method"    json:"method"`
	Amount    decimal.Decimal `db:"amount"    json:"amount"`
	Reference *string         `db:"reference" json:"reference,omitempty"`
	CreatedAt time.Time       `db:"created_at" json:"created_at"`
}

// Detail bundles a sale with its lines and payments (for receipts and views).
type Detail struct {
	Sale     Sale       `json:"sale"`
	Items    []SaleItem `json:"items"`
	Payments []Payment  `json:"payments"`
}

// --- repository ---

type Repository struct{ q db.Queryer }

func NewRepository(q db.Queryer) *Repository { return &Repository{q: q} }

func (r *Repository) NextReceiptNo(ctx context.Context) (string, error) {
	var n int64
	if err := r.q.GetContext(ctx, &n, `SELECT nextval('sales_receipt_seq')`); err != nil {
		return "", err
	}
	return fmt.Sprintf("S-%05d", n), nil
}

type saleRow struct {
	ReceiptNo     string
	CustomerID    *int64
	SaleType      string
	Subtotal      decimal.Decimal
	Discount      decimal.Decimal
	DiscountType  string
	DiscountValue decimal.Decimal
	Tax           decimal.Decimal
	Total         decimal.Decimal
	PaidAmount    decimal.Decimal
	ChangeGiven   decimal.Decimal
	Status        string
	CashierID     int64
	Notes         *string
}

func (r *Repository) InsertSale(ctx context.Context, s saleRow) (int64, error) {
	var id int64
	err := r.q.GetContext(ctx, &id, `
		INSERT INTO sales
			(receipt_no, customer_id, sale_type, subtotal, discount, discount_type, discount_value,
			 tax, total, paid_amount, change_given, status, cashier_id, notes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING id`,
		s.ReceiptNo, s.CustomerID, s.SaleType, s.Subtotal, s.Discount, s.DiscountType, s.DiscountValue,
		s.Tax, s.Total, s.PaidAmount, s.ChangeGiven, s.Status, s.CashierID, s.Notes)
	return id, err
}

func (r *Repository) InsertItem(ctx context.Context, saleID int64, it SaleItem) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO sale_items (sale_id, product_id, quantity, unit_price, cost_price, discount, discount_type, discount_value, subtotal, description)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		saleID, it.ProductID, it.Quantity, it.UnitPrice, it.CostPrice, it.Discount, it.DiscountType, it.DiscountValue, it.Subtotal, it.Description)
	return err
}

func (r *Repository) InsertPayment(ctx context.Context, saleID int64, method string, amount decimal.Decimal, ref *string) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO payments (sale_id, method, amount, reference)
		VALUES ($1,$2,$3,$4)`, saleID, method, amount, ref)
	return err
}

func (r *Repository) UpdateStatus(ctx context.Context, id int64, status string) error {
	_, err := r.q.ExecContext(ctx, `UPDATE sales SET status = $1 WHERE id = $2`, status, id)
	return err
}

// MarkItemFullyReturned sets returned_qty = quantity for a line.
func (r *Repository) MarkItemFullyReturned(ctx context.Context, itemID int64) error {
	_, err := r.q.ExecContext(ctx,
		`UPDATE sale_items SET returned_qty = quantity WHERE id = $1`, itemID)
	return err
}

// AddReturnedQty increments a line's returned quantity (guarded so it never
// exceeds the quantity sold). Returns false if the requested qty isn't available.
func (r *Repository) AddReturnedQty(ctx context.Context, itemID int64, qty decimal.Decimal) (bool, error) {
	res, err := r.q.ExecContext(ctx,
		`UPDATE sale_items SET returned_qty = returned_qty + $1
		 WHERE id = $2 AND returned_qty + $1 <= quantity`, qty, itemID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// FindItem loads one sale line (to value a partial return).
func (r *Repository) FindItem(ctx context.Context, saleID, itemID int64) (*SaleItem, error) {
	var it SaleItem
	err := r.q.GetContext(ctx, &it, `
		SELECT si.*, p.name AS product_name, u.abbreviation AS unit_abbr, p.is_service AS is_service
		FROM sale_items si
		JOIN products p ON p.id = si.product_id
		JOIN units u    ON u.id = p.unit_id
		WHERE si.id = $1 AND si.sale_id = $2`, itemID, saleID)
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// OutstandingItems counts sale lines that still have un-returned quantity, to
// decide whether a sale becomes 'partially_returned' or fully 'returned'.
func (r *Repository) OutstandingItems(ctx context.Context, saleID int64) (int, error) {
	var n int
	err := r.q.GetContext(ctx, &n,
		`SELECT COUNT(*) FROM sale_items WHERE sale_id = $1 AND returned_qty < quantity`, saleID)
	return n, err
}

func (r *Repository) InsertSaleReturn(ctx context.Context, saleID, userID int64, refund, creditReduction decimal.Decimal, reason *string) (int64, error) {
	var id int64
	err := r.q.GetContext(ctx, &id, `
		INSERT INTO sale_returns (sale_id, refund_amount, credit_reduction, reason, created_by)
		VALUES ($1,$2,$3,$4,$5) RETURNING id`, saleID, refund, creditReduction, reason, userID)
	return id, err
}

func (r *Repository) SetReturnTotals(ctx context.Context, returnID int64, refund, creditReduction decimal.Decimal) error {
	_, err := r.q.ExecContext(ctx,
		`UPDATE sale_returns SET refund_amount = $1, credit_reduction = $2 WHERE id = $3`,
		refund, creditReduction, returnID)
	return err
}

func (r *Repository) InsertSaleReturnItem(ctx context.Context, returnID, saleItemID, productID int64, qty, refund decimal.Decimal) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO sale_return_items (return_id, sale_item_id, product_id, quantity, refund_amount)
		VALUES ($1,$2,$3,$4,$5)`, returnID, saleItemID, productID, qty, refund)
	return err
}

// ReturnReceipt is the data for a printed refund slip: the most recent return on
// a sale, with its returned lines and refund/credit split.
type ReturnReceipt struct {
	ReturnID        int64           `db:"id"`
	ReceiptNo       string          `db:"receipt_no"`
	CreatedAt       time.Time       `db:"created_at"`
	Reason          *string         `db:"reason"`
	Refund          decimal.Decimal `db:"refund_amount"`
	CreditReduction decimal.Decimal `db:"credit_reduction"`
	Items           []ReturnReceiptItem
}

type ReturnReceiptItem struct {
	ProductName string          `db:"product_name"`
	UnitAbbr    string          `db:"unit_abbr"`
	Quantity    decimal.Decimal `db:"quantity"`
	Refund      decimal.Decimal `db:"refund_amount"`
}

// LatestReturn loads the most recent return on a sale plus its line items, for
// printing a refund slip right after a return is processed.
func (r *Repository) LatestReturn(ctx context.Context, saleID int64) (*ReturnReceipt, error) {
	var rr ReturnReceipt
	if err := r.q.GetContext(ctx, &rr, `
		SELECT sr.id, s.receipt_no, sr.created_at, sr.reason, sr.refund_amount, sr.credit_reduction
		FROM sale_returns sr JOIN sales s ON s.id = sr.sale_id
		WHERE sr.sale_id = $1 ORDER BY sr.id DESC LIMIT 1`, saleID); err != nil {
		return nil, err
	}
	if err := r.q.SelectContext(ctx, &rr.Items, `
		SELECT p.name AS product_name, u.abbreviation AS unit_abbr, sri.quantity, sri.refund_amount
		FROM sale_return_items sri
		JOIN products p ON p.id = sri.product_id
		JOIN units u ON u.id = p.unit_id
		WHERE sri.return_id = $1
		ORDER BY sri.id`, rr.ReturnID); err != nil {
		return nil, err
	}
	return &rr, nil
}

func (r *Repository) FindByID(ctx context.Context, id int64) (*Sale, error) {
	var s Sale
	err := r.q.GetContext(ctx, &s, `
		SELECT s.*, u.name AS cashier_name, c.name AS customer_name
		FROM sales s
		JOIN users u ON u.id = s.cashier_id
		LEFT JOIN customers c ON c.id = s.customer_id
		WHERE s.id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *Repository) Items(ctx context.Context, saleID int64) ([]SaleItem, error) {
	var rows []SaleItem
	err := r.q.SelectContext(ctx, &rows, `
		SELECT si.*, p.name AS product_name, u.abbreviation AS unit_abbr, p.is_service AS is_service
		FROM sale_items si
		JOIN products p ON p.id = si.product_id
		JOIN units u    ON u.id = p.unit_id
		WHERE si.sale_id = $1 ORDER BY si.id`, saleID)
	return rows, err
}

func (r *Repository) Payments(ctx context.Context, saleID int64) ([]Payment, error) {
	var rows []Payment
	err := r.q.SelectContext(ctx, &rows,
		`SELECT * FROM payments WHERE sale_id = $1 ORDER BY id`, saleID)
	return rows, err
}

// ListFilter narrows the sales list for the admin panel.
type ListFilter struct {
	From      *time.Time
	To        *time.Time
	CashierID *int64
	Status    string
	Receipt   string // receipt-number substring match (blank = any)
	Query     string // matches receipt no / customer name / customer phone (blank = any)
	Method    string // payment method on the sale (blank = any)
	Limit     int
	Offset    int
}

func (r *Repository) List(ctx context.Context, f ListFilter) ([]Sale, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	var status *string
	if f.Status != "" {
		status = &f.Status
	}
	var receipt *string
	if f.Receipt != "" {
		receipt = &f.Receipt
	}
	var method *string
	if f.Method != "" {
		method = &f.Method
	}
	var query *string
	if f.Query != "" {
		query = &f.Query
	}
	var rows []Sale
	err := r.q.SelectContext(ctx, &rows, `
		SELECT s.*, u.name AS cashier_name, c.name AS customer_name
		FROM sales s
		JOIN users u ON u.id = s.cashier_id
		LEFT JOIN customers c ON c.id = s.customer_id
		WHERE ($1::timestamptz IS NULL OR s.created_at >= $1)
		  AND ($2::timestamptz IS NULL OR s.created_at <  $2)
		  AND ($3::bigint IS NULL OR s.cashier_id = $3)
		  AND ($4::text IS NULL OR s.status = $4::sale_status)
		  AND ($5::text IS NULL OR s.receipt_no ILIKE '%' || $5 || '%')
		  AND ($8::text IS NULL OR EXISTS (
		      SELECT 1 FROM payments p WHERE p.sale_id = s.id AND p.method = $8::payment_method))
		  AND ($9::text IS NULL OR
		       s.receipt_no ILIKE '%' || $9 || '%' OR
		       c.name       ILIKE '%' || $9 || '%' OR
		       c.phone      ILIKE '%' || $9 || '%')
		ORDER BY s.created_at DESC, s.id DESC
		LIMIT $6 OFFSET $7`, f.From, f.To, f.CashierID, status, receipt, f.Limit, f.Offset, method, query)
	return rows, err
}
