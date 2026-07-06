package cashflow

import (
	"context"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// Receipt is one money-movement receipt: a dated, numbered record of a single
// cashflow.Move that can be handed over, tucked in the rubber band with cash, or
// reprinted later. The number (CR-000042) is unique and searchable, mirroring
// sale receipts.
type Receipt struct {
	ID            int64           `db:"id"            json:"id"`
	ReceiptNo     string          `db:"receipt_no"    json:"receipt_no"`
	Kind          string          `db:"kind"          json:"kind"`
	FromLabel     string          `db:"from_label"    json:"from_label"`
	ToLabel       string          `db:"to_label"      json:"to_label"`
	Party         string          `db:"party"         json:"party"`
	Amount        decimal.Decimal `db:"amount"        json:"amount"`
	Note          string          `db:"note"          json:"note"`
	RefKind       *string         `db:"ref_kind"      json:"ref_kind,omitempty"`
	RefID         *int64          `db:"ref_id"        json:"ref_id,omitempty"`
	CreatedBy     *int64          `db:"created_by"    json:"created_by,omitempty"`
	CreatedAt     time.Time       `db:"created_at"    json:"created_at"`
	CreatedByName *string         `db:"created_by_name" json:"created_by_name,omitempty"`
}

// ReceiptInput is one receipt row to insert (written by Move inside its tx).
type ReceiptInput struct {
	Kind      string
	FromLabel string
	ToLabel   string
	Party     string
	Amount    decimal.Decimal
	Note      string
	RefKind   *string
	RefID     *int64
	CreatedBy *int64
}

// ReceiptFilter narrows the receipts list (To is exclusive — the web layer
// passes the day after the chosen end date, matching the report range helper).
type ReceiptFilter struct {
	Query string // matches receipt_no / party / from_label / to_label (blank = any)
	Kind  string // exact kind (blank = any)
	From  *time.Time
	To    *time.Time
	Limit int
}

// ReceiptRepository runs receipt queries against either the pool or a tx.
type ReceiptRepository struct{ q db.Queryer }

func NewReceiptRepository(q db.Queryer) *ReceiptRepository { return &ReceiptRepository{q: q} }

// Insert appends one receipt, assigning the CR- number atomically from the
// sequence inside the same transaction as the money move. Returns the full row.
func (r *ReceiptRepository) Insert(ctx context.Context, in ReceiptInput) (*Receipt, error) {
	var rec Receipt
	err := r.q.GetContext(ctx, &rec, `
		INSERT INTO money_receipts
			(receipt_no, kind, from_label, to_label, party, amount, note, ref_kind, ref_id, created_by)
		VALUES ('CR-' || lpad(nextval('money_receipt_seq')::text, 6, '0'),
			$1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING *, (SELECT name FROM users WHERE id = created_by) AS created_by_name`,
		in.Kind, in.FromLabel, in.ToLabel, in.Party, in.Amount, in.Note, in.RefKind, in.RefID, in.CreatedBy)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// Get loads one receipt with the cashier name resolved.
func (r *ReceiptRepository) Get(ctx context.Context, id int64) (*Receipt, error) {
	var rec Receipt
	err := r.q.GetContext(ctx, &rec, `
		SELECT mr.*, u.name AS created_by_name
		FROM money_receipts mr
		LEFT JOIN users u ON u.id = mr.created_by
		WHERE mr.id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// List returns receipts in the given range, newest first.
func (r *ReceiptRepository) List(ctx context.Context, f ReceiptFilter) ([]Receipt, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var q, kind *string
	if f.Query != "" {
		q = &f.Query
	}
	if f.Kind != "" {
		kind = &f.Kind
	}
	var rows []Receipt
	err := r.q.SelectContext(ctx, &rows, `
		SELECT mr.*, u.name AS created_by_name
		FROM money_receipts mr
		LEFT JOIN users u ON u.id = mr.created_by
		WHERE ($1::timestamptz IS NULL OR mr.created_at >= $1)
		  AND ($2::timestamptz IS NULL OR mr.created_at <  $2)
		  AND ($3::text IS NULL OR mr.kind = $3)
		  AND ($4::text IS NULL OR
		       mr.receipt_no ILIKE '%' || $4 || '%' OR
		       mr.party      ILIKE '%' || $4 || '%' OR
		       mr.from_label ILIKE '%' || $4 || '%' OR
		       mr.to_label   ILIKE '%' || $4 || '%')
		ORDER BY mr.created_at DESC, mr.id DESC
		LIMIT $5`, f.From, f.To, kind, q, limit)
	return rows, err
}

// ReceiptService wraps the repository for the web layer (read paths).
type ReceiptService struct {
	db   *sqlx.DB
	repo *ReceiptRepository
}

func NewReceiptService(db *sqlx.DB) *ReceiptService {
	return &ReceiptService{db: db, repo: NewReceiptRepository(db)}
}

func (s *ReceiptService) List(ctx context.Context, f ReceiptFilter) ([]Receipt, error) {
	rows, err := s.repo.List(ctx, f)
	if err != nil {
		return nil, apperr.Internal("failed to list money receipts", err)
	}
	return rows, nil
}

func (s *ReceiptService) Get(ctx context.Context, id int64) (*Receipt, error) {
	rec, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, apperr.NotFound("receipt")
	}
	return rec, nil
}
