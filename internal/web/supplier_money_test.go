package web

import (
	"context"
	"os"
	"testing"

	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/purchases"
	"karots-pos/internal/features/supplierpay"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// TestReceiveAndPayMovesMoney is the assertion that would have caught the
// defect this work exists to fix: receiving with a payment used to mark the
// invoice paid and clear the supplier's balance while producing no payment
// record, no receipt, and no cash leaving any drawer.
//
// Everything happens inside a transaction that is rolled back, so the dev
// database is untouched.
func TestReceiveAndPayMovesMoney(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()

	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // the whole point: leave no trace

	var supplierID int64
	must(t, tx.GetContext(ctx, &supplierID,
		`INSERT INTO suppliers (name) VALUES ('TEST money trail') RETURNING id`))
	var productID int64
	productID = testProduct(t, ctx, tx)
	var lockerID int64
	must(t, tx.GetContext(ctx, &lockerID,
		`INSERT INTO lockers (name, kind) VALUES ('TEST safe', 'safe') RETURNING id`))
	must(t, seedLockerBalance(ctx, tx, lockerID, decimal.NewFromInt(5000)))

	// Act: receive 10 @ 100, then pay the whole 1000 out of the locker.
	detail, err := purchases.CreateTx(ctx, tx, purchases.CreateInput{
		SupplierID: supplierID,
		Discount:   "0",
		Items: []purchases.ItemInput{{
			ProductID: productID, Quantity: "10", CostPrice: "100", SellingPrice: "150",
		}},
	}, 1)
	if err != nil {
		t.Fatalf("receiving the delivery: %v", err)
	}
	total := detail.Purchase.Total

	// PayTx and MoveTx are methods, but both take the transaction explicitly and
	// neither reads the service's own db handle on this path. cashflow's sales
	// dependency is unused here, hence nil — a test-only shortcut.
	payer := supplierpay.NewService(conn)
	mover := cashflow.NewService(conn, nil)

	res, err := payer.PayTx(ctx, tx, supplierID, supplierpay.PayInput{
		Method:      "cash",
		Allocations: []supplierpay.Alloc{{PurchaseID: detail.Purchase.ID, Amount: total}},
	}, 1)
	if err != nil {
		t.Fatalf("paying: %v", err)
	}
	rec, err := mover.MoveTx(ctx, tx, cashflow.MoveInput{
		From:        cashflow.Locker(lockerID),
		To:          cashflow.External(),
		Amount:      res.Total,
		Reason:      "supplier payment: TEST money trail",
		ReceiptKind: "supplier_payment",
		Party:       "TEST money trail",
		Ref:         &cashflow.Ref{Kind: "supplier_payment", ID: res.PaymentID},
		ActorID:     1,
	})
	if err != nil {
		t.Fatalf("moving the cash: %v", err)
	}

	// Assert: every one of these read zero before this work.
	var payments int
	must(t, tx.GetContext(ctx, &payments,
		`SELECT count(*) FROM supplier_payments WHERE supplier_id = $1`, supplierID))
	if payments != 1 {
		t.Errorf("supplier_payments rows = %d, want 1", payments)
	}

	var receipts int
	must(t, tx.GetContext(ctx, &receipts, `SELECT count(*) FROM money_receipts WHERE id = $1`, rec.ID))
	if receipts != 1 {
		t.Errorf("money_receipts rows = %d, want 1", receipts)
	}

	var lockerDelta decimal.Decimal
	must(t, tx.GetContext(ctx, &lockerDelta,
		`SELECT COALESCE(SUM(balance_delta),0) FROM locker_ledger
		 WHERE locker_id = $1 AND ref_kind = 'supplier_payment'`, lockerID))
	if !lockerDelta.Equal(total.Neg()) {
		t.Errorf("locker moved by %s, want %s — the cash must actually leave", lockerDelta, total.Neg())
	}

	var paid, owed decimal.Decimal
	must(t, tx.GetContext(ctx, &paid, `SELECT paid_amount FROM purchases WHERE id = $1`, detail.Purchase.ID))
	must(t, tx.GetContext(ctx, &owed, `SELECT outstanding_balance FROM suppliers WHERE id = $1`, supplierID))
	if !paid.Equal(total) {
		t.Errorf("purchase paid_amount = %s, want %s", paid, total)
	}
	if !owed.IsZero() {
		t.Errorf("supplier still owed %s, want 0", owed)
	}
}

// TestReceiveWithoutPayingOwesEverything is the other half: receiving alone
// must leave the full amount owed and move nothing.
func TestReceiveWithoutPayingOwesEverything(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()

	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck

	var supplierID int64
	must(t, tx.GetContext(ctx, &supplierID,
		`INSERT INTO suppliers (name) VALUES ('TEST unpaid') RETURNING id`))
	var productID int64
	productID = testProduct(t, ctx, tx)

	detail, err := purchases.CreateTx(ctx, tx, purchases.CreateInput{
		SupplierID: supplierID,
		Discount:   "0",
		Items: []purchases.ItemInput{{
			ProductID: productID, Quantity: "4", CostPrice: "25", SellingPrice: "40",
		}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}

	var owed decimal.Decimal
	must(t, tx.GetContext(ctx, &owed, `SELECT outstanding_balance FROM suppliers WHERE id = $1`, supplierID))
	if !owed.Equal(detail.Purchase.Total) {
		t.Errorf("owed %s after receiving, want the full %s", owed, detail.Purchase.Total)
	}

	var payments int
	must(t, tx.GetContext(ctx, &payments,
		`SELECT count(*) FROM supplier_payments WHERE supplier_id = $1`, supplierID))
	if payments != 0 {
		t.Errorf("receiving alone created %d payments, want 0", payments)
	}
}

// testDB opens the dev database, skipping the test when there isn't one.
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

// seedLockerBalance gives a fresh locker an opening balance to pay out of.
//
// The kind must be 'open_balance': locker_ledger_kind_check allows only
// open_balance, transfer, payment, intake, bank_charge, interest and adjust.
func seedLockerBalance(ctx context.Context, tx *sqlx.Tx, lockerID int64, amount decimal.Decimal) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO locker_ledger (locker_id, balance_delta, kind, note)
		 VALUES ($1, $2, 'open_balance', 'test seed')`, lockerID, amount)
	return err
}

// testProduct creates a product for a test to buy and sell.
//
// These tests used to borrow whatever product happened to be in the database,
// which quietly tied the suite to a seeded catalogue: against a freshly
// initialised shop — exactly what a real install starts as — they failed with
// "sql: no rows in result set" and looked like a broken money trail rather than
// a missing fixture.
func testProduct(t *testing.T, ctx context.Context, tx *sqlx.Tx) int64 {
	t.Helper()
	var categoryID, unitID, productID int64
	must(t, tx.GetContext(ctx, &categoryID,
		`INSERT INTO categories (name) VALUES ('TEST money category') RETURNING id`))
	if err := tx.GetContext(ctx, &unitID, `SELECT id FROM units LIMIT 1`); err != nil {
		must(t, tx.GetContext(ctx, &unitID,
			`INSERT INTO units (name, abbreviation) VALUES ('TEST unit','tu') RETURNING id`))
	}
	must(t, tx.GetContext(ctx, &productID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price)
		VALUES ('TEST money product', $1, $2, 100, 150) RETURNING id`, categoryID, unitID))
	return productID
}
