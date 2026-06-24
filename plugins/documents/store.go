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
	"time"

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

// InsertJob records a completed job's analytics row. Labour is left at zero here:
// the cashier only sets the price, and the admin settles labour per job later.
func (s *Store) InsertJob(ctx context.Context, j Job) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO doc_job (sale_id, service_id, description, qty, unit_price, line_total,
		                     consumable_cost)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		j.SaleID, j.ServiceID, j.Description, j.Qty, j.UnitPrice, j.LineTotal,
		j.ConsumableCost)
	return err
}

// ConsumableCost estimates a product's unit cost (for the plugin's profit report;
// core finance already has the exact FEFO cost on the sale line).
func (s *Store) ConsumableCost(ctx context.Context, productID int64) decimal.Decimal {
	var c decimal.Decimal
	_ = s.db.GetContext(ctx, &c, `SELECT cost_price FROM products WHERE id=$1`, productID)
	return c
}

// ---- labour payments (per individual custom job) ----

// UnpaidJob is one custom-service job awaiting a labour decision in the admin
// panel: its receipt details + the price the cashier charged for it.
type UnpaidJob struct {
	ID          int64           `db:"id"           json:"id"`
	SaleID      *int64          `db:"sale_id"      json:"sale_id"`
	ServiceName string          `db:"service_name" json:"service_name"`
	Description string          `db:"description"  json:"description"`
	LineTotal   decimal.Decimal `db:"line_total"   json:"line_total"`
	CreatedAt   time.Time       `db:"created_at"   json:"created_at"`
}

// UnpaidJobs lists custom-service jobs that have not yet been settled (paid or
// dismissed). Metered work (photocopy/print) carries no labour, so it is excluded.
func (s *Store) UnpaidJobs(ctx context.Context) ([]UnpaidJob, error) {
	var rows []UnpaidJob
	err := s.db.SelectContext(ctx, &rows, `
		SELECT j.id, j.sale_id, COALESCE(sv.name,'(deleted)') AS service_name,
		       j.description, j.line_total, j.created_at
		FROM doc_job j JOIN doc_service sv ON sv.id = j.service_id
		WHERE j.labour_payout_id IS NULL AND sv.kind = 'custom'
		ORDER BY j.created_at DESC`)
	return rows, err
}

// UnpaidJob fetches a single still-unsettled custom job by id (nil when not found
// or already settled) — used to validate before booking money.
func (s *Store) UnpaidJob(ctx context.Context, id int64) (*UnpaidJob, error) {
	var j UnpaidJob
	err := s.db.GetContext(ctx, &j, `
		SELECT j.id, j.sale_id, COALESCE(sv.name,'(deleted)') AS service_name,
		       j.description, j.line_total, j.created_at
		FROM doc_job j JOIN doc_service sv ON sv.id = j.service_id
		WHERE j.id=$1 AND j.labour_payout_id IS NULL AND sv.kind = 'custom'`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// SettleInput records a labour decision for one job. A pay books amount>0 with a
// "Labour" expense (and, when Source=="till", that till's drawer withdrawal); a
// dismiss is amount 0 / source "none" with no expense. Either way the job leaves
// the unpaid worklist.
type SettleInput struct {
	JobID     int64
	Amount    decimal.Decimal
	Note      string
	Source    string // external | till | none
	TillUID   *int64
	ExpenseID int64
}

// SettleJob writes the payout row and stamps the job in one transaction.
func (s *Store) SettleJob(ctx context.Context, in SettleInput) error {
	return WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		var exp any
		if in.ExpenseID > 0 {
			exp = in.ExpenseID
		}
		var till any
		if in.TillUID != nil {
			till = *in.TillUID
		}
		var payoutID int64
		if err := tx.GetContext(ctx, &payoutID, `
			INSERT INTO doc_payout (job_id, amount, note, source, till_user_id, expense_id)
			VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
			in.JobID, in.Amount, in.Note, in.Source, till, exp); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE doc_job SET labour_amount=$2, labour_payout_id=$3
			WHERE id=$1 AND labour_payout_id IS NULL`, in.JobID, in.Amount, payoutID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errors.New("job already settled")
		}
		return nil
	})
}

// LabourPayment is one settled labour decision for the history log: what was paid
// (or dismissed), on which job, how, and when.
type LabourPayment struct {
	ID          int64           `db:"id"           json:"id"`
	PaidAt      time.Time       `db:"paid_at"      json:"paid_at"`
	ServiceName string          `db:"service_name" json:"service_name"`
	Description string          `db:"description"  json:"description"`
	Amount      decimal.Decimal `db:"amount"       json:"amount"`
	Note        string          `db:"note"         json:"note"`
	Source      string          `db:"source"       json:"source"` // till | external | none
	TillName    *string         `db:"till_name"    json:"till_name"`
}

// LabourHistory lists the most recent per-job labour settlements (paid and
// dismissed), newest first.
func (s *Store) LabourHistory(ctx context.Context, limit int) ([]LabourPayment, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows []LabourPayment
	err := s.db.SelectContext(ctx, &rows, `
		SELECT p.id, p.paid_at, COALESCE(sv.name,'(deleted)') AS service_name,
		       COALESCE(j.description,'') AS description, p.amount, p.note, p.source,
		       u.name AS till_name
		FROM doc_payout p
		LEFT JOIN doc_job j ON j.id = p.job_id
		LEFT JOIN doc_service sv ON sv.id = j.service_id
		LEFT JOIN users u ON u.id = p.till_user_id
		WHERE p.job_id IS NOT NULL
		ORDER BY p.paid_at DESC LIMIT $1`, limit)
	return rows, err
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
