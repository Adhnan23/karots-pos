// Package warranty tracks serial-numbered units sold under a manufacturer
// warranty and the claims (replacements) raised against them. A serialized
// product captures one unique serial per unit at the time of sale; a claim that
// ends in a replacement ships a new unit from stock with a fresh warranty.
package warranty

import (
	"context"
	"time"

	"karots-pos/internal/datetime"
	"karots-pos/internal/db"

	"github.com/shopspring/decimal"
)

// Unit is one serial-numbered item — either sold (source="sale") or issued as a
// free replacement (source="replacement").
type Unit struct {
	ID               int64     `db:"id"                  json:"id"`
	ProductID        int64     `db:"product_id"          json:"product_id"`
	SerialNo         string    `db:"serial_no"           json:"serial_no"`
	SaleID           *int64    `db:"sale_id"             json:"sale_id,omitempty"`
	CustomerID       *int64    `db:"customer_id"         json:"customer_id,omitempty"`
	SoldAt           time.Time `db:"sold_at"             json:"sold_at"`
	WarrantyMonths   int       `db:"warranty_months"     json:"warranty_months"`
	WarrantyUntil    time.Time `db:"warranty_until"      json:"warranty_until"`
	Source           string    `db:"source"              json:"source"`
	Status           string    `db:"status"              json:"status"`
	ReplacedByUnitID *int64    `db:"replaced_by_unit_id" json:"replaced_by_unit_id,omitempty"`
	CreatedAt        time.Time `db:"created_at"          json:"created_at"`
	// Joined, read-only:
	ProductName  string  `db:"product_name"  json:"product_name"`
	CustomerName *string `db:"customer_name" json:"customer_name,omitempty"`
	ReceiptNo    *string `db:"receipt_no"    json:"receipt_no,omitempty"`
	// Populated only by ListUnits (nil for single lookups): the worth of the
	// replacement handed out for this unit, and any supplier-recovery outcome.
	LossValue      *decimal.Decimal `db:"loss_value"      json:"loss_value,omitempty"`
	RecoveryStatus *string          `db:"recovery_status" json:"recovery_status,omitempty"`
}

// UnderWarranty reports whether the unit is still live and within its cover.
// warranty_until is a pure date, so an ISO date string compare is timezone-safe.
func (u Unit) UnderWarranty() bool {
	if u.Status != "active" {
		return false
	}
	return u.WarrantyUntil.UTC().Format("2006-01-02") >= datetime.Date(time.Now())
}

// Claim is one warranty claim against a unit.
type Claim struct {
	ID                int64     `db:"id"                  json:"id"`
	UnitID            int64     `db:"unit_id"             json:"unit_id"`
	ClaimDate         time.Time `db:"claim_date"          json:"claim_date"`
	Reason            *string   `db:"reason"              json:"reason,omitempty"`
	Resolution        string    `db:"resolution"          json:"resolution"`
	ReplacementUnitID *int64    `db:"replacement_unit_id" json:"replacement_unit_id,omitempty"`
	HandledBy         int64     `db:"handled_by"          json:"handled_by"`
	CreatedAt         time.Time `db:"created_at"          json:"created_at"`
	// Joined:
	HandledByName     string  `db:"handled_by_name"    json:"handled_by_name"`
	ReplacementSerial *string `db:"replacement_serial" json:"replacement_serial,omitempty"`
	ProductName       string  `db:"product_name"       json:"product_name"`
	OldSerial         string  `db:"old_serial"         json:"old_serial"`
	CustomerName      *string `db:"customer_name"      json:"customer_name,omitempty"`
}

// NewUnit is the data to insert a warranty unit (at sale or as a replacement).
type NewUnit struct {
	ProductID      int64
	SerialNo       string
	SaleID         *int64
	CustomerID     *int64
	SoldAt         time.Time
	WarrantyMonths int
	WarrantyUntil  time.Time
	Source         string
}

const selectUnit = `
	SELECT wu.*, p.name AS product_name, c.name AS customer_name, s.receipt_no AS receipt_no
	FROM warranty_units wu
	JOIN products p        ON p.id = wu.product_id
	LEFT JOIN customers c  ON c.id = wu.customer_id
	LEFT JOIN sales s      ON s.id = wu.sale_id`

type Repository struct{ q db.Queryer }

func NewRepository(q db.Queryer) *Repository { return &Repository{q: q} }

func (r *Repository) InsertUnit(ctx context.Context, u NewUnit) (int64, error) {
	source := u.Source
	if source == "" {
		source = "sale"
	}
	var id int64
	err := r.q.GetContext(ctx, &id, `
		INSERT INTO warranty_units
			(product_id, serial_no, sale_id, customer_id, sold_at, warranty_months, warranty_until, source)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING id`,
		u.ProductID, u.SerialNo, u.SaleID, u.CustomerID, u.SoldAt, u.WarrantyMonths, u.WarrantyUntil, source)
	return id, err
}

// SerialExists reports whether a serial is already on record (any product), so
// the caller can reject a duplicate with a friendly message before inserting.
func (r *Repository) SerialExists(ctx context.Context, serial string) (bool, error) {
	var exists bool
	err := r.q.GetContext(ctx, &exists,
		`SELECT EXISTS(SELECT 1 FROM warranty_units WHERE serial_no = $1)`, serial)
	return exists, err
}

func (r *Repository) FindUnitByID(ctx context.Context, id int64) (*Unit, error) {
	var u Unit
	if err := r.q.GetContext(ctx, &u, selectUnit+` WHERE wu.id = $1`, id); err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *Repository) FindUnitBySerial(ctx context.Context, serial string) (*Unit, error) {
	var u Unit
	if err := r.q.GetContext(ctx, &u, selectUnit+` WHERE wu.serial_no = $1`, serial); err != nil {
		return nil, err
	}
	return &u, nil
}

// UnitsForSale returns the serials recorded against a sale, for the receipt.
func (r *Repository) UnitsForSale(ctx context.Context, saleID int64) ([]Unit, error) {
	var rows []Unit
	err := r.q.SelectContext(ctx, &rows, selectUnit+` WHERE wu.sale_id = $1 ORDER BY wu.id`, saleID)
	return rows, err
}

// ListUnits returns warranty units filtered by status and an optional search on
// serial number or product name. status is one of all|active|expired|replaced.
func (r *Repository) ListUnits(ctx context.Context, status, search string, limit int) ([]Unit, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if status == "" {
		status = "all"
	}
	var s *string
	if search != "" {
		s = &search
	}
	// The replaced unit's worth is the cost on the warranty_replacement movement
	// of its 'replaced' claim; recovery_status is the outcome of any supplier
	// recovery recorded against this (faulty) unit.
	var rows []Unit
	err := r.q.SelectContext(ctx, &rows, `
		SELECT wu.*, p.name AS product_name, c.name AS customer_name, s.receipt_no AS receipt_no,
		       mv.cost AS loss_value, lr.outcome AS recovery_status
		FROM warranty_units wu
		JOIN products p        ON p.id = wu.product_id
		LEFT JOIN customers c  ON c.id = wu.customer_id
		LEFT JOIN sales s      ON s.id = wu.sale_id
		LEFT JOIN warranty_claims wcl ON wcl.unit_id = wu.id AND wcl.resolution = 'replaced'
		LEFT JOIN stock_movements mv  ON mv.reference_type = 'warranty' AND mv.reference_id = wcl.id
		LEFT JOIN loss_recoveries lr  ON lr.source_type = 'warranty' AND lr.source_id = wu.id
		WHERE ($1::text IS NULL OR wu.serial_no ILIKE '%' || $1 || '%' OR p.name ILIKE '%' || $1 || '%')
		  AND (
		    $2 = 'all'
		    OR ($2 = 'active'   AND wu.status = 'active'   AND wu.warranty_until >= CURRENT_DATE)
		    OR ($2 = 'expired'  AND wu.status = 'active'   AND wu.warranty_until <  CURRENT_DATE)
		    OR ($2 = 'replaced' AND wu.status = 'replaced')
		  )
		ORDER BY wu.created_at DESC
		LIMIT $3`, s, status, limit)
	return rows, err
}

func (r *Repository) ClaimsForUnit(ctx context.Context, unitID int64) ([]Claim, error) {
	var rows []Claim
	err := r.q.SelectContext(ctx, &rows, `
		SELECT wc.*, u.name AS handled_by_name, ru.serial_no AS replacement_serial
		FROM warranty_claims wc
		JOIN users u                ON u.id = wc.handled_by
		LEFT JOIN warranty_units ru ON ru.id = wc.replacement_unit_id
		WHERE wc.unit_id = $1
		ORDER BY wc.id DESC`, unitID)
	return rows, err
}

// ClaimFilter narrows the global claims list. To is exclusive (the web layer
// passes the day after the chosen end date, matching the report range helper).
type ClaimFilter struct {
	Search string // matches old/new serial or customer name (blank = any)
	From   *time.Time
	To     *time.Time
	Limit  int
}

// claimSelect lists claims with everything the receipts table + a reprinted slip
// need: the faulty (old) unit's product/serial/customer and the replacement
// unit's serial.
const claimSelect = `
	SELECT wc.*, u.name AS handled_by_name,
	       ou.serial_no AS old_serial,
	       p.name AS product_name,
	       c.name AS customer_name,
	       ru.serial_no AS replacement_serial
	FROM warranty_claims wc
	JOIN users u                ON u.id = wc.handled_by
	JOIN warranty_units ou      ON ou.id = wc.unit_id
	JOIN products p             ON p.id = ou.product_id
	LEFT JOIN customers c       ON c.id = ou.customer_id
	LEFT JOIN warranty_units ru ON ru.id = wc.replacement_unit_id`

// ListClaims returns recent claims across all units, newest first.
func (r *Repository) ListClaims(ctx context.Context, f ClaimFilter) ([]Claim, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var s *string
	if f.Search != "" {
		s = &f.Search
	}
	var rows []Claim
	err := r.q.SelectContext(ctx, &rows, claimSelect+`
		WHERE ($1::timestamptz IS NULL OR wc.created_at >= $1)
		  AND ($2::timestamptz IS NULL OR wc.created_at <  $2)
		  AND ($3::text IS NULL
		       OR ou.serial_no ILIKE '%' || $3 || '%'
		       OR ru.serial_no ILIKE '%' || $3 || '%'
		       OR c.name       ILIKE '%' || $3 || '%')
		ORDER BY wc.id DESC
		LIMIT $4`, f.From, f.To, s, limit)
	return rows, err
}

// GetClaim loads one claim with the same joined fields as ListClaims.
func (r *Repository) GetClaim(ctx context.Context, id int64) (*Claim, error) {
	var cl Claim
	if err := r.q.GetContext(ctx, &cl, claimSelect+` WHERE wc.id = $1`, id); err != nil {
		return nil, err
	}
	return &cl, nil
}

func (r *Repository) InsertClaim(ctx context.Context, unitID int64, reason *string, resolution string, replacementUnitID *int64, userID int64) (int64, error) {
	var id int64
	err := r.q.GetContext(ctx, &id, `
		INSERT INTO warranty_claims (unit_id, reason, resolution, replacement_unit_id, handled_by)
		VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		unitID, reason, resolution, replacementUnitID, userID)
	return id, err
}

func (r *Repository) MarkUnitReplaced(ctx context.Context, unitID, replacementUnitID int64) error {
	_, err := r.q.ExecContext(ctx,
		`UPDATE warranty_units SET status = 'replaced', replaced_by_unit_id = $2 WHERE id = $1`,
		unitID, replacementUnitID)
	return err
}

// ProductWarrantyMonths is the product's current default warranty length, used
// to stamp a fresh window onto a replacement unit.
func (r *Repository) ProductWarrantyMonths(ctx context.Context, productID int64) (int, error) {
	var months int
	err := r.q.GetContext(ctx, &months, `SELECT warranty_months FROM products WHERE id = $1`, productID)
	return months, err
}
