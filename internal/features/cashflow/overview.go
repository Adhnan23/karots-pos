package cashflow

import (
	"context"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/cashregister"
	"karots-pos/internal/features/lockers"
	"karots-pos/internal/money"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// TillLockerLeg books the locker side (+ a CR- receipt) of a till cash event,
// inside the cashregister tx. It is injected into cashregister via WithLockerLeg
// so that package never imports cashflow. The till side is handled by the
// cashregister itself (opening_cash, closing, or the drawer movement), so this
// only writes the locker ledger row and the receipt.
//
// Direction (delta on the locker):
//   - open  / payin    : cash came FROM the locker → locker decreases (guarded)
//   - close / withdraw : cash went   TO the locker → locker increases
func (s *Service) TillLockerLeg(ctx context.Context, tx *sqlx.Tx, ev cashregister.TillCashEvent) error {
	lrepo := lockers.NewRepository(tx)
	out := ev.Kind == "open" || ev.Kind == "payin" // money leaving the locker

	l, err := lrepo.GetForUpdate(ctx, ev.LockerID)
	if err != nil {
		return apperr.NotFound("locker")
	}
	if out && !l.AllowNegative && ev.Amount.GreaterThan(l.Balance) {
		return apperr.Conflict(l.Name + " only has " + money.Display(l.Balance) + " available")
	}

	delta := ev.Amount
	if out {
		delta = ev.Amount.Neg()
	}
	sessID := ev.SessionID
	counter := "till"
	if _, err := lrepo.AddEntry(ctx, lockers.LedgerInput{
		LockerID: ev.LockerID, BalanceDelta: delta, Kind: "transfer",
		Counterparty: &counter, CounterTillSession: &sessID, Note: ev.Reason,
		CreatedBy: actorPtr(ev.UserID),
	}); err != nil {
		return apperr.Internal("failed to record locker movement", err)
	}

	tillLabel := "Till"
	var name string
	if err := tx.GetContext(ctx, &name, `SELECT name FROM users WHERE id = $1`, ev.UserID); err == nil && name != "" {
		tillLabel = "Till — " + name
	}
	from, to := tillLabel, l.Name // money into the locker (close / withdraw)
	if out {
		from, to = l.Name, tillLabel
	}
	if _, err := NewReceiptRepository(tx).Insert(ctx, ReceiptInput{
		Kind: "transfer", FromLabel: from, ToLabel: to, Amount: ev.Amount,
		Note: ev.Reason, CreatedBy: actorPtr(ev.UserID),
	}); err != nil {
		return apperr.Internal("failed to record money receipt", err)
	}
	return nil
}

func actorPtr(id int64) *int64 {
	if id <= 0 {
		return nil
	}
	return &id
}

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
