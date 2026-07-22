package supplierpay

import (
	"context"
	"os"
	"testing"

	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/purchases"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// The defect this pins: a pool row (a payment, a debit note) only knows its own
// amount minus what it has been allocated to invoices — it cannot see that the
// supplier later handed the money back. Trusting it let a refunded advance go on
// paying invoices, so the aggregate balance said money was owed while every
// invoice read "paid" and dropped off the payment queue, where it could never be
// paid. It compounded with each delivery.
//
// Everything runs in a transaction that is rolled back.

type settleFixture struct {
	svc        *Service
	supplierID int64
}

func newSettleFixture(t *testing.T, ctx context.Context, tx *sqlx.Tx, db *sqlx.DB) settleFixture {
	t.Helper()
	var supplierID int64
	mustS(t, tx.GetContext(ctx, &supplierID,
		`INSERT INTO suppliers (name) VALUES ('TEST settle supplier') RETURNING id`))
	return settleFixture{svc: NewService(db), supplierID: supplierID}
}

// payAdvance records cash paid with nothing to allocate it to, and moves the
// supplier balance the way PayTx would.
func payAdvance(t *testing.T, ctx context.Context, tx *sqlx.Tx, supplierID int64, amount string) {
	t.Helper()
	amt := decimal.RequireFromString(amount)
	_, err := NewRepository(tx).InsertPayment(ctx, supplierID, amt, "online", nil, nil, 1)
	mustS(t, err)
	_, err = tx.ExecContext(ctx,
		`UPDATE suppliers SET outstanding_balance = outstanding_balance - $1 WHERE id = $2`, amt, supplierID)
	mustS(t, err)
}

// bookInvoice books a received purchase the way applyReceivedLines does: the
// invoice row plus its effect on the supplier's balance.
func bookInvoice(t *testing.T, ctx context.Context, tx *sqlx.Tx, supplierID int64, total string) int64 {
	t.Helper()
	amt := decimal.RequireFromString(total)
	var id int64
	mustS(t, tx.GetContext(ctx, &id, `
		INSERT INTO purchases (supplier_id, status, subtotal, discount, total, paid_amount)
		VALUES ($1,'received',$2,0,$2,0) RETURNING id`, supplierID, amt))
	_, err := tx.ExecContext(ctx,
		`UPDATE suppliers SET outstanding_balance = outstanding_balance + $1 WHERE id = $2`, amt, supplierID)
	mustS(t, err)
	return id
}

func owed(t *testing.T, ctx context.Context, tx *sqlx.Tx, purchaseID int64) decimal.Decimal {
	t.Helper()
	pu, err := purchases.NewRepository(tx).FindByID(ctx, purchaseID)
	mustS(t, err)
	return pu.Balance()
}

// An ordinary advance must still settle the next invoice in full.
func TestAdvanceSettlesTheNextInvoice(t *testing.T) {
	db := settleTestDB(t)
	defer db.Close()
	ctx := context.Background()
	tx, err := db.BeginTxx(ctx, nil)
	mustS(t, err)
	defer tx.Rollback() //nolint:errcheck

	f := newSettleFixture(t, ctx, tx, db)
	payAdvance(t, ctx, tx, f.supplierID, "5000")
	id := bookInvoice(t, ctx, tx, f.supplierID, "3000")

	mustS(t, f.svc.ApplySupplierCreditTx(ctx, tx, f.supplierID, id))
	if got := owed(t, ctx, tx, id); !got.IsZero() {
		t.Errorf("invoice still owes %s, want 0 — the advance should have covered it", got)
	}
}

// THE money test. Money handed back must stop paying invoices.
func TestRefundedAdvanceStopsPayingInvoices(t *testing.T) {
	db := settleTestDB(t)
	defer db.Close()
	ctx := context.Background()
	tx, err := db.BeginTxx(ctx, nil)
	mustS(t, err)
	defer tx.Rollback() //nolint:errcheck

	f := newSettleFixture(t, ctx, tx, db)
	payAdvance(t, ctx, tx, f.supplierID, "5000")
	// The supplier hands Rs 2,000 of it back: only Rs 3,000 is still on hand.
	if _, err := f.svc.RefundTx(ctx, tx, f.supplierID,
		RefundInput{Amount: decimal.NewFromInt(2000), Method: "cash"}, 1); err != nil {
		t.Fatalf("refund: %v", err)
	}

	id := bookInvoice(t, ctx, tx, f.supplierID, "4000")
	mustS(t, f.svc.ApplySupplierCreditTx(ctx, tx, f.supplierID, id))

	// Only the Rs 3,000 genuinely on hand may be applied, leaving Rs 1,000 owed.
	if got := owed(t, ctx, tx, id); !got.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("invoice owes %s, want 1000 — refunded money paid an invoice", got)
	}

	// And the two ledgers must agree, or the invoice drops off the payment queue
	// carrying a debt the aggregate still reports.
	var balance decimal.Decimal
	mustS(t, tx.GetContext(ctx, &balance,
		`SELECT outstanding_balance FROM suppliers WHERE id=$1`, f.supplierID))
	if !balance.Equal(owed(t, ctx, tx, id)) {
		t.Errorf("aggregate says %s owed but invoices say %s", balance, owed(t, ctx, tx, id))
	}
}

// The failure compounded: each later delivery spent more of the phantom.
func TestPhantomCreditDoesNotCompoundAcrossDeliveries(t *testing.T) {
	db := settleTestDB(t)
	defer db.Close()
	ctx := context.Background()
	tx, err := db.BeginTxx(ctx, nil)
	mustS(t, err)
	defer tx.Rollback() //nolint:errcheck

	f := newSettleFixture(t, ctx, tx, db)
	payAdvance(t, ctx, tx, f.supplierID, "5000")
	if _, err := f.svc.RefundTx(ctx, tx, f.supplierID,
		RefundInput{Amount: decimal.NewFromInt(2000), Method: "cash"}, 1); err != nil {
		t.Fatal(err)
	}
	first := bookInvoice(t, ctx, tx, f.supplierID, "4000")
	mustS(t, f.svc.ApplySupplierCreditTx(ctx, tx, f.supplierID, first))

	// Nothing is left on hand, so this one must be owed in full.
	second := bookInvoice(t, ctx, tx, f.supplierID, "1000")
	mustS(t, f.svc.ApplySupplierCreditTx(ctx, tx, f.supplierID, second))
	if got := owed(t, ctx, tx, second); !got.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("second invoice owes %s, want 1000 — the phantom compounded", got)
	}

	var balance decimal.Decimal
	mustS(t, tx.GetContext(ctx, &balance,
		`SELECT outstanding_balance FROM suppliers WHERE id=$1`, f.supplierID))
	total := owed(t, ctx, tx, first).Add(owed(t, ctx, tx, second))
	if !balance.Equal(total) {
		t.Errorf("aggregate says %s owed but invoices total %s", balance, total)
	}
}

func settleTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	conn, err := appdb.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func mustS(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
