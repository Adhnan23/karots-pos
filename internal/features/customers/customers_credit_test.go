package customers

import (
	"context"
	"os"
	"testing"

	appdb "karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// These prove the till's inline credit-limit edit: it changes only the limit,
// validates the amount, and (like every DB test here) runs inside a transaction
// that is rolled back so the dev database is untouched.

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

func TestSetCreditLimitPersists(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck // leave no trace

	svc := &Service{repo: NewRepository(tx)}
	cust, err := svc.repo.Create(ctx, "TEST credit edit", nil, nil, decimal.NewFromInt(100), decimal.Zero)
	must(t, err)

	if err := svc.SetCreditLimit(ctx, cust.ID, "5000"); err != nil {
		t.Fatalf("SetCreditLimit: %v", err)
	}
	got, err := svc.repo.FindByID(ctx, cust.ID)
	must(t, err)
	if !got.CreditLimit.Equal(decimal.NewFromInt(5000)) {
		t.Errorf("credit limit = %s, want 5000", got.CreditLimit)
	}
}

func TestSetCreditLimitRejectsNegativeAndLeavesItUnchanged(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	svc := &Service{repo: NewRepository(tx)}
	cust, err := svc.repo.Create(ctx, "TEST credit neg", nil, nil, decimal.NewFromInt(100), decimal.Zero)
	must(t, err)

	if err := svc.SetCreditLimit(ctx, cust.ID, "-50"); err == nil {
		t.Fatal("a negative credit limit was accepted")
	}
	got, err := svc.repo.FindByID(ctx, cust.ID)
	must(t, err)
	if !got.CreditLimit.Equal(decimal.NewFromInt(100)) {
		t.Errorf("credit limit changed to %s despite a rejected update", got.CreditLimit)
	}
}
