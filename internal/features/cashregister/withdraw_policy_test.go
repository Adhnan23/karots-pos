package cashregister

import (
	"context"
	"os"
	"strings"
	"testing"

	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/sales"
)

// TestWithdrawUntrackedPolicy verifies the server-side guard: with untracked cash
// movements disabled, a locker-less withdrawal is rejected before it touches the
// session; once a locker is named the guard passes. It flips the single settings
// flag and restores it, so the dev database is left exactly as it was.
func TestWithdrawUntrackedPolicy(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	conn, err := appdb.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := context.Background()

	var orig bool
	if err := conn.GetContext(ctx, &orig, `SELECT allow_untracked_cash FROM settings WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = conn.ExecContext(ctx, `UPDATE settings SET allow_untracked_cash = $1 WHERE id = 1`, orig)
	}()

	if _, err := conn.ExecContext(ctx, `UPDATE settings SET allow_untracked_cash = false WHERE id = 1`); err != nil {
		t.Fatal(err)
	}

	svc := NewService(conn, sales.NewService(conn))

	// Locker-less withdrawal must be rejected by the policy guard.
	if _, err := svc.Withdraw(ctx, 1, MovementInput{Amount: "10", CounterLockerID: 0}); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "untracked") {
		t.Fatalf("expected an 'untracked disabled' rejection, got: %v", err)
	}

	// With a locker chosen the guard passes; the call then fails later for an
	// unrelated reason (no open session / unknown locker), which must NOT be the
	// untracked guard.
	if _, err := svc.Withdraw(ctx, 1, MovementInput{Amount: "10", CounterLockerID: 999999}); err != nil &&
		strings.Contains(strings.ToLower(err.Error()), "untracked") {
		t.Fatalf("guard should have passed once a locker was chosen, got: %v", err)
	}
}
