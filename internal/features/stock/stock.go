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
			(product_id, type, quantity, reference_id, reference_type, user_id, note)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		m.ProductID, m.Type, m.Quantity, m.ReferenceID, m.ReferenceType, m.UserID, m.Note)
	return err
}

func (r *Repository) ListMovements(ctx context.Context, productID *int64, mtype string, limit int) ([]Movement, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
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
