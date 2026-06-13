// Package recovery records what happens to a faulty item after it leaves
// inventory as a loss — a replaced warranty unit or a damage write-off. The
// shop often returns the faulty unit to its supplier, who either hands back a
// replacement (restocked), pays/credits the amount (the payable drops), or
// nothing (a full write-off). Each recovery is logged against its source loss
// so the worth lost and any value recovered surface in the finance P&L.
package recovery

import (
	"context"
	"time"

	"karots-pos/internal/db"

	"github.com/shopspring/decimal"
)

// Outcomes of a recovery action.
const (
	OutcomeReplacement = "replacement" // supplier handed back a unit; restocked
	OutcomePaid        = "paid"        // supplier paid/credited the amount
	OutcomeWrittenOff  = "written_off" // unrecoverable; full loss stands
)

// Source types a recovery can be raised against.
const (
	SourceWarranty = "warranty" // source_id = warranty_units.id (the faulty unit)
	SourceDamage   = "damage"   // source_id = stock_movements.id (the write-off)
)

// Recovery is one recorded recovery action against a loss.
type Recovery struct {
	ID             int64           `db:"id"              json:"id"`
	SourceType     string          `db:"source_type"     json:"source_type"`
	SourceID       int64           `db:"source_id"       json:"source_id"`
	ProductID      int64           `db:"product_id"      json:"product_id"`
	SupplierID     *int64          `db:"supplier_id"     json:"supplier_id,omitempty"`
	Outcome        string          `db:"outcome"         json:"outcome"`
	Quantity       decimal.Decimal `db:"quantity"        json:"quantity"`
	LossValue      decimal.Decimal `db:"loss_value"      json:"loss_value"`
	RecoveredValue decimal.Decimal `db:"recovered_value" json:"recovered_value"`
	Note           *string         `db:"note"            json:"note,omitempty"`
	HandledBy      int64           `db:"handled_by"      json:"handled_by"`
	CreatedAt      time.Time       `db:"created_at"       json:"created_at"`
}

// CreateInput is the form payload for recording a recovery.
type CreateInput struct {
	SourceType      string `form:"source_type"`
	SourceID        int64  `form:"source_id"`
	SupplierID      *int64 `form:"supplier_id"`
	Outcome         string `form:"outcome"`
	RecoveredAmount string `form:"recovered_amount"`
	Note            string `form:"note"`
}

// DamageLoss is a damage write-off row for the admin Damage report, with any
// recovery already recorded against it.
type DamageLoss struct {
	MovementID     int64           `db:"movement_id"     json:"movement_id"`
	ProductID      int64           `db:"product_id"      json:"product_id"`
	ProductName    string          `db:"product_name"    json:"product_name"`
	Quantity       decimal.Decimal `db:"quantity"        json:"quantity"`
	LossValue      decimal.Decimal `db:"loss_value"      json:"loss_value"`
	Note           *string         `db:"note"            json:"note,omitempty"`
	UserName       string          `db:"user_name"       json:"user_name"`
	CreatedAt      time.Time       `db:"created_at"       json:"created_at"`
	RecoveryStatus *string         `db:"recovery_status" json:"recovery_status,omitempty"`
	RecoveredValue decimal.Decimal `db:"recovered_value" json:"recovered_value"`
}

// source describes a resolved loss the recovery is being raised against.
type source struct {
	ProductID   int64           `db:"product_id"`
	ProductName string          `db:"product_name"`
	Quantity    decimal.Decimal `db:"quantity"`
	LossValue   decimal.Decimal `db:"loss_value"`
	Status      string          `db:"status"`
}

type Repository struct{ q db.Queryer }

func NewRepository(q db.Queryer) *Repository { return &Repository{q: q} }

// warrantySource resolves a replaced warranty unit: its product, qty (1) and the
// worth of the replacement handed out (the cost on its 'replaced' claim move).
func (r *Repository) warrantySource(ctx context.Context, unitID int64) (*source, error) {
	var s source
	err := r.q.GetContext(ctx, &s, `
		SELECT wu.product_id, p.name AS product_name,
		       1::numeric AS quantity,
		       COALESCE(mv.cost, 0) AS loss_value,
		       wu.status
		FROM warranty_units wu
		JOIN products p ON p.id = wu.product_id
		LEFT JOIN warranty_claims wcl ON wcl.unit_id = wu.id AND wcl.resolution = 'replaced'
		LEFT JOIN stock_movements mv  ON mv.reference_type = 'warranty' AND mv.reference_id = wcl.id
		WHERE wu.id = $1`, unitID)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// damageSource resolves a damage write-off movement: its product, quantity and
// the worth recorded on the movement.
func (r *Repository) damageSource(ctx context.Context, movementID int64) (*source, error) {
	var s source
	err := r.q.GetContext(ctx, &s, `
		SELECT m.product_id, p.name AS product_name,
		       ABS(m.quantity) AS quantity,
		       m.cost AS loss_value,
		       'damage' AS status
		FROM stock_movements m
		JOIN products p ON p.id = m.product_id
		WHERE m.id = $1 AND m.type = 'damage'`, movementID)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// RecoveredQtyForSource is how much has already been recovered against a source.
func (r *Repository) RecoveredQtyForSource(ctx context.Context, sourceType string, sourceID int64) (decimal.Decimal, error) {
	var q decimal.Decimal
	err := r.q.GetContext(ctx, &q,
		`SELECT COALESCE(SUM(quantity),0) FROM loss_recoveries WHERE source_type = $1 AND source_id = $2`,
		sourceType, sourceID)
	return q, err
}

func (r *Repository) Insert(ctx context.Context, rec Recovery) (int64, error) {
	var id int64
	err := r.q.GetContext(ctx, &id, `
		INSERT INTO loss_recoveries
			(source_type, source_id, product_id, supplier_id, outcome, quantity, loss_value, recovered_value, note, handled_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id`,
		rec.SourceType, rec.SourceID, rec.ProductID, rec.SupplierID, rec.Outcome,
		rec.Quantity, rec.LossValue, rec.RecoveredValue, rec.Note, rec.HandledBy)
	return id, err
}

// DamageLosses lists damage write-offs (newest first) with any recovery.
func (r *Repository) DamageLosses(ctx context.Context, limit int) ([]DamageLoss, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var rows []DamageLoss
	err := r.q.SelectContext(ctx, &rows, `
		SELECT m.id AS movement_id, m.product_id, p.name AS product_name,
		       ABS(m.quantity) AS quantity, m.cost AS loss_value, m.note,
		       u.name AS user_name, m.created_at,
		       lr.outcome AS recovery_status, COALESCE(lr.recovered_value,0) AS recovered_value
		FROM stock_movements m
		JOIN products p ON p.id = m.product_id
		JOIN users u    ON u.id = m.user_id
		LEFT JOIN loss_recoveries lr ON lr.source_type = 'damage' AND lr.source_id = m.id
		WHERE m.type = 'damage'
		ORDER BY m.created_at DESC
		LIMIT $1`, limit)
	return rows, err
}
