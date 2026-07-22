// Package purchases records Goods Received Notes (GRN) from suppliers. Receiving
// a purchase increments stock, audits movements, refreshes product cost/price,
// and updates the supplier payable — all in one transaction.
package purchases

import (
	"context"
	"time"

	"karots-pos/internal/db"

	"github.com/shopspring/decimal"
)

type Purchase struct {
	ID         int64           `db:"id"          json:"id"`
	SupplierID int64           `db:"supplier_id" json:"supplier_id"`
	InvoiceNo  *string         `db:"invoice_no"  json:"invoice_no,omitempty"`
	Status     string          `db:"status"      json:"status"`
	Subtotal   decimal.Decimal `db:"subtotal"    json:"subtotal"`
	Discount   decimal.Decimal `db:"discount"    json:"discount"`
	Total      decimal.Decimal `db:"total"       json:"total"`
	PaidAmount decimal.Decimal `db:"paid_amount" json:"paid_amount"`
	// CreditedAmount is value returned to the supplier against this invoice. It
	// reduces what is owed without claiming any money changed hands.
	CreditedAmount decimal.Decimal `db:"credited_amount" json:"credited_amount"`
	DueDate    *time.Time      `db:"due_date"    json:"due_date,omitempty"`
	ExpectedDate *time.Time    `db:"expected_date" json:"expected_date,omitempty"`
	ReceivedBy *int64          `db:"received_by" json:"received_by,omitempty"`
	Notes      *string         `db:"notes"       json:"notes,omitempty"`
	CreatedAt  time.Time       `db:"created_at"  json:"created_at"`
	// joined
	SupplierName   string  `db:"supplier_name"   json:"supplier_name"`
	ReceivedByName *string `db:"received_by_name" json:"received_by_name,omitempty"`
}

// Balance is what is still owed on this purchase: the total less what was paid
// AND less anything credited back by returning goods. Returned stock is not a
// debt, so it must come off here — otherwise the invoice stays on the payment
// queue and the shop pays for goods it already sent back.
func (p Purchase) Balance() decimal.Decimal {
	return p.Total.Sub(p.PaidAmount).Sub(p.CreditedAmount)
}

type PurchaseItem struct {
	ID           int64            `db:"id"            json:"id"`
	PurchaseID   int64            `db:"purchase_id"   json:"purchase_id"`
	ProductID    int64            `db:"product_id"    json:"product_id"`
	Quantity     decimal.Decimal  `db:"quantity"      json:"quantity"`
	OrderedQty   *decimal.Decimal `db:"ordered_qty"   json:"ordered_qty,omitempty"`
	CostPrice    decimal.Decimal  `db:"cost_price"    json:"cost_price"`
	SellingPrice decimal.Decimal  `db:"selling_price" json:"selling_price"`
	ExpiryDate   *time.Time       `db:"expiry_date"   json:"expiry_date,omitempty"`
	Subtotal     decimal.Decimal  `db:"subtotal"      json:"subtotal"`
	ProductName  string           `db:"product_name"  json:"product_name"`
}

type Detail struct {
	Purchase Purchase       `json:"purchase"`
	Items    []PurchaseItem `json:"items"`
}

type Repository struct{ q db.Queryer }

func NewRepository(q db.Queryer) *Repository { return &Repository{q: q} }

type purchaseRow struct {
	SupplierID   int64
	InvoiceNo    *string
	Status       string
	Subtotal     decimal.Decimal
	Discount     decimal.Decimal
	Total        decimal.Decimal
	Paid         decimal.Decimal
	DueDate      *time.Time
	ExpectedDate *time.Time
	ReceivedBy   int64
	Notes        *string
}

func (r *Repository) InsertPurchase(ctx context.Context, p purchaseRow) (int64, error) {
	var id int64
	err := r.q.GetContext(ctx, &id, `
		INSERT INTO purchases
			(supplier_id, invoice_no, status, subtotal, discount, total, paid_amount, due_date, expected_date, received_by, notes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
		p.SupplierID, p.InvoiceNo, p.Status, p.Subtotal, p.Discount, p.Total, p.Paid, p.DueDate, p.ExpectedDate, p.ReceivedBy, p.Notes)
	return id, err
}

func (r *Repository) InsertItem(ctx context.Context, purchaseID int64, it PurchaseItem) error {
	_, err := r.InsertItemReturningID(ctx, purchaseID, it)
	return err
}

// InsertItemReturningID inserts a purchase line and returns its id so a matching
// stock batch can reference it.
func (r *Repository) InsertItemReturningID(ctx context.Context, purchaseID int64, it PurchaseItem) (int64, error) {
	var id int64
	err := r.q.GetContext(ctx, &id, `
		INSERT INTO purchase_items (purchase_id, product_id, quantity, ordered_qty, cost_price, selling_price, expiry_date, subtotal)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`,
		purchaseID, it.ProductID, it.Quantity, it.OrderedQty, it.CostPrice, it.SellingPrice, it.ExpiryDate, it.Subtotal)
	return id, err
}

// MarkHasExpiry flags a product as expiry-tracked once a dated batch arrives.
func (r *Repository) MarkHasExpiry(ctx context.Context, productID int64) error {
	_, err := r.q.ExecContext(ctx, `UPDATE products SET has_expiry = true WHERE id = $1`, productID)
	return err
}

// RefreshProductPricing updates a product's cost (always) and selling price
// (only when a positive new price was supplied on the GRN line).
func (r *Repository) RefreshProductPricing(ctx context.Context, productID int64, cost, selling decimal.Decimal) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE products
		SET cost_price = $1,
		    selling_price = CASE WHEN $2 > 0 THEN $2 ELSE selling_price END
		WHERE id = $3`, cost, selling, productID)
	return err
}

func (r *Repository) FindByID(ctx context.Context, id int64) (*Purchase, error) {
	var p Purchase
	err := r.q.GetContext(ctx, &p, `
		SELECT pu.*, s.name AS supplier_name, u.name AS received_by_name
		FROM purchases pu
		JOIN suppliers s ON s.id = pu.supplier_id
		LEFT JOIN users u ON u.id = pu.received_by
		WHERE pu.id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *Repository) Items(ctx context.Context, purchaseID int64) ([]PurchaseItem, error) {
	var rows []PurchaseItem
	err := r.q.SelectContext(ctx, &rows, `
		SELECT pi.*, p.name AS product_name
		FROM purchase_items pi
		JOIN products p ON p.id = pi.product_id
		WHERE pi.purchase_id = $1 ORDER BY pi.id`, purchaseID)
	return rows, err
}

// OpenBySupplier lists a supplier's purchases that still owe money (total >
// paid_amount), oldest first — the queue for allocating a payment.
func (r *Repository) OpenBySupplier(ctx context.Context, supplierID int64) ([]Purchase, error) {
	var rows []Purchase
	err := r.q.SelectContext(ctx, &rows, `
		SELECT pu.*, s.name AS supplier_name, u.name AS received_by_name
		FROM purchases pu
		JOIN suppliers s ON s.id = pu.supplier_id
		LEFT JOIN users u ON u.id = pu.received_by
		WHERE pu.supplier_id = $1 AND pu.status <> 'draft'
		  AND pu.total > pu.paid_amount + pu.credited_amount
		ORDER BY pu.created_at`, supplierID)
	return rows, err
}

// ApplyPayment adds to a purchase's paid_amount and recomputes its status
// (paid when fully settled, otherwise partial). Guarded so it never pays a
// purchase beyond its total. Returns false if the row couldn't be advanced.
func (r *Repository) ApplyPayment(ctx context.Context, purchaseID int64, amount decimal.Decimal) (bool, error) {
	// Credits count against the ceiling: once goods are returned, the money owed
	// on that invoice drops, and paying the old total would be paying for stock
	// that went back to the supplier.
	res, err := r.q.ExecContext(ctx, `
		UPDATE purchases
		SET paid_amount = paid_amount + $1,
		    status = CASE WHEN paid_amount + $1 >= total - credited_amount THEN 'paid'::purchase_status
		                  ELSE 'partial'::purchase_status END
		WHERE id = $2 AND paid_amount + $1 <= total - credited_amount`, amount, purchaseID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ApplyCredit books returned value against one invoice and returns how much it
// could actually absorb. It is capped at what that invoice still owes, so a
// credit can never drive an invoice negative — the caller carries any excess as a
// supplier credit instead. FOR UPDATE keeps a concurrent payment from spending
// the same headroom.
func (r *Repository) ApplyCredit(ctx context.Context, purchaseID int64, amount decimal.Decimal) (decimal.Decimal, error) {
	var applied decimal.Decimal
	err := r.q.GetContext(ctx, &applied, `
		WITH target AS (
			SELECT id, LEAST($1, GREATEST(total - paid_amount - credited_amount, 0)) AS apply
			FROM purchases WHERE id = $2 FOR UPDATE
		)
		UPDATE purchases p
		SET credited_amount = p.credited_amount + target.apply
		FROM target
		WHERE p.id = target.id
		RETURNING target.apply`, amount, purchaseID)
	return applied, err
}

func (r *Repository) List(ctx context.Context, limit int) ([]Purchase, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []Purchase
	err := r.q.SelectContext(ctx, &rows, `
		SELECT pu.*, s.name AS supplier_name, u.name AS received_by_name
		FROM purchases pu
		JOIN suppliers s ON s.id = pu.supplier_id
		LEFT JOIN users u ON u.id = pu.received_by
		ORDER BY pu.created_at DESC LIMIT $1`, limit)
	return rows, err
}

// ListBetween lists the purchases actually received in a date window, oldest
// last. Drafts are excluded: an order that has not arrived is not a purchase,
// and counting one inflates both what was bought and what is owed.
//
// Unlike List it has no row cap. The Purchases report filters by date, and a cap
// applied before that filter quietly empties any range older than the most
// recent rows — the report would report less than the truth without saying so.
func (r *Repository) ListBetween(ctx context.Context, from, to time.Time) ([]Purchase, error) {
	var rows []Purchase
	err := r.q.SelectContext(ctx, &rows, `
		SELECT pu.*, s.name AS supplier_name, u.name AS received_by_name
		FROM purchases pu
		JOIN suppliers s ON s.id = pu.supplier_id
		LEFT JOIN users u ON u.id = pu.received_by
		WHERE pu.status <> 'draft' AND pu.created_at >= $1 AND pu.created_at < $2
		ORDER BY pu.created_at DESC`, from, to)
	return rows, err
}

// ListByStatus lists draft purchases (Purchase Orders, draft=true) or received
// history (draft=false), newest first.
func (r *Repository) ListByStatus(ctx context.Context, draft bool, limit int) ([]Purchase, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	cond := "pu.status <> 'draft'"
	if draft {
		cond = "pu.status = 'draft'"
	}
	var rows []Purchase
	err := r.q.SelectContext(ctx, &rows, `
		SELECT pu.*, s.name AS supplier_name, u.name AS received_by_name
		FROM purchases pu
		JOIN suppliers s ON s.id = pu.supplier_id
		LEFT JOIN users u ON u.id = pu.received_by
		WHERE `+cond+`
		ORDER BY pu.created_at DESC LIMIT $1`, limit)
	return rows, err
}

// UpdateHeader rewrites a purchase's header fields. Used when receiving a draft
// (sets invoice/paid/due and flips status) and when editing a draft's totals.
func (r *Repository) UpdateHeader(ctx context.Context, id int64, h purchaseRow) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE purchases
		SET invoice_no = $2, status = $3, subtotal = $4, discount = $5,
		    total = $6, paid_amount = $7, due_date = $8, expected_date = $9, received_by = $10, notes = $11
		WHERE id = $1`,
		id, h.InvoiceNo, h.Status, h.Subtotal, h.Discount, h.Total, h.Paid, h.DueDate, h.ExpectedDate, h.ReceivedBy, h.Notes)
	return err
}

// DeleteItems clears a purchase's lines (so a draft can be re-saved/received with
// an edited set). Caller must run inside a transaction.
func (r *Repository) DeleteItems(ctx context.Context, purchaseID int64) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM purchase_items WHERE purchase_id = $1`, purchaseID)
	return err
}

// DeleteDraft removes a draft purchase (only while still a draft — received
// purchases are immutable history). Returns false if nothing was deleted.
func (r *Repository) DeleteDraft(ctx context.Context, id int64) (bool, error) {
	res, err := r.q.ExecContext(ctx, `DELETE FROM purchases WHERE id = $1 AND status = 'draft'`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
