package conversions

import (
	"context"
	"time"

	"karots-pos/internal/apperr"

	"github.com/shopspring/decimal"
)

// Conversion run history.
//
// conversion_runs has been recorded since the feature shipped — who ran what,
// when, and how much moved — but nothing ever read it back. The table even
// carries an idx_conversion_runs_created_at index built for a listing that did
// not exist. Without this view a conversion is an untraceable stock change,
// which is exactly the thing you need traceable once cashiers can run them.

type Run struct {
	ID            int64           `db:"id"              json:"id"`
	ConversionID  *int64          `db:"conversion_id"   json:"conversion_id,omitempty"`
	FromProductID int64           `db:"from_product_id" json:"from_product_id"`
	ToProductID   int64           `db:"to_product_id"   json:"to_product_id"`
	FromQty       decimal.Decimal `db:"from_qty"        json:"from_qty"`
	ToQty         decimal.Decimal `db:"to_qty"          json:"to_qty"`
	CreatedBy     int64           `db:"created_by"      json:"created_by"`
	CreatedAt     time.Time       `db:"created_at"      json:"created_at"`
	// joined
	FromName     string `db:"from_name"      json:"from_name"`
	ToName       string `db:"to_name"        json:"to_name"`
	FromUnitAbbr string `db:"from_unit_abbr" json:"from_unit_abbr"`
	ToUnitAbbr   string `db:"to_unit_abbr"   json:"to_unit_abbr"`
	UserName     string `db:"user_name"      json:"user_name"`
}

// RunFilter pages and narrows the history. Limit 0 means "every match", used by
// the CSV export.
type RunFilter struct {
	ConversionID *int64
	From, To     *time.Time
	Limit        int
	Offset       int
}

const selectRun = `
	SELECT r.*, fp.name AS from_name, tp.name AS to_name,
	       fu.abbreviation AS from_unit_abbr, tu.abbreviation AS to_unit_abbr,
	       u.name AS user_name
	FROM conversion_runs r
	JOIN products fp ON fp.id = r.from_product_id
	JOIN products tp ON tp.id = r.to_product_id
	JOIN units fu ON fu.id = fp.unit_id
	JOIN units tu ON tu.id = tp.unit_id
	JOIN users u  ON u.id = r.created_by`

// runWhere is shared by the list and the count so a page can never be filtered
// differently from the total shown beside it.
const runWhere = `
	WHERE ($1::bigint      IS NULL OR r.conversion_id = $1)
	  AND ($2::timestamptz IS NULL OR r.created_at >= $2)
	  AND ($3::timestamptz IS NULL OR r.created_at <  $3)`

func (r *Repository) ListRuns(ctx context.Context, f RunFilter) ([]Run, error) {
	var rows []Run
	err := r.q.SelectContext(ctx, &rows, selectRun+runWhere+`
		ORDER BY r.created_at DESC, r.id DESC
		LIMIT NULLIF($4, 0) OFFSET $5`,
		f.ConversionID, f.From, f.To, f.Limit, f.Offset)
	return rows, err
}

func (r *Repository) CountRuns(ctx context.Context, f RunFilter) (int, error) {
	var n int
	err := r.q.GetContext(ctx, &n,
		`SELECT count(*) FROM conversion_runs r`+runWhere, f.ConversionID, f.From, f.To)
	return n, err
}

// ListRuns returns one page of the history plus the total number of matches.
func (s *Service) ListRuns(ctx context.Context, f RunFilter) ([]Run, int, error) {
	total, err := s.repo.CountRuns(ctx, f)
	if err != nil {
		return nil, 0, apperr.Internal("failed to count conversion runs", err)
	}
	rows, err := s.repo.ListRuns(ctx, f)
	if err != nil {
		return nil, 0, apperr.Internal("failed to list conversion runs", err)
	}
	return rows, total, nil
}
