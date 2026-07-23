package stock

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"karots-pos/internal/apperr"

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
	// SellingPrice is this lot's own price. Zero means "follow the product's
	// current selling price" — see EffectivePrice.
	SellingPrice decimal.Decimal `db:"selling_price"    json:"selling_price"`
	Source       string          `db:"source"           json:"source"`
	CreatedAt    time.Time       `db:"created_at"       json:"created_at"`
	// joined
	ProductName string `db:"product_name" json:"product_name"`
	UnitAbbr    string `db:"unit_abbr"    json:"unit_abbr"`
	// ProductPrice is the product's current price, so a report listing lots can
	// resolve EffectivePrice without a second lookup per row.
	ProductPrice decimal.Decimal `db:"product_price" json:"product_price"`
}

// NewBatch is the data needed to add a lot to inventory.
type NewBatch struct {
	ProductID      int64
	PurchaseItemID *int64
	BatchNo        *string
	ExpiryDate     *time.Time
	Quantity       decimal.Decimal
	CostPrice      decimal.Decimal
	// SellingPrice is optional: leave it zero and the lot follows the product's
	// current price, which is what every caller did before per-lot pricing.
	SellingPrice decimal.Decimal
	Source       string // purchase|opening|adjust|return|conversion
}

// EffectivePrice is THE rule for what a lot sells at: its own price when one was
// entered, otherwise the product's current price. Every query below repeats it in
// SQL (as effectivePriceSQL); keep the two in step.
func (b Batch) EffectivePrice(productPrice decimal.Decimal) decimal.Decimal {
	if b.SellingPrice.IsPositive() {
		return b.SellingPrice
	}
	return productPrice
}

// effectivePriceSQL is EffectivePrice expressed over aliases b (stock_batches)
// and p (products).
const effectivePriceSQL = `CASE WHEN b.selling_price > 0 THEN b.selling_price ELSE p.selling_price END`

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
	// selling_price gets no such rescue: zero is a meaningful value there (it
	// means "follow the product"), so it is stored exactly as supplied.
	err := r.q.GetContext(ctx, &id, `
		INSERT INTO stock_batches
			(product_id, purchase_item_id, batch_no, expiry_date, qty_received, qty_remaining, cost_price, selling_price, source)
		VALUES ($1,$2,$3,$4,$5,$5,
			CASE WHEN $6::numeric = 0
			     THEN COALESCE((SELECT cost_price FROM products WHERE id = $1), 0)
			     ELSE $6::numeric END,
			$7, $8) RETURNING id`,
		b.ProductID, b.PurchaseItemID, b.BatchNo, b.ExpiryDate, b.Quantity, b.CostPrice, b.SellingPrice, b.Source)
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

// PriceOption is one live lot offered to the cashier when a product's batches
// disagree on price: enough to match the sticker on the package in the
// customer's hand (the price itself, plus expiry / batch no / when it arrived).
type PriceOption struct {
	BatchID      int64           `db:"id"            json:"batch_id"`
	ProductID    int64           `db:"product_id"    json:"product_id"`
	BatchNo      *string         `db:"batch_no"      json:"batch_no,omitempty"`
	ExpiryDate   *time.Time      `db:"expiry_date"   json:"expiry_date,omitempty"`
	QtyRemaining decimal.Decimal `db:"qty_remaining" json:"qty_remaining"`
	Price        decimal.Decimal `db:"price"         json:"price"`
	// OwnPrice distinguishes a lot priced in its own right from one merely
	// inheriting the product's price, so the admin batch list can say so.
	OwnPrice   bool      `db:"own_price"  json:"own_price"`
	ReceivedAt time.Time `db:"created_at" json:"received_at"`
}

// PriceOptions lists a product's live lots with the price each would ring up at,
// FEFO order (so the first entry is the one normal rotation would sell). The
// caller decides whether the prices actually differ.
func (r *Repository) PriceOptions(ctx context.Context, productID int64) ([]PriceOption, error) {
	var rows []PriceOption
	err := r.q.SelectContext(ctx, &rows, `
		SELECT b.id, b.product_id, b.batch_no, b.expiry_date, b.qty_remaining, b.created_at,
		       `+effectivePriceSQL+` AS price,
		       (b.selling_price > 0) AS own_price
		FROM stock_batches b
		JOIN products p ON p.id = b.product_id
		WHERE b.product_id = $1 AND b.qty_remaining > 0
		ORDER BY b.expiry_date NULLS LAST, b.id`, productID)
	return rows, err
}

// MultiPriceProducts returns, for every product whose live lots disagree on
// price, that product's options — and nothing else. One query for the whole
// catalogue: the till loads it once and can then decide whether to prompt with no
// further round trips, however the item was added (scan, menu card, search).
//
// Until per-lot prices are actually entered every lot inherits the product's
// price, so COUNT(DISTINCT price) is 1 everywhere and this comes back empty.
func (r *Repository) MultiPriceProducts(ctx context.Context) (map[int64][]PriceOption, error) {
	var rows []PriceOption
	err := r.q.SelectContext(ctx, &rows, `
		WITH live AS (
			SELECT b.id, b.product_id, b.batch_no, b.expiry_date, b.qty_remaining, b.created_at,
			       `+effectivePriceSQL+` AS price,
			       (b.selling_price > 0) AS own_price
			FROM stock_batches b
			JOIN products p ON p.id = b.product_id
			WHERE b.qty_remaining > 0 AND p.is_active AND NOT p.is_service
		), multi AS (
			SELECT product_id FROM live GROUP BY product_id HAVING COUNT(DISTINCT price) > 1
		)
		SELECT l.* FROM live l
		JOIN multi m ON m.product_id = l.product_id
		ORDER BY l.product_id, l.expiry_date NULLS LAST, l.id`)
	if err != nil {
		return nil, err
	}
	out := make(map[int64][]PriceOption)
	for _, o := range rows {
		out[o.ProductID] = append(out[o.ProductID], o)
	}
	return out, nil
}

// LockBatch loads one lot FOR UPDATE, refusing a batch that is gone or belongs to
// a different product — the till sends a batch id, so this is where a stale or
// tampered id is caught. Runs inside the caller's transaction.
func (r *Repository) LockBatch(ctx context.Context, batchID, productID int64) (*Batch, error) {
	var b Batch
	err := r.q.GetContext(ctx, &b,
		`SELECT * FROM stock_batches WHERE id = $1 AND product_id = $2 FOR UPDATE`,
		batchID, productID)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// DepleteBatch consumes qty from ONE named lot (the cashier picked it because it
// matches the sticker on the package), leaving every other lot alone, and returns
// that lot's cost for the COGS snapshot. Callers must have locked the row with
// LockBatch and checked qty_remaining first.
//
// The zero-cost rescue matches DepleteFEFO: a lot created with a blank cost box
// is charged at the product's cost rather than booking the whole sale as profit.
func (r *Repository) DepleteBatch(ctx context.Context, batchID int64, qty decimal.Decimal) (decimal.Decimal, error) {
	if !qty.IsPositive() {
		return decimal.Zero, nil
	}
	var b struct {
		ProductID int64           `db:"product_id"`
		Cost      decimal.Decimal `db:"cost_price"`
	}
	if err := r.q.GetContext(ctx, &b,
		`UPDATE stock_batches SET qty_remaining = qty_remaining - $1
		 WHERE id = $2 RETURNING product_id, cost_price`, qty, batchID); err != nil {
		return decimal.Zero, err
	}
	fallback := decimal.Zero
	if b.Cost.IsZero() {
		var err error
		if fallback, err = r.productCost(ctx, b.ProductID); err != nil {
			return decimal.Zero, err
		}
	}
	return costOfConsumed([]consumedLot{{Qty: qty, Cost: b.Cost}}, qty, fallback), nil
}

// depleteChosen removes qty from a named lot, or falls back to FEFO when no lot
// was named. It is the one place the "which lot is leaving" decision is made for
// every non-sale removal — write-offs, own-use, staff, and stock corrections —
// so they cannot drift apart.
//
// Naming a lot matters once lots carry different prices: taking the shortfall off
// the oldest lot is a guess, and a wrong guess leaves the books claiming stock at
// a price the shop no longer has for the till to go on offering.
func (r *Repository) depleteChosen(ctx context.Context, productID, batchID int64, qty decimal.Decimal) (decimal.Decimal, error) {
	if batchID <= 0 {
		cost, err := r.DepleteFEFO(ctx, productID, qty)
		if err != nil {
			return decimal.Zero, apperr.Internal("failed to deplete batches", err)
		}
		return cost, nil
	}
	b, err := r.LockBatch(ctx, batchID, productID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return decimal.Zero, apperr.Validation("that batch is no longer available")
		}
		return decimal.Zero, apperr.Internal("failed to load batch", err)
	}
	if b.QtyRemaining.LessThan(qty) {
		return decimal.Zero, apperr.Conflict("only " + b.QtyRemaining.String() + " left in that batch")
	}
	cost, err := r.DepleteBatch(ctx, b.ID, qty)
	if err != nil {
		return decimal.Zero, apperr.Internal("failed to deplete batch", err)
	}
	return cost, nil
}

// SetBatchSellingPrice re-prices one lot from the admin batch list. Zero puts the
// lot back on the product's current price.
func (r *Repository) SetBatchSellingPrice(ctx context.Context, batchID int64, price decimal.Decimal) error {
	_, err := r.q.ExecContext(ctx,
		`UPDATE stock_batches SET selling_price = $1 WHERE id = $2`, price, batchID)
	return err
}

// RestockLot puts returned units back into the lot they were sold from, and
// reports whether it could.
//
// A return used to always open a NEW lot, so a customer bringing one bottle back
// left the shop holding three lots of the same thing where it had one. Do that a
// few dozen times and the batch list is unreadable, FEFO order is decided by
// which return came first, and a product whose lots all agreed on price can
// sprout a spurious "which price?" prompt.
//
// Only ever restocks a lot that still exists and belongs to the product; the
// caller falls back to opening a return lot when this returns false (an old sale
// with no recorded lot, or one whose lot has since been archived).
func (r *Repository) RestockLot(ctx context.Context, batchID *int64, productID int64, qty decimal.Decimal) (bool, error) {
	if batchID == nil || *batchID <= 0 || !qty.IsPositive() {
		return false, nil
	}
	res, err := r.q.ExecContext(ctx, `
		UPDATE stock_batches SET qty_remaining = qty_remaining + $1
		 WHERE id = $2 AND product_id = $3`, qty, *batchID, productID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ClearBatchPrices drops the per-lot price from every live lot of a product, so
// they follow the product's price again.
//
// Raising a shelf price otherwise leaves the stock on hand stranded at whatever
// it was stickered at when it arrived: the till card advertises the new price
// while every option in the "which price?" prompt is the old one, and the shop
// goes on selling at yesterday's price until those lots drain, with nothing on
// screen saying so. This is the deliberate "apply it to what I already have"
// action — it clears rather than stamps, so the lots track future price changes
// too instead of being frozen at today's.
func (r *Repository) ClearBatchPrices(ctx context.Context, productID int64) error {
	_, err := r.q.ExecContext(ctx,
		`UPDATE stock_batches SET selling_price = 0
		  WHERE product_id = $1 AND qty_remaining > 0 AND selling_price > 0`, productID)
	return err
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
		SELECT b.*, p.name AS product_name, p.selling_price AS product_price, u.abbreviation AS unit_abbr
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
		SELECT b.*, p.name AS product_name, p.selling_price AS product_price, u.abbreviation AS unit_abbr
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
		SELECT b.*, p.name AS product_name, p.selling_price AS product_price, u.abbreviation AS unit_abbr
		FROM stock_batches b
		JOIN products p ON p.id = b.product_id
		JOIN units u    ON u.id = p.unit_id
		WHERE b.qty_remaining > 0 AND b.expiry_date IS NOT NULL AND b.expiry_date <= $1
		ORDER BY b.expiry_date, b.id`, cutoff)
	return rows, err
}
