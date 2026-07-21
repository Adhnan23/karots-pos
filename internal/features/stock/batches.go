package stock

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// Batch is one received lot of a product, carrying its own expiry and cost.
// On-hand quantity for a product is SUM(qty_remaining) across its batches, which
// is mirrored into stock.quantity (the fast, atomic oversell guard).
type Batch struct {
	ID             int64           `db:"id"               json:"id"`
	ProductID      int64           `db:"product_id"       json:"product_id"`
	PurchaseItemID *int64          `db:"purchase_item_id" json:"purchase_item_id,omitempty"`
	BatchNo        *string         `db:"batch_no"         json:"batch_no,omitempty"`
	ExpiryDate     *time.Time      `db:"expiry_date"      json:"expiry_date,omitempty"`
	QtyReceived    decimal.Decimal `db:"qty_received"     json:"qty_received"`
	QtyRemaining   decimal.Decimal `db:"qty_remaining"    json:"qty_remaining"`
	CostPrice      decimal.Decimal `db:"cost_price"       json:"cost_price"`
	Source         string          `db:"source"           json:"source"`
	CreatedAt      time.Time       `db:"created_at"       json:"created_at"`
	// joined
	ProductName string `db:"product_name" json:"product_name"`
	UnitAbbr    string `db:"unit_abbr"    json:"unit_abbr"`
}

// NewBatch is the data needed to add a lot to inventory.
type NewBatch struct {
	ProductID      int64
	PurchaseItemID *int64
	BatchNo        *string
	ExpiryDate     *time.Time
	Quantity       decimal.Decimal
	CostPrice      decimal.Decimal
	Source         string // purchase|opening|adjust|return|conversion
}

// InsertBatch adds a new lot. Callers must also bump stock.quantity (use the
// Increment helper) so the cached aggregate stays in sync within the same tx.
func (r *Repository) InsertBatch(ctx context.Context, b NewBatch) (int64, error) {
	if b.Source == "" {
		b.Source = "purchase"
	}
	var id int64
	// A blank cost box at intake would otherwise freeze this lot at zero for
	// good — cost is never revisited once the batch exists — so inherit the
	// product's current cost. Genuinely free stock stays free, because a free
	// product's own cost is zero too.
	err := r.q.GetContext(ctx, &id, `
		INSERT INTO stock_batches
			(product_id, purchase_item_id, batch_no, expiry_date, qty_received, qty_remaining, cost_price, source)
		VALUES ($1,$2,$3,$4,$5,$5,
			CASE WHEN $6::numeric = 0
			     THEN COALESCE((SELECT cost_price FROM products WHERE id = $1), 0)
			     ELSE $6::numeric END,
			$7) RETURNING id`,
		b.ProductID, b.PurchaseItemID, b.BatchNo, b.ExpiryDate, b.Quantity, b.CostPrice, b.Source)
	return id, err
}

// consumedLot is one batch's contribution to a depletion: how much was taken
// and what that batch says it cost.
type consumedLot struct {
	Qty  decimal.Decimal
	Cost decimal.Decimal
}

// costOfConsumed returns the weighted-average cost per unit of a depletion.
//
// A lot whose batch cost is zero is charged at productCost instead. Cost is
// frozen into a batch when the batch is created, so stock added before its cost
// was entered — a blank cost box at intake — is stuck at zero even after the
// product's cost is corrected. Left alone it books Rs 0 of COGS and reports the
// whole sale as profit, which is worse than being slightly stale.
//
// The rescue is per lot, not on the final average: one properly-costed batch
// would lift the average above zero and hide the free one. A product whose own
// cost is also zero stays at zero — that one really is free.
func costOfConsumed(lots []consumedLot, qty, productCost decimal.Decimal) decimal.Decimal {
	if !qty.IsPositive() {
		return decimal.Zero
	}
	total := decimal.Zero
	for _, l := range lots {
		cost := l.Cost
		if cost.IsZero() {
			cost = productCost
		}
		total = total.Add(l.Qty.Mul(cost))
	}
	return total.Div(qty).Round(2)
}

// DepleteFEFO consumes qty from a product's batches, earliest-expiry-first, and
// returns the weighted-average cost of the consumed units (for COGS snapshots).
// It assumes the caller already passed the atomic stock.quantity guard, so the
// batches should always cover qty; if a rounding shortfall remains it is charged
// against the last touched batch's cost (or zero) rather than failing.
//
// Runs inside the caller's transaction (r.q is the *sqlx.Tx).
func (r *Repository) DepleteFEFO(ctx context.Context, productID int64, qty decimal.Decimal) (decimal.Decimal, error) {
	if !qty.IsPositive() {
		return decimal.Zero, nil
	}
	type batchRow struct {
		ID        int64           `db:"id"`
		Remaining decimal.Decimal `db:"qty_remaining"`
		Cost      decimal.Decimal `db:"cost_price"`
	}
	var batches []batchRow
	// FOR UPDATE locks the rows we are about to consume for the tx's lifetime.
	if err := r.q.SelectContext(ctx, &batches, `
		SELECT id, qty_remaining, cost_price
		FROM stock_batches
		WHERE product_id = $1 AND qty_remaining > 0
		ORDER BY expiry_date NULLS LAST, id
		FOR UPDATE`, productID); err != nil {
		return decimal.Zero, err
	}

	remaining := qty
	lots := make([]consumedLot, 0, len(batches))
	zeroCost := false
	for _, b := range batches {
		if !remaining.IsPositive() {
			break
		}
		take := decimal.Min(b.Remaining, remaining)
		if _, err := r.q.ExecContext(ctx,
			`UPDATE stock_batches SET qty_remaining = qty_remaining - $1 WHERE id = $2`,
			take, b.ID); err != nil {
			return decimal.Zero, err
		}
		lots = append(lots, consumedLot{Qty: take, Cost: b.Cost})
		zeroCost = zeroCost || b.Cost.IsZero()
		remaining = remaining.Sub(take)
	}

	// Only pay for the extra lookup when something actually needs rescuing.
	fallback := decimal.Zero
	if zeroCost {
		var err error
		if fallback, err = r.productCost(ctx, productID); err != nil {
			return decimal.Zero, err
		}
	}
	return costOfConsumed(lots, qty, fallback), nil
}

// productCost reads a product's current cost price (used to value adjustment
// batches when stock is increased outside a purchase).
func (r *Repository) productCost(ctx context.Context, productID int64) (decimal.Decimal, error) {
	var c decimal.Decimal
	err := r.q.GetContext(ctx, &c, `SELECT cost_price FROM products WHERE id = $1`, productID)
	return c, err
}

// ProductCost is the exported view of productCost, for callers outside this
// package (e.g. the recovery service valuing a restocked replacement batch).
func (r *Repository) ProductCost(ctx context.Context, productID int64) (decimal.Decimal, error) {
	return r.productCost(ctx, productID)
}

// ListBatches returns the live (qty_remaining>0) lots for a product, FEFO order.
func (r *Repository) ListBatches(ctx context.Context, productID int64) ([]Batch, error) {
	var rows []Batch
	err := r.q.SelectContext(ctx, &rows, `
		SELECT b.*, p.name AS product_name, u.abbreviation AS unit_abbr
		FROM stock_batches b
		JOIN products p ON p.id = b.product_id
		JOIN units u    ON u.id = p.unit_id
		WHERE b.product_id = $1 AND b.qty_remaining > 0
		ORDER BY b.expiry_date NULLS LAST, b.id`, productID)
	return rows, err
}

// AllLiveBatches lists every batch with stock remaining (for the batch report),
// earliest-expiry first then product name.
func (r *Repository) AllLiveBatches(ctx context.Context) ([]Batch, error) {
	var rows []Batch
	err := r.q.SelectContext(ctx, &rows, `
		SELECT b.*, p.name AS product_name, u.abbreviation AS unit_abbr
		FROM stock_batches b
		JOIN products p ON p.id = b.product_id
		JOIN units u    ON u.id = p.unit_id
		WHERE b.qty_remaining > 0
		ORDER BY b.expiry_date NULLS LAST, p.name, b.id`)
	return rows, err
}

// ExpiringBefore lists live batches that expire on/before `cutoff` (or are
// already expired), earliest first — the data behind the expiry report.
func (r *Repository) ExpiringBefore(ctx context.Context, cutoff time.Time) ([]Batch, error) {
	var rows []Batch
	err := r.q.SelectContext(ctx, &rows, `
		SELECT b.*, p.name AS product_name, u.abbreviation AS unit_abbr
		FROM stock_batches b
		JOIN products p ON p.id = b.product_id
		JOIN units u    ON u.id = p.unit_id
		WHERE b.qty_remaining > 0 AND b.expiry_date IS NOT NULL AND b.expiry_date <= $1
		ORDER BY b.expiry_date, b.id`, cutoff)
	return rows, err
}
