// Package supplierpay settles supplier payables: a payment is split across the
// specific purchase invoices it pays, advancing each purchase's
// paid_amount/status, recording the payment + allocations for history, and
// decrementing the supplier's aggregate balance — all in one transaction.
//
// It lives in its own package (not suppliers/purchases) because it coordinates
// both: purchases already imports suppliers, so the cross-table flow can't live
// in either without an import cycle. The cash-drawer impact is wired by the web
// layer (mirroring the customer-credit flow), so this package stays free of the
// cashregister dependency.
package supplierpay

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/purchases"
	"karots-pos/internal/features/suppliers"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// Payment is a recorded supplier payment (history row).
type Payment struct {
	ID         int64           `db:"id"          json:"id"`
	SupplierID int64           `db:"supplier_id" json:"supplier_id"`
	Amount     decimal.Decimal `db:"amount"      json:"amount"`
	Method     string          `db:"method"      json:"method"`
	Reference  *string         `db:"reference"   json:"reference,omitempty"`
	Note       *string         `db:"note"        json:"note,omitempty"`
	PaidBy     *int64          `db:"paid_by"     json:"paid_by,omitempty"`
	CreatedAt  time.Time       `db:"created_at"  json:"created_at"`
	// joined
	PaidByName *string `db:"paid_by_name" json:"paid_by_name,omitempty"`
}

// Alloc applies part of a payment to one purchase invoice.
type Alloc struct {
	PurchaseID int64
	Amount     decimal.Decimal
}

// PayInput is one supplier payment: per-invoice allocations, plus an optional
// unallocated amount (for suppliers carrying a balance with no open invoices).
type PayInput struct {
	Method      string
	Reference   string
	Note        string
	Allocations []Alloc
	Unallocated decimal.Decimal
}

// Result summarises a recorded payment for the caller (e.g. the cash mirror).
type Result struct {
	PaymentID int64
	Total     decimal.Decimal
	Method    string
}

type Repository struct{ q appdb.Queryer }

func NewRepository(q appdb.Queryer) *Repository { return &Repository{q: q} }

func (r *Repository) InsertPayment(ctx context.Context, supplierID int64, amount decimal.Decimal, method string, reference, note *string, paidBy int64) (int64, error) {
	var id int64
	err := r.q.GetContext(ctx, &id, `
		INSERT INTO supplier_payments (supplier_id, amount, method, reference, note, paid_by)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		supplierID, amount, method, reference, note, paidBy)
	return id, err
}

func (r *Repository) InsertAllocation(ctx context.Context, paymentID, purchaseID int64, amount decimal.Decimal) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO supplier_payment_allocations (payment_id, purchase_id, amount)
		VALUES ($1,$2,$3)`, paymentID, purchaseID, amount)
	return err
}

func (r *Repository) History(ctx context.Context, supplierID int64) ([]Payment, error) {
	var rows []Payment
	err := r.q.SelectContext(ctx, &rows, `
		SELECT sp.*, u.name AS paid_by_name
		FROM supplier_payments sp
		LEFT JOIN users u ON u.id = sp.paid_by
		WHERE sp.supplier_id = $1
		ORDER BY sp.created_at DESC`, supplierID)
	return rows, err
}

type Service struct {
	db   *sqlx.DB
	repo *Repository
}

func NewService(db *sqlx.DB) *Service { return &Service{db: db, repo: NewRepository(db)} }

// OpenInvoices lists a supplier's purchases that still owe money (oldest first).
func (s *Service) OpenInvoices(ctx context.Context, supplierID int64) ([]purchases.Purchase, error) {
	rows, err := purchases.NewRepository(s.db).OpenBySupplier(ctx, supplierID)
	if err != nil {
		return nil, apperr.Internal("failed to load open invoices", err)
	}
	return rows, nil
}

// History lists a supplier's recorded payments, newest first.
func (s *Service) History(ctx context.Context, supplierID int64) ([]Payment, error) {
	rows, err := s.repo.History(ctx, supplierID)
	if err != nil {
		return nil, apperr.Internal("failed to load payment history", err)
	}
	return rows, nil
}

func normMethod(m string) (string, bool) {
	switch m {
	case "cash", "card", "online":
		return m, true
	case "":
		return "cash", true
	default:
		return "", false
	}
}

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Pay records a supplier payment in one transaction: each allocation advances
// its purchase's paid_amount/status (guarded against overpay), the payment and
// allocation rows are written, and the supplier's aggregate balance drops by the
// full amount. Returns the recorded total (for the cash-drawer mirror).
func (s *Service) Pay(ctx context.Context, supplierID int64, in PayInput, userID int64) (*Result, error) {
	var res Result
	err := appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		r, err := s.PayTx(ctx, tx, supplierID, in, userID)
		if err != nil {
			return err
		}
		res = *r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// PayTx records the supplier payment over an existing transaction, so the caller
// can book the matching cash move (cashflow.MoveTx) atomically in the same tx.
// It re-validates the method/total so it is safe to call directly.
func (s *Service) PayTx(ctx context.Context, tx *sqlx.Tx, supplierID int64, in PayInput, userID int64) (*Result, error) {
	method, ok := normMethod(in.Method)
	if !ok {
		return nil, apperr.Validation("payment method must be cash, card or online")
	}
	total := in.Unallocated
	for _, a := range in.Allocations {
		if a.Amount.IsNegative() {
			return nil, apperr.Validation("allocation amounts must not be negative")
		}
		total = total.Add(a.Amount)
	}
	if !total.IsPositive() {
		return nil, apperr.Validation("payment amount must be greater than zero")
	}

	supRepo := suppliers.NewRepository(tx)
	puRepo := purchases.NewRepository(tx)
	payRepo := NewRepository(tx)

	sup, err := supRepo.FindByID(ctx, supplierID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("supplier")
		}
		return nil, apperr.Internal("failed to load supplier", err)
	}

	paymentID, err := payRepo.InsertPayment(ctx, supplierID, total, method,
		strOrNil(in.Reference), strOrNil(in.Note), userID)
	if err != nil {
		return nil, apperr.Internal("failed to record payment", err)
	}

	for _, a := range in.Allocations {
		if !a.Amount.IsPositive() {
			continue
		}
		pu, err := puRepo.FindByID(ctx, a.PurchaseID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, apperr.Validation("invoice not found")
			}
			return nil, apperr.Internal("failed to load invoice", err)
		}
		if pu.SupplierID != supplierID {
			return nil, apperr.Validation("invoice does not belong to this supplier")
		}
		advanced, err := puRepo.ApplyPayment(ctx, a.PurchaseID, a.Amount)
		if err != nil {
			return nil, apperr.Internal("failed to apply payment to invoice", err)
		}
		if !advanced {
			return nil, apperr.Conflict("payment exceeds the balance on invoice " + invoiceLabel(pu))
		}
		if err := payRepo.InsertAllocation(ctx, paymentID, a.PurchaseID, a.Amount); err != nil {
			return nil, apperr.Internal("failed to record allocation", err)
		}
	}

	// Drop the supplier's aggregate payable by the full amount paid.
	if err := supRepo.AddBalance(ctx, sup.ID, total.Neg()); err != nil {
		return nil, apperr.Internal("failed to update supplier balance", err)
	}

	return &Result{PaymentID: paymentID, Total: total, Method: method}, nil
}

func invoiceLabel(p *purchases.Purchase) string {
	if p.InvoiceNo != nil && *p.InvoiceNo != "" {
		return *p.InvoiceNo
	}
	return "#" + decimal.NewFromInt(p.ID).String()
}
