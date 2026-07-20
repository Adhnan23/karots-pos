// Package stock owns on-hand quantities and the immutable movement audit trail.
// Its repository methods take db.Queryer so the sales transaction can reuse the
// guarded-decrement and movement-insert primitives atomically.
package stock

import (
	"context"
	"time"

	"karots-pos/internal/db"

	"github.com/shopspring/decimal"
)

// Movement types mirror the stock_movement_type enum.
const (
	MovePurchase       = "purchase"
	MoveSale           = "sale"
	MoveAdjust         = "adjust"
	MoveReturn         = "return"
	MoveDamage         = "damage"
	MoveConversion     = "conversion"
	MovePurchaseReturn = "purchase_return"
	MoveWarranty       = "warranty_replacement"
	MoveRecovery       = "recovery"
)

type Movement struct {
	ID            int64           `db:"id"             json:"id"`
	ProductID     int64           `db:"product_id"     json:"product_id"`
	Type          string          `db:"type"           json:"type"`
	Quantity      decimal.Decimal `db:"quantity"       json:"quantity"`
	ReferenceID   *int64          `db:"reference_id"   json:"reference_id,omitempty"`
	ReferenceType *string         `db:"reference_type" json:"reference_type,omitempty"`
	UserID        int64           `db:"user_id"        json:"user_id"`
	Note          *string         `db:"note"           json:"note,omitempty"`
	Cost          decimal.Decimal `db:"cost"           json:"cost"`
	CreatedAt     time.Time       `db:"created_at"     json:"created_at"`
	// joined
	ProductName string `db:"product_name" json:"product_name"`
	UserName    string `db:"user_name"    json:"user_name"`
}

// MovementInput is a row to append to the audit trail.
type MovementInput struct {
	ProductID     int64
	Type          string
	Quantity      decimal.Decimal // signed: + in, - out
	ReferenceID   *int64
	ReferenceType *string
	UserID        int64
	Note          *string
	Cost          decimal.Decimal // worth of the goods moved (damage/warranty/recovery)
}

type Repository struct{ q db.Queryer }

func NewRepository(q db.Queryer) *Repository { return &Repository{q: q} }

func (r *Repository) GetQuantity(ctx context.Context, productID int64) (decimal.Decimal, error) {
	var qty decimal.Decimal
	err := r.q.GetContext(ctx, &qty,
		`SELECT quantity FROM stock WHERE product_id = $1`, productID)
	return qty, err
}

// DecrementGuarded subtracts qty only if enough stock exists. It returns true
// when the row was updated; false means insufficient stock (no oversell). This
// single atomic statement is what prevents the concurrent-oversell race in the
// original plan.
func (r *Repository) DecrementGuarded(ctx context.Context, productID int64, qty decimal.Decimal) (bool, error) {
	res, err := r.q.ExecContext(ctx,
		`UPDATE stock SET quantity = quantity - $1, last_updated = NOW()
		 WHERE product_id = $2 AND quantity >= $1`, qty, productID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (r *Repository) Increment(ctx context.Context, productID int64, qty decimal.Decimal) error {
	_, err := r.q.ExecContext(ctx,
		`UPDATE stock SET quantity = quantity + $1, last_updated = NOW() WHERE product_id = $2`,
		qty, productID)
	return err
}

func (r *Repository) SetQuantity(ctx context.Context, productID int64, qty decimal.Decimal) error {
	_, err := r.q.ExecContext(ctx,
		`UPDATE stock SET quantity = $1, last_updated = NOW() WHERE product_id = $2`,
		qty, productID)
	return err
}

func (r *Repository) InsertMovement(ctx context.Context, m MovementInput) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO stock_movements
			(product_id, type, quantity, reference_id, reference_type, user_id, note, cost)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		m.ProductID, m.Type, m.Quantity, m.ReferenceID, m.ReferenceType, m.UserID, m.Note, m.Cost)
	return err
}

// MaxMovementLimit is the largest page ListMovements will serve.
const MaxMovementLimit = 500

func (r *Repository) ListMovements(ctx context.Context, productID *int64, mtype string, limit int) ([]Movement, error) {
	// Clamp DOWN TO the maximum, never below it. The old "> 500 → 100" rule
	// meant over-asking returned fewer rows than a modest request.
	switch {
	case limit <= 0:
		limit = 100
	case limit > MaxMovementLimit:
		limit = MaxMovementLimit
	}
	var t *string
	if mtype != "" {
		t = &mtype
	}
	var rows []Movement
	err := r.q.SelectContext(ctx, &rows, `
		SELECT m.*, p.name AS product_name, u.name AS user_name
		FROM stock_movements m
		JOIN products p ON p.id = m.product_id
		JOIN users u    ON u.id = m.user_id
		WHERE ($1::bigint IS NULL OR m.product_id = $1)
		  AND ($2::text IS NULL OR m.type = $2::stock_movement_type)
		ORDER BY m.created_at DESC
		LIMIT $3`, productID, t, limit)
	return rows, err
}

// MovementFilter drives the paged audit-trail view: the movement history is
// append-only and grows with every sale, so it must be filtered and paged
// rather than read as "the most recent N".
type MovementFilter struct {
	ProductID *int64
	Type      string
	From, To  *time.Time
	Limit     int
	Offset    int
}

// movementWhere is shared by the list and the count so a page can never be
// filtered differently from the total shown beside it.
const movementWhere = `
	WHERE ($1::bigint      IS NULL OR m.product_id = $1)
	  AND ($2::text        IS NULL OR m.type = $2::stock_movement_type)
	  AND ($3::timestamptz IS NULL OR m.created_at >= $3)
	  AND ($4::timestamptz IS NULL OR m.created_at <  $4)`

// FindMovements returns one page of the audit trail, newest first. The id is a
// tiebreaker so two movements written in the same instant can't swap places
// between pages and hide a row.
func (r *Repository) FindMovements(ctx context.Context, f MovementFilter) ([]Movement, error) {
	var rows []Movement
	err := r.q.SelectContext(ctx, &rows, `
		SELECT m.*, p.name AS product_name, u.name AS user_name
		FROM stock_movements m
		JOIN products p ON p.id = m.product_id
		JOIN users u    ON u.id = m.user_id`+movementWhere+`
		ORDER BY m.created_at DESC, m.id DESC
		LIMIT NULLIF($5, 0) OFFSET $6`,
		f.ProductID, nilIfEmpty(f.Type), f.From, f.To, f.Limit, f.Offset)
	return rows, err
}

// CountMovements is how many movements match the filter, ignoring paging.
func (r *Repository) CountMovements(ctx context.Context, f MovementFilter) (int, error) {
	var n int
	err := r.q.GetContext(ctx, &n, `
		SELECT count(*) FROM stock_movements m`+movementWhere,
		f.ProductID, nilIfEmpty(f.Type), f.From, f.To)
	return n, err
}
