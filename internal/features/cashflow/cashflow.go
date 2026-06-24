// Package cashflow is the single, atomic way money moves between the shop's
// tracked cash locations. Every cash event — a locker↔locker transfer, paying an
// expense from a till or a locker, collecting customer credit into a drawer, a
// refund, a capital injection — is expressed as a Move from one Location to
// another. Move writes both sides (a cash_movements row for a till leg, a
// locker_ledger row for a locker leg) inside ONE database transaction, so the
// two sides always commit together or not at all and the cash-flow record can
// never drift.
//
// A Location is a Till (a cashier's open drawer), a Locker (safe / bank / pocket),
// or External — the trading counterparty (supplier, customer, the bank, fresh
// capital). External is never one of "your own piles": it is only ever the far
// side of money coming in or going out, and nothing is written for it.
package cashflow

import (
	"context"
	"database/sql"
	"errors"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/cashregister"
	"karots-pos/internal/features/lockers"
	"karots-pos/internal/features/sales"
	"karots-pos/internal/money"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// Kind enumerates the sorts of cash location.
type Kind int

const (
	KindExternal Kind = iota // a trading counterparty; nothing is written for it
	KindTill                 // a cashier's open drawer (ID = cashier user id)
	KindLocker               // a core locker (ID = locker id)
)

// Location names one endpoint of a move.
type Location struct {
	Kind Kind
	ID   int64
}

// Till is the open drawer of cashier userID.
func Till(userID int64) Location { return Location{Kind: KindTill, ID: userID} }

// Locker is the core locker with the given id.
func Locker(id int64) Location { return Location{Kind: KindLocker, ID: id} }

// External is the trading counterparty (supplier, customer, bank, capital).
func External() Location { return Location{Kind: KindExternal} }

func (l Location) tracked() bool { return l.Kind != KindExternal }

// Ref softly links the move's locker ledger rows back to the domain record that
// caused it (an expense, a supplier payment, a customer payment, ...).
type Ref struct {
	Kind string
	ID   int64
}

// MoveInput describes one money movement.
type MoveInput struct {
	From   Location
	To     Location
	Amount decimal.Decimal
	Reason string
	Ref    *Ref
	// LockerKind overrides the locker_ledger.kind for any locker leg. Leave empty
	// to let Move classify it (transfer / payment / intake). Used by the bank
	// charge / interest / adjust flows that need an explicit kind.
	LockerKind string
	// ActorID is the user performing the move (recorded as created_by on locker
	// ledger rows). For a till leg the drawer's own cashier id is used.
	ActorID int64
}

// Service performs atomic money moves.
type Service struct {
	db    *sqlx.DB
	sales *sales.Service
}

// NewService builds the cashflow service. salesSvc is used only to read committed
// cash sales when guarding a till against overdraw.
func NewService(db *sqlx.DB, salesSvc *sales.Service) *Service {
	return &Service{db: db, sales: salesSvc}
}

// Move transfers Amount from in.From to in.To atomically. It guards the source
// against overdraw (a till, or a locker with allow_negative=false, cannot go
// below zero — 409 Conflict) and writes whichever sides are tracked.
func (s *Service) Move(ctx context.Context, in MoveInput) error {
	if !in.Amount.IsPositive() {
		return apperr.Validation("amount must be greater than zero")
	}
	if !in.From.tracked() && !in.To.tracked() {
		return apperr.Validation("a move needs at least one tracked location")
	}
	if in.From.Kind == in.To.Kind && in.From.ID == in.To.ID && in.From.tracked() {
		return apperr.Validation("source and destination must be different")
	}

	return appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		crepo := cashregister.NewRepository(tx)
		lrepo := lockers.NewRepository(tx)

		// Resolve any till sessions up front (locked) — needed both for the
		// overdraw guard and to reference the session from the locker leg.
		var fromSess, toSess *cashregister.Session
		if in.From.Kind == KindTill {
			sess, err := crepo.FindOpenForUpdate(ctx, in.From.ID)
			if err != nil {
				return tillSessionErr(err)
			}
			fromSess = sess
		}
		if in.To.Kind == KindTill {
			sess, err := crepo.FindOpenForUpdate(ctx, in.To.ID)
			if err != nil {
				return tillSessionErr(err)
			}
			toSess = sess
		}

		if err := s.guardSource(ctx, crepo, lrepo, in, fromSess); err != nil {
			return err
		}
		if err := s.writeLeg(ctx, crepo, lrepo, in, true, fromSess, toSess); err != nil {
			return err
		}
		return s.writeLeg(ctx, crepo, lrepo, in, false, fromSess, toSess)
	})
}

// guardSource blocks a move that would overdraw the source.
func (s *Service) guardSource(ctx context.Context, crepo *cashregister.Repository, lrepo *lockers.Repository, in MoveInput, fromSess *cashregister.Session) error {
	switch in.From.Kind {
	case KindTill:
		// Available = opening float + committed cash sales + net adjustments —
		// the same figure the close reconciles to.
		cashSales, err := s.sales.CashCollectedSince(ctx, in.From.ID, fromSess.OpenedAt)
		if err != nil {
			return err
		}
		adj, err := crepo.AdjustmentTotal(ctx, fromSess.ID)
		if err != nil {
			return apperr.Internal("failed to total cash movements", err)
		}
		available := fromSess.OpeningCash.Add(cashSales).Add(adj)
		if in.Amount.GreaterThan(available) {
			return apperr.Conflict("the till only has " + money.Display(available) + " available")
		}
	case KindLocker:
		l, err := lrepo.GetForUpdate(ctx, in.From.ID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return apperr.NotFound("locker")
			}
			return apperr.Internal("failed to load locker", err)
		}
		if !l.AllowNegative && in.Amount.GreaterThan(l.Balance) {
			return apperr.Conflict(l.Name + " only has " + money.Display(l.Balance) + " available")
		}
	}
	return nil
}

// writeLeg writes one endpoint of the move (the source when isSource, else the
// destination). External legs write nothing.
func (s *Service) writeLeg(ctx context.Context, crepo *cashregister.Repository, lrepo *lockers.Repository, in MoveInput, isSource bool, fromSess, toSess *cashregister.Session) error {
	self, other := in.To, in.From
	sess, otherSess := toSess, fromSess
	if isSource {
		self, other = in.From, in.To
		sess, otherSess = fromSess, toSess
	}

	switch self.Kind {
	case KindExternal:
		return nil
	case KindTill:
		mtype := cashregister.MovePayIn
		amt := in.Amount
		if isSource {
			mtype = cashregister.MoveWithdrawal
			amt = in.Amount.Neg()
		}
		if err := crepo.AddMovement(ctx, sess.ID, self.ID, mtype, amt, in.Reason, nil); err != nil {
			return apperr.Internal("failed to record drawer movement", err)
		}
		return nil
	case KindLocker:
		delta := in.Amount
		if isSource {
			delta = in.Amount.Neg()
		}
		kind := in.LockerKind
		if kind == "" {
			kind = defaultLockerKind(other.Kind, isSource)
		}
		entry := lockers.LedgerInput{
			LockerID:     self.ID,
			BalanceDelta: delta,
			Kind:         kind,
			Counterparty: counterpartyTag(other.Kind),
			Note:         in.Reason,
		}
		if other.Kind == KindLocker {
			id := other.ID
			entry.CounterLockerID = &id
		}
		if other.Kind == KindTill && otherSess != nil {
			id := otherSess.ID
			entry.CounterTillSession = &id
		}
		if in.Ref != nil {
			entry.RefKind = &in.Ref.Kind
			entry.RefID = &in.Ref.ID
		}
		if in.ActorID > 0 {
			by := in.ActorID
			entry.CreatedBy = &by
		}
		if _, err := lrepo.AddEntry(ctx, entry); err != nil {
			return apperr.Internal("failed to record locker movement", err)
		}
		return nil
	}
	return nil
}

// defaultLockerKind classifies a locker leg from the other endpoint: money
// between own piles is a transfer; money to/from External is a payment (out) or
// an intake (in).
func defaultLockerKind(other Kind, isSource bool) string {
	switch other {
	case KindLocker, KindTill:
		return "transfer"
	default: // External
		if isSource {
			return "payment" // money leaving the locker to a counterparty
		}
		return "intake" // money entering the locker from a counterparty
	}
}

func counterpartyTag(k Kind) *string {
	var s string
	switch k {
	case KindTill:
		s = "till"
	case KindLocker:
		s = "locker"
	default:
		s = "external"
	}
	return &s
}

func tillSessionErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.Conflict("that till has no open session")
	}
	return apperr.Internal("failed to load till session", err)
}
