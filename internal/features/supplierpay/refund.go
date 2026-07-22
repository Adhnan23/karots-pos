package supplierpay

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/suppliers"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// Refund is money received back FROM a supplier — settling a credit they owe us
// after goods went back, or an advance we no longer want sitting with them.
//
// It is the mirror of a Payment and is kept as its own row type for that reason:
// folding refunds into supplier_payments as negative amounts would make every
// "how much have we paid this supplier" sum quietly wrong.
type Refund struct {
	ID         int64           `db:"id"          json:"id"`
	SupplierID int64           `db:"supplier_id" json:"supplier_id"`
	Amount     decimal.Decimal `db:"amount"      json:"amount"`
	Method     string          `db:"method"      json:"method"`
	Reference  *string         `db:"reference"   json:"reference,omitempty"`
	Note       *string         `db:"note"        json:"note,omitempty"`
	ReceivedBy *int64          `db:"received_by" json:"received_by,omitempty"`
	CreatedAt  time.Time       `db:"created_at"  json:"created_at"`
	// joined
	ReceivedByName *string `db:"received_by_name" json:"received_by_name,omitempty"`
}

// RefundInput is one refund received from a supplier.
type RefundInput struct {
	Amount    decimal.Decimal
	Method    string
	Reference string
	Note      string
}

// RefundResult summarises a recorded refund for the caller (the cash mirror).
type RefundResult struct {
	RefundID int64
	Amount   decimal.Decimal
	Method   string
}

func (r *Repository) InsertRefund(ctx context.Context, supplierID int64, amount decimal.Decimal, method string, reference, note *string, receivedBy int64) (int64, error) {
	var id int64
	err := r.q.GetContext(ctx, &id, `
		INSERT INTO supplier_refunds (supplier_id, amount, method, reference, note, received_by)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		supplierID, amount, method, reference, note, receivedBy)
	return id, err
}

// RefundHistory lists a supplier's refunds, newest first.
func (r *Repository) RefundHistory(ctx context.Context, supplierID int64) ([]Refund, error) {
	var rows []Refund
	err := r.q.SelectContext(ctx, &rows, `
		SELECT sr.*, u.name AS received_by_name
		FROM supplier_refunds sr
		LEFT JOIN users u ON u.id = sr.received_by
		WHERE sr.supplier_id = $1
		ORDER BY sr.created_at DESC`, supplierID)
	return rows, err
}

// RefundHistory lists a supplier's recorded refunds, newest first.
func (s *Service) RefundHistory(ctx context.Context, supplierID int64) ([]Refund, error) {
	rows, err := s.repo.RefundHistory(ctx, supplierID)
	if err != nil {
		return nil, apperr.Internal("failed to load refund history", err)
	}
	return rows, nil
}

// Credit is what a supplier currently owes us: the positive reading of a negative
// outstanding_balance. Zero when the balance is a normal payable.
func Credit(balance decimal.Decimal) decimal.Decimal {
	if balance.IsNegative() {
		return balance.Neg()
	}
	return decimal.Zero
}

// RefundTx records money received back from a supplier over the caller's
// transaction, so the matching cash move (cashflow.MoveTx) commits with it.
//
// It is capped at the credit the supplier actually owes. Without that ceiling a
// refund would invent money: the balance would swing positive and the shop would
// then appear to owe the supplier for cash it had just taken in. This is the
// mirror of the overpay guard on payments.
func (s *Service) RefundTx(ctx context.Context, tx *sqlx.Tx, supplierID int64, in RefundInput, userID int64) (*RefundResult, error) {
	method, ok := normMethod(in.Method)
	if !ok {
		return nil, apperr.Validation("refund method must be cash, card or online")
	}
	if !in.Amount.IsPositive() {
		return nil, apperr.Validation("refund amount must be greater than zero")
	}

	supRepo := suppliers.NewRepository(tx)
	sup, err := supRepo.FindByID(ctx, supplierID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("supplier")
		}
		return nil, apperr.Internal("failed to load supplier", err)
	}

	credit := Credit(sup.OutstandingBalance)
	if !credit.IsPositive() {
		return nil, apperr.Validation("this supplier owes you nothing to refund")
	}
	if in.Amount.GreaterThan(credit) {
		return nil, apperr.Conflict("that is more than the " + credit.String() + " this supplier owes you")
	}

	refundID, err := s.repo.InsertRefund(ctx, supplierID, in.Amount, method,
		strOrNil(in.Reference), strOrNil(in.Note), userID)
	if err != nil {
		return nil, apperr.Internal("failed to record refund", err)
	}
	// Taking the cash back settles the credit: the balance walks toward zero.
	if err := supRepo.AddBalance(ctx, supplierID, in.Amount); err != nil {
		return nil, apperr.Internal("failed to update supplier balance", err)
	}
	return &RefundResult{RefundID: refundID, Amount: in.Amount, Method: method}, nil
}

// Refund is RefundTx in its own transaction (no cash leg — callers that move cash
// use RefundTx inside their own tx).
func (s *Service) Refund(ctx context.Context, supplierID int64, in RefundInput, userID int64) (*RefundResult, error) {
	var res *RefundResult
	err := appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		r, txErr := s.RefundTx(ctx, tx, supplierID, in, userID)
		res = r
		return txErr
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}
