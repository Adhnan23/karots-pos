package repairs

import (
	"context"
	"fmt"
	"time"

	"karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// Store is all DB access for the repairs plugin. It holds a db.Queryer so the
// same methods run on the pooled *sqlx.DB in production and on a *sqlx.Tx in
// rollback tests (mirrors the core repositories).
type Store struct{ q db.Queryer }

func NewStore(db *sqlx.DB) *Store   { return &Store{q: db} }
func newStoreQ(q db.Queryer) *Store { return &Store{q: q} }

// ---- types (columns mirror migration 0001) ----

type Job struct {
	ID            int64      `db:"id"`
	TicketNo      string     `db:"ticket_no"`
	TicketCode    string     `db:"ticket_code"`
	CustomerID    *int64     `db:"customer_id"`
	CustomerName  string     `db:"customer_name"`
	CustomerPhone string     `db:"customer_phone"`
	RepairType    string     `db:"repair_type"`
	DeviceModel   string     `db:"device_model"`
	Fault         string     `db:"fault"`
	Status        string     `db:"status"`
	PromisedDate  *time.Time `db:"promised_date"`
	Urgent        bool       `db:"urgent"`
	WarrantyDays  int        `db:"warranty_days"`
	WarrantyUntil *time.Time `db:"warranty_until"`
	SaleID        *int64     `db:"sale_id"`
	ReworkOf      *int64     `db:"rework_of"`
	Notes         string     `db:"notes"`
	CreatedBy     *int64     `db:"created_by"`
	CreatedAt     time.Time  `db:"created_at"`
	ReadyAt       *time.Time `db:"ready_at"`
	CollectedAt   *time.Time `db:"collected_at"`
	CancelledAt   *time.Time `db:"cancelled_at"`
}

type Part struct {
	ID            int64           `db:"id"`
	JobID         int64           `db:"job_id"`
	ProductID     int64           `db:"product_id"`
	Qty           decimal.Decimal `db:"qty"`
	UnitCharge    decimal.Decimal `db:"unit_charge"`
	Discount      decimal.Decimal `db:"discount"`
	DiscountType  string          `db:"discount_type"`
	DiscountValue decimal.Decimal `db:"discount_value"`
	ProductName   string          `db:"product_name"` // joined
}

type Charge struct {
	ID     int64           `db:"id"`
	JobID  int64           `db:"job_id"`
	Label  string          `db:"label"`
	Amount decimal.Decimal `db:"amount"`
}

type Payment struct {
	ID        int64           `db:"id"`
	JobID     int64           `db:"job_id"`
	Amount    decimal.Decimal `db:"amount"`
	Kind      string          `db:"kind"`
	UserID    *int64          `db:"user_id"`
	CreatedAt time.Time       `db:"created_at"`
}

type Detail struct {
	Job      Job
	Parts    []Part
	Charges  []Charge
	Payments []Payment
}

// ---- inputs ----

type JobInput struct {
	CustomerID    *int64
	CustomerName  string
	CustomerPhone string
	RepairType    string
	DeviceModel   string
	Fault         string
	Notes         string
	PromisedDate  *time.Time
	WarrantyDays  int
	Urgent        bool
	ReworkOf      *int64
	CreatedBy     int64
}

type PartInput struct {
	ProductID     int64
	Qty           decimal.Decimal
	UnitCharge    decimal.Decimal
	DiscountType  string
	DiscountValue decimal.Decimal
}

// ---- config + ticket sequence ----

// EnsureConfig loads the singleton config, creating the labour/service product
// on first run (via ensureLabour) and stamping its id. Returns the labour
// product id and the default warranty days.
func (s *Store) EnsureConfig(ctx context.Context, ensureLabour func() (int64, error)) (int64, int, error) {
	var cfg struct {
		LabourProductID     int64 `db:"labour_product_id"`
		DefaultWarrantyDays int   `db:"default_warranty_days"`
	}
	if err := s.q.GetContext(ctx, &cfg,
		`SELECT labour_product_id, default_warranty_days FROM repair_config WHERE id = 1`); err != nil {
		return 0, 0, err
	}
	if cfg.LabourProductID == 0 {
		pid, err := ensureLabour()
		if err != nil {
			return 0, 0, err
		}
		if _, err := s.q.ExecContext(ctx,
			`UPDATE repair_config SET labour_product_id = $1 WHERE id = 1`, pid); err != nil {
			return 0, 0, err
		}
		cfg.LabourProductID = pid
	}
	return cfg.LabourProductID, cfg.DefaultWarrantyDays, nil
}

// NextTicket atomically advances the per-shop counter and returns the human
// ref (R-0001) and the scannable code (RPR000001).
func (s *Store) NextTicket(ctx context.Context) (string, string, error) {
	var seq int64
	err := s.q.GetContext(ctx, &seq,
		`UPDATE repair_config SET ticket_seq = ticket_seq + 1 WHERE id = 1 RETURNING ticket_seq`)
	if err != nil {
		return "", "", err
	}
	return fmt.Sprintf("R-%04d", seq), fmt.Sprintf("RPR%06d", seq), nil
}

// ---- jobs ----

// CreateJob generates a ticket and inserts a received job. Returns the new id.
func (s *Store) CreateJob(ctx context.Context, in JobInput) (int64, error) {
	no, code, err := s.NextTicket(ctx)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.q.GetContext(ctx, &id, `
		INSERT INTO repair_jobs
			(ticket_no, ticket_code, customer_id, customer_name, customer_phone,
			 repair_type, device_model, fault, promised_date, urgent, warranty_days,
			 rework_of, notes, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING id`,
		no, code, in.CustomerID, in.CustomerName, in.CustomerPhone,
		in.RepairType, in.DeviceModel, in.Fault, in.PromisedDate, in.Urgent, in.WarrantyDays,
		in.ReworkOf, in.Notes, in.CreatedBy)
	return id, err
}

func (s *Store) GetJob(ctx context.Context, id int64) (*Detail, error) {
	var j Job
	if err := s.q.GetContext(ctx, &j, `SELECT * FROM repair_jobs WHERE id = $1`, id); err != nil {
		return nil, err
	}
	d := &Detail{Job: j}
	if err := s.q.SelectContext(ctx, &d.Parts, `
		SELECT rp.*, COALESCE(p.name, '') AS product_name
		FROM repair_parts rp LEFT JOIN products p ON p.id = rp.product_id
		WHERE rp.job_id = $1 ORDER BY rp.id`, id); err != nil {
		return nil, err
	}
	if err := s.q.SelectContext(ctx, &d.Charges,
		`SELECT * FROM repair_charges WHERE job_id = $1 ORDER BY id`, id); err != nil {
		return nil, err
	}
	if err := s.q.SelectContext(ctx, &d.Payments,
		`SELECT * FROM repair_payments WHERE job_id = $1 ORDER BY id`, id); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Store) JobByTicketCode(ctx context.Context, code string) (*Job, error) {
	var j Job
	err := s.q.GetContext(ctx, &j, `SELECT * FROM repair_jobs WHERE ticket_code = $1`, code)
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// UpdateJobFields rewrites the editable header fields of a (non-collected) job.
func (s *Store) UpdateJobFields(ctx context.Context, id int64, in JobInput) error {
	_, err := s.q.ExecContext(ctx, `
		UPDATE repair_jobs SET
			customer_id = $2, customer_name = $3, customer_phone = $4,
			repair_type = $5, device_model = $6, fault = $7,
			promised_date = $8, urgent = $9, warranty_days = $10, notes = $11
		WHERE id = $1`,
		id, in.CustomerID, in.CustomerName, in.CustomerPhone,
		in.RepairType, in.DeviceModel, in.Fault,
		in.PromisedDate, in.Urgent, in.WarrantyDays, in.Notes)
	return err
}

// SetStatus moves a job's status and stamps the matching timestamp column.
func (s *Store) SetStatus(ctx context.Context, id int64, status string, stamp *time.Time) error {
	col := map[string]string{"ready": "ready_at", "collected": "collected_at", "cancelled": "cancelled_at"}[status]
	if col == "" {
		_, err := s.q.ExecContext(ctx, `UPDATE repair_jobs SET status = $2 WHERE id = $1`, id, status)
		return err
	}
	_, err := s.q.ExecContext(ctx,
		`UPDATE repair_jobs SET status = $2, `+col+` = COALESCE($3, now()) WHERE id = $1`, id, status, stamp)
	return err
}

// MarkCollected links the settling sale, flips status, and stamps the warranty.
func (s *Store) MarkCollected(ctx context.Context, id, saleID int64, warrantyUntil *time.Time) error {
	_, err := s.q.ExecContext(ctx, `
		UPDATE repair_jobs
		SET status = 'collected', sale_id = $2, warranty_until = $3, collected_at = now()
		WHERE id = $1`, id, saleID, warrantyUntil)
	return err
}

// ---- parts / charges / payments ----

func (s *Store) AddPart(ctx context.Context, jobID int64, p PartInput) error {
	dtype := normDiscType(p.DiscountType)
	gross := p.Qty.Mul(p.UnitCharge)
	disc := resolvePartDiscount(dtype, p.DiscountValue, gross, p.Qty)
	_, err := s.q.ExecContext(ctx, `
		INSERT INTO repair_parts (job_id, product_id, qty, unit_charge, discount, discount_type, discount_value)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		jobID, p.ProductID, p.Qty, p.UnitCharge, disc, dtype, p.DiscountValue)
	return err
}

func (s *Store) RemovePart(ctx context.Context, id int64) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM repair_parts WHERE id = $1`, id)
	return err
}

func (s *Store) AddCharge(ctx context.Context, jobID int64, label string, amount decimal.Decimal) error {
	_, err := s.q.ExecContext(ctx,
		`INSERT INTO repair_charges (job_id, label, amount) VALUES ($1,$2,$3)`, jobID, label, amount)
	return err
}

func (s *Store) RemoveCharge(ctx context.Context, id int64) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM repair_charges WHERE id = $1`, id)
	return err
}

func (s *Store) AddPayment(ctx context.Context, jobID int64, amount decimal.Decimal, kind string, userID int64) error {
	_, err := s.q.ExecContext(ctx,
		`INSERT INTO repair_payments (job_id, amount, kind, user_id) VALUES ($1,$2,$3,$4)`,
		jobID, amount, kind, userID)
	return err
}

// ---- listings ----

func (s *Store) ListByStatuses(ctx context.Context, statuses []string) ([]Job, error) {
	var rows []Job
	q, args, err := sqlx.In(`SELECT * FROM repair_jobs WHERE status IN (?)
		ORDER BY (promised_date IS NULL), promised_date, created_at`, statuses)
	if err != nil {
		return nil, err
	}
	q = s.q.Rebind(q)
	err = s.q.SelectContext(ctx, &rows, q, args...)
	return rows, err
}

// ListForReport returns full details for jobs collected in [from,to).
func (s *Store) ListForReport(ctx context.Context, from, to time.Time) ([]Detail, error) {
	var ids []int64
	if err := s.q.SelectContext(ctx, &ids,
		`SELECT id FROM repair_jobs WHERE status = 'collected' AND collected_at >= $1 AND collected_at < $2
		 ORDER BY collected_at`, from, to); err != nil {
		return nil, err
	}
	out := make([]Detail, 0, len(ids))
	for _, id := range ids {
		d, err := s.GetJob(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, nil
}

func (s *Store) DistinctTypes(ctx context.Context) ([]string, error) {
	var v []string
	err := s.q.SelectContext(ctx, &v,
		`SELECT DISTINCT repair_type FROM repair_jobs WHERE repair_type <> '' ORDER BY 1`)
	return v, err
}

func (s *Store) DistinctModels(ctx context.Context) ([]string, error) {
	var v []string
	err := s.q.SelectContext(ctx, &v,
		`SELECT DISTINCT device_model FROM repair_jobs WHERE device_model <> '' ORDER BY 1`)
	return v, err
}
