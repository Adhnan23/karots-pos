package cashflow

import (
	"context"
	"time"

	"karots-pos/internal/apperr"

	"github.com/shopspring/decimal"
)

// LedgerRow is one line of the unified cash-flow ledger — a locker ledger entry
// or a till cash movement, normalized to a single shape. Delta is signed from the
// location's perspective: positive = cash in, negative = cash out.
type LedgerRow struct {
	CreatedAt time.Time       `db:"created_at"`
	Location  string          `db:"location"`
	Kind      string          `db:"kind"`
	Delta     decimal.Decimal `db:"delta"`
	Note      string          `db:"note"`
}

// UnifiedLedger merges locker_ledger and the till cash_movements into one
// time-ordered ledger over [from, to). It is the spine of the combined cash-flow
// view — every tracked money event in one place.
func (s *Service) UnifiedLedger(ctx context.Context, from, to time.Time, limit int) ([]LedgerRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	var rows []LedgerRow
	err := s.db.SelectContext(ctx, &rows, `
		SELECT created_at, location, kind, delta, note FROM (
			SELECT ll.created_at,
			       l.name        AS location,
			       ll.kind       AS kind,
			       ll.balance_delta AS delta,
			       ll.note       AS note
			FROM locker_ledger ll
			JOIN lockers l ON l.id = ll.locker_id
			WHERE ll.created_at >= $1 AND ll.created_at < $2
			UNION ALL
			SELECT cm.created_at,
			       'Till — ' || u.name AS location,
			       cm.type::text       AS kind,
			       cm.amount           AS delta,
			       COALESCE(cm.reason, '') AS note
			FROM cash_movements cm
			JOIN users u ON u.id = cm.user_id
			WHERE cm.created_at >= $1 AND cm.created_at < $2
			  AND cm.type IN ('withdrawal','pay_in','credit_payment','refund')
		) x
		ORDER BY created_at DESC
		LIMIT $3`, from, to, limit)
	if err != nil {
		return nil, apperr.Internal("failed to load cash-flow ledger", err)
	}
	return rows, nil
}
