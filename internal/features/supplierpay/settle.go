package supplierpay

import (
	"context"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/purchasereturns"
	"karots-pos/internal/features/purchases"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// pool is one unspent source of supplier credit: a payment whose money has not
// all been assigned to invoices yet (an advance), or a debit note whose value has
// not all been credited yet (goods returned against nothing).
type pool struct {
	ID        int64           `db:"id"`
	Remaining decimal.Decimal `db:"remaining"`
}

// unappliedAdvances lists payments carrying money not yet assigned to any
// invoice, oldest first. That is exactly what an advance is: cash handed over
// before there was an invoice to put it against.
func unappliedAdvances(ctx context.Context, tx *sqlx.Tx, supplierID int64) ([]pool, error) {
	var rows []pool
	err := tx.SelectContext(ctx, &rows, `
		SELECT sp.id,
		       sp.amount - COALESCE((SELECT SUM(a.amount) FROM supplier_payment_allocations a
		                             WHERE a.payment_id = sp.id), 0) AS remaining
		FROM supplier_payments sp
		WHERE sp.supplier_id = $1
		  AND sp.amount > COALESCE((SELECT SUM(a.amount) FROM supplier_payment_allocations a
		                            WHERE a.payment_id = sp.id), 0)
		ORDER BY sp.created_at, sp.id`, supplierID)
	return rows, err
}

// unappliedReturnCredits lists debit notes whose value has not all been credited
// to an invoice — goods sent back when nothing was owed.
func unappliedReturnCredits(ctx context.Context, tx *sqlx.Tx, supplierID int64) ([]pool, error) {
	var rows []pool
	err := tx.SelectContext(ctx, &rows, `
		SELECT pr.id,
		       pr.total - COALESCE((SELECT SUM(a.amount) FROM purchase_return_allocations a
		                            WHERE a.purchase_return_id = pr.id), 0) AS remaining
		FROM purchase_returns pr
		WHERE pr.supplier_id = $1
		  AND pr.total > COALESCE((SELECT SUM(a.amount) FROM purchase_return_allocations a
		                           WHERE a.purchase_return_id = pr.id), 0)
		ORDER BY pr.created_at, pr.id`, supplierID)
	return rows, err
}

// ApplySupplierCreditTx settles a freshly booked invoice out of whatever the
// supplier is already holding for us — advances paid before the goods arrived,
// and value from returned goods.
//
// Without this an advance would re-open the very hole that crediting returns
// closed: the supplier's aggregate balance would net the advance while the new
// invoice sat at its full amount on the payment queue, and the shop would pay a
// second time for goods it had already funded.
//
// Advances go first because that money genuinely left the till and should be
// recognised as paid; return value follows as a credit, which moves no cash.
// Both are drawn oldest-first, matching how payments allocate.
func (s *Service) ApplySupplierCreditTx(ctx context.Context, tx *sqlx.Tx, supplierID, purchaseID int64) error {
	puRepo := purchases.NewRepository(tx)
	payRepo := NewRepository(tx)
	retRepo := purchasereturns.NewRepository(tx)

	pu, err := puRepo.FindByID(ctx, purchaseID)
	if err != nil {
		return apperr.Internal("failed to load the invoice", err)
	}
	left := pu.Balance()
	if !left.IsPositive() {
		return nil
	}

	advances, err := unappliedAdvances(ctx, tx, supplierID)
	if err != nil {
		return apperr.Internal("failed to read supplier advances", err)
	}
	for _, p := range advances {
		if !left.IsPositive() {
			break
		}
		take := decimal.Min(p.Remaining, left)
		if !take.IsPositive() {
			continue
		}
		ok, aerr := puRepo.ApplyPayment(ctx, purchaseID, take)
		if aerr != nil {
			return apperr.Internal("failed to apply an advance", aerr)
		}
		if !ok {
			break
		}
		if aerr := payRepo.InsertAllocation(ctx, p.ID, purchaseID, take); aerr != nil {
			return apperr.Internal("failed to record the advance allocation", aerr)
		}
		left = left.Sub(take)
	}

	credits, err := unappliedReturnCredits(ctx, tx, supplierID)
	if err != nil {
		return apperr.Internal("failed to read supplier credits", err)
	}
	for _, p := range credits {
		if !left.IsPositive() {
			break
		}
		take := decimal.Min(p.Remaining, left)
		if !take.IsPositive() {
			continue
		}
		got, aerr := puRepo.ApplyCredit(ctx, purchaseID, take)
		if aerr != nil {
			return apperr.Internal("failed to apply a return credit", aerr)
		}
		if !got.IsPositive() {
			break
		}
		if aerr := retRepo.InsertAllocation(ctx, p.ID, purchaseID, got); aerr != nil {
			return apperr.Internal("failed to record the credit allocation", aerr)
		}
		left = left.Sub(got)
	}
	return nil
}
