// Package documents is the communication-store plugin: photocopy / print /
// laminate / bind (metered, priced from a matrix with quantity tiers and
// consuming paper/film stock) plus custom labour jobs (photo editing, CV,
// posters) with a typed price and a per-worker "mini salary" payout. Each service
// sells through the core sale path via a hidden is_service product; consumables
// are depleted by the core consume-on-sale seam.
package documents

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// Store is the plugin's data access over the core database. Cross-table refs to
// core (products, sales, users) are by id only; the plugin never alters core schema.
type Store struct{ db *sqlx.DB }

func NewStore(db *sqlx.DB) *Store { return &Store{db: db} }

type Service struct {
	ID        int64  `db:"id"         json:"id"`
	Name      string `db:"name"       json:"name"`
	Kind      string `db:"kind"       json:"kind"`     // metered | custom
	Category  string `db:"category"   json:"category"` // copy|print|laminate|bind|other
	ProductID int64  `db:"product_id" json:"product_id"`
	IsActive  bool   `db:"is_active"  json:"is_active"`
}

type Price struct {
	ID         int64           `db:"id"          json:"id"`
	ServiceID  int64           `db:"service_id"  json:"service_id"`
	Size       *string         `db:"size"        json:"size"`
	Color      bool            `db:"color"       json:"color"`
	DoubleSide bool            `db:"double_side" json:"double_side"`
	MinQty     int             `db:"min_qty"     json:"min_qty"`
	UnitPrice  decimal.Decimal `db:"unit_price"  json:"unit_price"`
}

type Consumable struct {
	ID          int64           `db:"id"           json:"id"`
	ServiceID   int64           `db:"service_id"   json:"service_id"`
	Size        *string         `db:"size"         json:"size"`
	ProductID   int64           `db:"product_id"   json:"product_id"`
	QtyPerUnit  decimal.Decimal `db:"qty_per_unit" json:"qty_per_unit"`
	ProductName string          `db:"product_name" json:"product_name"`
}

type Job struct {
	ID             int64           `db:"id"               json:"id"`
	SaleID         *int64          `db:"sale_id"          json:"sale_id"`
	ServiceID      *int64          `db:"service_id"       json:"service_id"`
	Description    string          `db:"description"      json:"description"`
	Qty            decimal.Decimal `db:"qty"              json:"qty"`
	UnitPrice      decimal.Decimal `db:"unit_price"       json:"unit_price"`
	LineTotal      decimal.Decimal `db:"line_total"       json:"line_total"`
	ConsumableCost decimal.Decimal `db:"consumable_cost"  json:"consumable_cost"`
	LabourWorkerID *int64          `db:"labour_worker_id" json:"labour_worker_id"`
	LabourAmount   decimal.Decimal `db:"labour_amount"    json:"labour_amount"`
	LabourPayoutID *int64          `db:"labour_payout_id" json:"labour_payout_id"`
}

// serviceDefaults resolves a category id (ensuring a "Documents" category exists)
// and a unit id for a service's hidden product. Touches core reference tables
// additively only.
func (s *Store) serviceDefaults(ctx context.Context) (catID, unitID int64, err error) {
	if err = s.db.GetContext(ctx, &unitID, `SELECT id FROM units ORDER BY id LIMIT 1`); err != nil {
		return 0, 0, err
	}
	err = s.db.GetContext(ctx, &catID, `SELECT id FROM categories WHERE name = 'Documents' LIMIT 1`)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.GetContext(ctx, &catID, `INSERT INTO categories (name) VALUES ('Documents') RETURNING id`)
	}
	return catID, unitID, err
}

// ---- services ----

func (s *Store) CreateService(ctx context.Context, name, kind, category string, productID int64) (int64, error) {
	var id int64
	err := s.db.GetContext(ctx, &id, `
		INSERT INTO doc_service (name, kind, category, product_id)
		VALUES ($1,$2,$3,$4) RETURNING id`, name, kind, category, productID)
	return id, err
}

func (s *Store) Services(ctx context.Context, activeOnly bool) ([]Service, error) {
	q := `SELECT id,name,kind,category,product_id,is_active FROM doc_service`
	if activeOnly {
		q += ` WHERE is_active = true`
	}
	q += ` ORDER BY category, name`
	var rows []Service
	err := s.db.SelectContext(ctx, &rows, q)
	return rows, err
}

func (s *Store) ServiceByID(ctx context.Context, id int64) (*Service, error) {
	var sv Service
	err := s.db.GetContext(ctx, &sv, `SELECT id,name,kind,category,product_id,is_active FROM doc_service WHERE id=$1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sv, nil
}

func (s *Store) DeactivateService(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE doc_service SET is_active=false WHERE id=$1`, id)
	return err
}

// ---- prices ----

func (s *Store) AddPrice(ctx context.Context, p Price) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO doc_price (service_id, size, color, double_side, min_qty, unit_price)
		VALUES ($1,$2,$3,$4,$5,$6)`, p.ServiceID, p.Size, p.Color, p.DoubleSide, p.MinQty, p.UnitPrice)
	return err
}

func (s *Store) Prices(ctx context.Context, serviceID int64) ([]Price, error) {
	var rows []Price
	err := s.db.SelectContext(ctx, &rows, `
		SELECT id,service_id,size,color,double_side,min_qty,unit_price
		FROM doc_price WHERE service_id=$1
		ORDER BY size NULLS FIRST, color, double_side, min_qty`, serviceID)
	return rows, err
}

func (s *Store) DeletePrice(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM doc_price WHERE id=$1`, id)
	return err
}

// ResolveUnitPrice picks the matrix row for (service,size,color,double_side) with
// the greatest min_qty <= qty. A NULL-size row matches any size (fallback). Returns
// the per-unit price and whether a rule was found.
func (s *Store) ResolveUnitPrice(ctx context.Context, serviceID int64, size string, color, doubleSide bool, qty int) (decimal.Decimal, bool, error) {
	var price decimal.Decimal
	err := s.db.GetContext(ctx, &price, `
		SELECT unit_price FROM doc_price
		WHERE service_id=$1 AND color=$3 AND double_side=$4 AND min_qty<=$5
		  AND (size = $2 OR size IS NULL)
		ORDER BY (size IS NOT NULL) DESC, min_qty DESC
		LIMIT 1`, serviceID, nullStr(size), color, doubleSide, qty)
	if errors.Is(err, sql.ErrNoRows) {
		return decimal.Zero, false, nil
	}
	if err != nil {
		return decimal.Zero, false, err
	}
	return price, true, nil
}

// ---- consumables ----

func (s *Store) AddConsumable(ctx context.Context, c Consumable) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO doc_consumable (service_id, size, product_id, qty_per_unit)
		VALUES ($1,$2,$3,$4)`, c.ServiceID, c.Size, c.ProductID, c.QtyPerUnit)
	return err
}

func (s *Store) Consumables(ctx context.Context, serviceID int64) ([]Consumable, error) {
	var rows []Consumable
	err := s.db.SelectContext(ctx, &rows, `
		SELECT dc.id, dc.service_id, dc.size, dc.product_id, dc.qty_per_unit, p.name AS product_name
		FROM doc_consumable dc JOIN products p ON p.id = dc.product_id
		WHERE dc.service_id=$1 ORDER BY dc.size NULLS FIRST`, serviceID)
	return rows, err
}

func (s *Store) DeleteConsumable(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM doc_consumable WHERE id=$1`, id)
	return err
}

// ConsumablesFor returns the consumable mappings that apply to a service+size
// (size-specific rows preferred, else NULL-size fallback rows).
func (s *Store) ConsumablesFor(ctx context.Context, serviceID int64, size string) ([]Consumable, error) {
	var rows []Consumable
	err := s.db.SelectContext(ctx, &rows, `
		SELECT dc.id, dc.service_id, dc.size, dc.product_id, dc.qty_per_unit, p.name AS product_name
		FROM doc_consumable dc JOIN products p ON p.id = dc.product_id
		WHERE dc.service_id=$1 AND (dc.size=$2 OR dc.size IS NULL)
		ORDER BY (dc.size IS NOT NULL) DESC`, serviceID, nullStr(size))
	return rows, err
}

// ---- jobs ----

func (s *Store) InsertJob(ctx context.Context, j Job) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO doc_job (sale_id, service_id, description, qty, unit_price, line_total,
		                     consumable_cost, labour_worker_id, labour_amount)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		j.SaleID, j.ServiceID, j.Description, j.Qty, j.UnitPrice, j.LineTotal,
		j.ConsumableCost, j.LabourWorkerID, j.LabourAmount)
	return err
}

// ConsumableCost estimates a product's unit cost (for the plugin's profit report;
// core finance already has the exact FEFO cost on the sale line).
func (s *Store) ConsumableCost(ctx context.Context, productID int64) decimal.Decimal {
	var c decimal.Decimal
	_ = s.db.GetContext(ctx, &c, `SELECT cost_price FROM products WHERE id=$1`, productID)
	return c
}

// ---- workers & payouts ----

type Worker struct {
	ID   int64  `db:"id"   json:"id"`
	Name string `db:"name" json:"name"`
}

func (s *Store) Workers(ctx context.Context) ([]Worker, error) {
	var rows []Worker
	err := s.db.SelectContext(ctx, &rows, `SELECT id, name FROM users WHERE is_active = true ORDER BY name`)
	return rows, err
}

// WorkerBalance is a worker's unpaid labour total + paid-to-date.
type WorkerBalance struct {
	WorkerID int64           `db:"worker_id" json:"worker_id"`
	Name     string          `db:"name"      json:"name"`
	Unpaid   decimal.Decimal `db:"unpaid"    json:"unpaid"`
	Jobs     int             `db:"jobs"      json:"jobs"`
}

func (s *Store) WorkerBalances(ctx context.Context) ([]WorkerBalance, error) {
	var rows []WorkerBalance
	err := s.db.SelectContext(ctx, &rows, `
		SELECT u.id AS worker_id, u.name,
		       COALESCE(SUM(j.labour_amount),0) AS unpaid, COUNT(j.id) AS jobs
		FROM doc_job j JOIN users u ON u.id = j.labour_worker_id
		WHERE j.labour_payout_id IS NULL AND j.labour_amount > 0
		GROUP BY u.id, u.name ORDER BY u.name`)
	return rows, err
}

// UnpaidTotal is a single worker's outstanding labour.
func (s *Store) UnpaidTotal(ctx context.Context, workerID int64) (decimal.Decimal, error) {
	var t decimal.Decimal
	err := s.db.GetContext(ctx, &t, `
		SELECT COALESCE(SUM(labour_amount),0) FROM doc_job
		WHERE labour_worker_id=$1 AND labour_payout_id IS NULL AND labour_amount>0`, workerID)
	return t, err
}

// WorkerName returns a worker's display name ("" if unknown).
func (s *Store) WorkerName(ctx context.Context, id int64) string {
	var n string
	_ = s.db.GetContext(ctx, &n, `SELECT name FROM users WHERE id=$1`, id)
	return n
}

// SettleWorker books a payout: it sums the worker's unpaid labour, records a
// payout row (with the core expense id), and stamps the covered jobs. Returns the
// settled amount (zero when nothing was due).
func (s *Store) SettleWorker(ctx context.Context, workerID, expenseID int64) (decimal.Decimal, int64, error) {
	var settled decimal.Decimal
	var payoutID int64
	err := WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &settled, `
			SELECT COALESCE(SUM(labour_amount),0) FROM doc_job
			WHERE labour_worker_id=$1 AND labour_payout_id IS NULL AND labour_amount>0`, workerID); err != nil {
			return err
		}
		if !settled.IsPositive() {
			return nil
		}
		var exp any
		if expenseID > 0 {
			exp = expenseID
		}
		if err := tx.GetContext(ctx, &payoutID, `
			INSERT INTO doc_payout (worker_id, amount, expense_id) VALUES ($1,$2,$3) RETURNING id`,
			workerID, settled, exp); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE doc_job SET labour_payout_id=$2
			WHERE labour_worker_id=$1 AND labour_payout_id IS NULL AND labour_amount>0`, workerID, payoutID)
		return err
	})
	return settled, payoutID, err
}

// ---- report aggregates ----

type ServiceTotals struct {
	Category    string          `db:"category"     json:"category"`
	Name        string          `db:"name"         json:"name"`
	Jobs        int             `db:"jobs"         json:"jobs"`
	Revenue     decimal.Decimal `db:"revenue"      json:"revenue"`
	Consumables decimal.Decimal `db:"consumables"  json:"consumables"`
	Labour      decimal.Decimal `db:"labour"       json:"labour"`
}

func (s *Store) ServiceTotals(ctx context.Context, from, to any) ([]ServiceTotals, error) {
	var rows []ServiceTotals
	err := s.db.SelectContext(ctx, &rows, `
		SELECT COALESCE(sv.category,'other') AS category, COALESCE(sv.name,'(deleted)') AS name,
		       COUNT(j.id) AS jobs, COALESCE(SUM(j.line_total),0) AS revenue,
		       COALESCE(SUM(j.consumable_cost),0) AS consumables, COALESCE(SUM(j.labour_amount),0) AS labour
		FROM doc_job j LEFT JOIN doc_service sv ON sv.id = j.service_id
		WHERE j.created_at >= $1 AND j.created_at < $2
		GROUP BY sv.category, sv.name ORDER BY revenue DESC`, from, to)
	return rows, err
}

// PaperUsed totals consumable units + cost over a range (from stock movements that
// reference document sales is complex; we report from doc_job's recorded cost).
type PaperRow struct {
	ProductName string          `db:"product_name" json:"product_name"`
	Cost        decimal.Decimal `db:"cost"         json:"cost"`
}

// nullStr maps "" to a NULL string parameter.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// WithTx runs fn in a transaction (small local helper to avoid importing core db).
func WithTx(ctx context.Context, db *sqlx.DB, fn func(tx *sqlx.Tx) error) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
