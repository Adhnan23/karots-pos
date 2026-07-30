package recharge

import (
	"context"
	"os"
	"testing"

	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/cashflow"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// These tests exercise the exact DB path adminUI.BankTx runs — buildBankLegs fed
// through cashflow.MoveTx — against the real schema, entirely inside a rolled-back
// transaction so the dev database is left untouched. They are skipped unless
// DATABASE_URL is set. (Mirrors internal/web/supplier_money_test.go.)

func rechargeTestDB(t *testing.T) *sqlx.DB {
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

func mustR(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// seedLocker inserts a locker and an opening balance, returning its id.
func seedLocker(t *testing.T, ctx context.Context, tx *sqlx.Tx, name, kind string, allowNeg bool, opening decimal.Decimal) int64 {
	t.Helper()
	var id int64
	mustR(t, tx.GetContext(ctx, &id,
		`INSERT INTO lockers (name, kind, allow_negative) VALUES ($1,$2,$3) RETURNING id`,
		name, kind, allowNeg))
	if !opening.IsZero() {
		mustR(t, func() error {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO locker_ledger (locker_id, balance_delta, kind, note)
				 VALUES ($1,$2,'open_balance','test seed')`, id, opening)
			return err
		}())
	}
	return id
}

// lockerBalance sums the locker's ledger deltas within the test tx.
func lockerBalance(t *testing.T, ctx context.Context, tx *sqlx.Tx, id int64) decimal.Decimal {
	t.Helper()
	var b decimal.Decimal
	mustR(t, tx.GetContext(ctx, &b,
		`SELECT COALESCE(SUM(balance_delta),0) FROM locker_ledger WHERE locker_id = $1`, id))
	return b
}

// runLegs applies buildBankLegs through the real cashflow mover, exactly as the
// handler does (minus the HTTP layer). Returns the first error.
func runLegs(ctx context.Context, tx *sqlx.Tx, mover *cashflow.Service, legs []bankLeg) error {
	for _, l := range legs {
		if _, err := mover.MoveTx(ctx, tx, cashflow.MoveInput{
			From: l.From, To: l.To, Amount: l.Amount, Reason: "TEST bill",
			ReceiptKind: "billpay", Party: l.Party, ActorID: 1,
		}); err != nil {
			return err
		}
	}
	return nil
}

func TestAdminBillPayMovesMoney(t *testing.T) {
	conn := rechargeTestDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	mustR(t, err)
	defer tx.Rollback() //nolint:errcheck // leave no trace

	bank := seedLocker(t, ctx, tx, "TEST BOC", "bank", false, decimal.NewFromInt(1000))
	safe := seedLocker(t, ctx, tx, "TEST Safe", "safe", false, decimal.Zero)
	mover := cashflow.NewService(conn, nil)

	// billpay: bank pays biller 100 (down), 120 cash (100 + 20 svc) into the safe.
	legs := buildBankLegs("billpay", cashflow.Locker(bank), cashflow.Locker(safe),
		decimal.NewFromInt(100), decimal.NewFromInt(20), "Bill T")
	mustR(t, runLegs(ctx, tx, mover, legs))

	if got := lockerBalance(t, ctx, tx, bank); !got.Equal(decimal.NewFromInt(900)) {
		t.Errorf("bank after billpay = %s, want 900", got)
	}
	if got := lockerBalance(t, ctx, tx, safe); !got.Equal(decimal.NewFromInt(120)) {
		t.Errorf("safe after billpay = %s, want 120", got)
	}
}

func TestAdminGetMoneyMovesMoney(t *testing.T) {
	conn := rechargeTestDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	mustR(t, err)
	defer tx.Rollback() //nolint:errcheck

	bank := seedLocker(t, ctx, tx, "TEST BOC", "bank", false, decimal.NewFromInt(1000))
	safe := seedLocker(t, ctx, tx, "TEST Safe", "safe", false, decimal.NewFromInt(500))
	mover := cashflow.NewService(conn, nil)

	// getmoney: safe pays customer 100 (down), bank receives 100 (up), 20 svc cash
	// back into the safe. Net: bank +100, safe -80.
	legs := buildBankLegs("getmoney", cashflow.Locker(bank), cashflow.Locker(safe),
		decimal.NewFromInt(100), decimal.NewFromInt(20), "Bill T")
	mustR(t, runLegs(ctx, tx, mover, legs))

	if got := lockerBalance(t, ctx, tx, bank); !got.Equal(decimal.NewFromInt(1100)) {
		t.Errorf("bank after getmoney = %s, want 1100", got)
	}
	if got := lockerBalance(t, ctx, tx, safe); !got.Equal(decimal.NewFromInt(420)) {
		t.Errorf("safe after getmoney = %s, want 420", got)
	}
}

func TestAdminBillPayOverdrawGuard(t *testing.T) {
	conn := rechargeTestDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	mustR(t, err)
	defer tx.Rollback() //nolint:errcheck

	bank := seedLocker(t, ctx, tx, "TEST BOC", "bank", false, decimal.NewFromInt(50))
	safe := seedLocker(t, ctx, tx, "TEST Safe", "safe", false, decimal.Zero)
	mover := cashflow.NewService(conn, nil)

	// The guarded leg (bank down) is first, so an over-balance amount errors before
	// any money moves — the whole thing is refused.
	legs := buildBankLegs("billpay", cashflow.Locker(bank), cashflow.Locker(safe),
		decimal.NewFromInt(100), decimal.Zero, "Bill T")
	if err := runLegs(ctx, tx, mover, legs); err == nil {
		t.Fatal("expected an overdraw error paying 100 from a 50 bank, got nil")
	}
	if got := lockerBalance(t, ctx, tx, bank); !got.Equal(decimal.NewFromInt(50)) {
		t.Errorf("bank moved despite overdraw: %s, want 50", got)
	}
	if got := lockerBalance(t, ctx, tx, safe); !got.Equal(decimal.Zero) {
		t.Errorf("safe moved despite overdraw: %s, want 0", got)
	}
}

func TestAdminNegativeAllowedBankMayGoBelowZero(t *testing.T) {
	conn := rechargeTestDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	mustR(t, err)
	defer tx.Rollback() //nolint:errcheck

	// A negative-allowed bank (e.g. an owner "pocket" account) is NOT blocked.
	bank := seedLocker(t, ctx, tx, "TEST Pocket", "bank", true, decimal.NewFromInt(50))
	safe := seedLocker(t, ctx, tx, "TEST Safe", "safe", false, decimal.Zero)
	mover := cashflow.NewService(conn, nil)

	legs := buildBankLegs("billpay", cashflow.Locker(bank), cashflow.Locker(safe),
		decimal.NewFromInt(100), decimal.Zero, "Bill T")
	mustR(t, runLegs(ctx, tx, mover, legs))

	if got := lockerBalance(t, ctx, tx, bank); !got.Equal(decimal.NewFromInt(-50)) {
		t.Errorf("negative-allowed bank = %s, want -50", got)
	}
}

// TestAdminBillTxRowIsSessionless proves the admin shape the handler records:
// a recharge_bill_tx row with a NULL session and an FK-free bank_locker_id (0 when
// the account side is a till). Written and read back inside the rolled-back tx.
func TestAdminBillTxRowIsSessionless(t *testing.T) {
	conn := rechargeTestDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	mustR(t, err)
	defer tx.Rollback() //nolint:errcheck

	var id int64
	mustR(t, tx.GetContext(ctx, &id,
		`INSERT INTO recharge_bill_tx
		   (session_id, bank_locker_id, bank_name, type, amount, service_charge, created_by)
		 VALUES (NULL, 0, 'TEST Safe', 'billpay', 100, 20, 1) RETURNING id`))

	var sessionNull bool
	mustR(t, tx.GetContext(ctx, &sessionNull,
		`SELECT session_id IS NULL FROM recharge_bill_tx WHERE id = $1`, id))
	if !sessionNull {
		t.Error("admin bill row should have a NULL session_id")
	}
}
