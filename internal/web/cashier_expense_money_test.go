package web

import (
	"context"
	"testing"

	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/expenses"

	"github.com/shopspring/decimal"
)

// TestCashierExpenseMovesMoney books an expense and its cash-out in the one
// transaction the cashier handler uses, then asserts the expense row, the CR-
// receipt, and the locker debit all reconcile. Everything runs in a rolled-back
// transaction, so the dev database is untouched. (testDB, must and
// seedLockerBalance are shared with supplier_money_test.go.)
func TestCashierExpenseMovesMoney(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()

	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // the whole point: leave no trace

	var lockerID int64
	must(t, tx.GetContext(ctx, &lockerID,
		`INSERT INTO lockers (name, kind) VALUES ('TEST expense safe', 'safe') RETURNING id`))
	must(t, seedLockerBalance(ctx, tx, lockerID, decimal.NewFromInt(1000)))

	// Act: the exact composition ExpenseRecord performs inside its tx.
	exp := expenses.NewService(conn)
	e, err := exp.CreateInTx(ctx, tx, expenses.CreateInput{
		Category: "TEST electricity", Amount: "250",
	}, 1)
	if err != nil {
		t.Fatalf("creating the expense: %v", err)
	}

	mover := cashflow.NewService(conn, nil)
	rec, err := mover.MoveTx(ctx, tx, cashflow.MoveInput{
		From:        cashflow.Locker(lockerID),
		To:          cashflow.External(),
		Amount:      e.Amount,
		Reason:      "TEST electricity",
		ReceiptKind: "expense",
		Ref:         &cashflow.Ref{Kind: "expense", ID: e.ID},
		ActorID:     1,
	})
	if err != nil {
		t.Fatalf("moving the cash: %v", err)
	}

	// The receipt bills exactly the expense amount.
	if !rec.Amount.Equal(decimal.NewFromInt(250)) {
		t.Errorf("receipt amount = %s, want 250", rec.Amount)
	}

	// The expense row exists and is what we booked.
	var got int
	must(t, tx.GetContext(ctx, &got,
		`SELECT count(*) FROM expenses WHERE id = $1 AND amount = 250`, e.ID))
	if got != 1 {
		t.Errorf("expenses rows = %d, want 1", got)
	}

	// A CR- money receipt was recorded for this move.
	var receipts int
	must(t, tx.GetContext(ctx, &receipts, `SELECT count(*) FROM money_receipts WHERE id = $1`, rec.ID))
	if receipts != 1 {
		t.Errorf("money_receipts rows = %d, want 1", receipts)
	}

	// The cash genuinely left the locker: ledger delta == -amount.
	var delta decimal.Decimal
	must(t, tx.GetContext(ctx, &delta,
		`SELECT COALESCE(SUM(balance_delta),0) FROM locker_ledger
		 WHERE locker_id = $1 AND ref_kind = 'expense'`, lockerID))
	if !delta.Equal(e.Amount.Neg()) {
		t.Errorf("locker moved by %s, want %s — the cash must actually leave", delta, e.Amount.Neg())
	}
}
