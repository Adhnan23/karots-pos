package purchases

import (
	"context"
	"os"
	"testing"

	appdb "karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// The defect these pin: returning goods used to adjust only the supplier's
// aggregate balance, leaving the invoice itself sitting on the open-payment queue
// at its full amount. The shop could then hand over cash for stock it had already
// sent back, and the supplier balance went negative with no way to recover it.
//
// Everything runs in a transaction that is rolled back, so the dev database is
// untouched.

func seedInvoice(t *testing.T, ctx context.Context, tx *sqlx.Tx, total string) (*Repository, int64, int64) {
	t.Helper()
	repo := NewRepository(tx)
	var supplierID, purchaseID int64
	must(t, tx.GetContext(ctx, &supplierID,
		`INSERT INTO suppliers (name) VALUES ('TEST credit supplier') RETURNING id`))
	must(t, tx.GetContext(ctx, &purchaseID, `
		INSERT INTO purchases (supplier_id, status, subtotal, discount, total, paid_amount)
		VALUES ($1, 'received', $2, 0, $2, 0) RETURNING id`, supplierID, total))
	return repo, supplierID, purchaseID
}

// Crediting an invoice must reduce what it still owes.
func TestApplyCreditReducesWhatIsOwed(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	repo, supplierID, purchaseID := seedInvoice(t, ctx, tx, "2000")

	applied, err := repo.ApplyCredit(ctx, purchaseID, decimal.NewFromInt(1300))
	must(t, err)
	if !applied.Equal(decimal.NewFromInt(1300)) {
		t.Fatalf("applied %s, want 1300", applied)
	}

	open, err := repo.OpenBySupplier(ctx, supplierID)
	must(t, err)
	if len(open) != 1 {
		t.Fatalf("got %d open invoices, want 1", len(open))
	}
	if got := open[0].Balance(); !got.Equal(decimal.NewFromInt(700)) {
		t.Errorf("still owed %s, want 700 (2000 less the 1300 returned)", got)
	}
}

// THE money test. Paying more than an invoice owes AFTER a credit must be
// refused — that payment is cash handed over for returned goods.
func TestPaymentCannotExceedWhatIsOwedAfterACredit(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	repo, _, purchaseID := seedInvoice(t, ctx, tx, "2000")
	if _, err := repo.ApplyCredit(ctx, purchaseID, decimal.NewFromInt(1300)); err != nil {
		t.Fatal(err)
	}

	// The old total was 2000, so this is exactly the payment that used to sail
	// through and drive the supplier balance to -1300.
	advanced, err := repo.ApplyPayment(ctx, purchaseID, decimal.NewFromInt(2000))
	must(t, err)
	if advanced {
		t.Error("paying the pre-credit total was allowed — that is cash paid for returned goods")
	}
	// What is genuinely still owed must still be payable.
	advanced, err = repo.ApplyPayment(ctx, purchaseID, decimal.NewFromInt(700))
	must(t, err)
	if !advanced {
		t.Error("paying the real remaining balance was refused")
	}
}

// A credit can never drive an invoice below zero; the excess is the caller's to
// carry as a supplier credit instead.
func TestApplyCreditIsCappedAtTheInvoiceBalance(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	repo, _, purchaseID := seedInvoice(t, ctx, tx, "500")

	applied, err := repo.ApplyCredit(ctx, purchaseID, decimal.NewFromInt(900))
	must(t, err)
	if !applied.Equal(decimal.NewFromInt(500)) {
		t.Errorf("applied %s, want the invoice's 500 (the other 400 is a supplier credit)", applied)
	}
	// A second credit finds no headroom left.
	applied, err = repo.ApplyCredit(ctx, purchaseID, decimal.NewFromInt(100))
	must(t, err)
	if !applied.IsZero() {
		t.Errorf("applied %s to a fully credited invoice, want 0", applied)
	}
}

// A fully credited invoice must drop off the payment queue entirely.
func TestFullyCreditedInvoiceLeavesTheOpenQueue(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	repo, supplierID, purchaseID := seedInvoice(t, ctx, tx, "500")
	if _, err := repo.ApplyCredit(ctx, purchaseID, decimal.NewFromInt(500)); err != nil {
		t.Fatal(err)
	}
	open, err := repo.OpenBySupplier(ctx, supplierID)
	must(t, err)
	if len(open) != 0 {
		t.Errorf("a fully returned invoice is still listed as open (%d) — it would be paid again", len(open))
	}
}

func testDB(t *testing.T) *sqlx.DB {
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

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
