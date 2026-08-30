package customers

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	appdb "karots-pos/internal/db"

	"github.com/shopspring/decimal"
)

// TestPayOpening proves paying the old debt down reduces outstanding and
// opening_unlinked together, leaves the linked part alone, and never goes below
// zero. Creates and hard-deletes its own customer row.
func TestPayOpening(t *testing.T) {
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
	svc := NewService(conn)
	repo := NewRepository(conn)

	name := fmt.Sprintf("payopening-test-%d", time.Now().UnixNano())
	phone := fmt.Sprintf("%010d", time.Now().UnixNano()%10000000000) // unique, dedup guard requires phone
	cust, _, err := svc.Create(ctx, CreateInput{Name: name, Phone: &phone, OpeningBalance: "10000"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `DELETE FROM customers WHERE id = $1`, cust.ID)

	check := func(step, wantOut, wantUnlinked, wantLinked string) {
		t.Helper()
		c, gerr := svc.Get(ctx, cust.ID)
		if gerr != nil {
			t.Fatalf("%s: %v", step, gerr)
		}
		if got := c.OutstandingBalance.StringFixed(2); got != wantOut {
			t.Errorf("%s: outstanding = %s, want %s", step, got, wantOut)
		}
		if got := c.OpeningUnlinked.StringFixed(2); got != wantUnlinked {
			t.Errorf("%s: opening_unlinked = %s, want %s", step, got, wantUnlinked)
		}
		if got := c.LinkedBalance().StringFixed(2); got != wantLinked {
			t.Errorf("%s: linked = %s, want %s", step, got, wantLinked)
		}
	}

	// A 5,000 credit sale: linked rises, opening untouched.
	if err := repo.AddBalance(ctx, cust.ID, decimal.RequireFromString("5000")); err != nil {
		t.Fatal(err)
	}
	check("after sale", "15000.00", "10000.00", "5000.00")

	// Pay 4,000 against the OLD debt specifically: opening drops to 6,000, linked
	// stays at 5,000 (unlike AddBalance, which would settle linked first).
	if err := repo.PayOpening(ctx, cust.ID, decimal.RequireFromString("4000")); err != nil {
		t.Fatal(err)
	}
	check("after pay opening", "11000.00", "6000.00", "5000.00")

	// Paying more than the remaining opening clamps opening at 0 (guard lives in
	// the service; the repo itself must not go negative).
	if err := repo.PayOpening(ctx, cust.ID, decimal.RequireFromString("99999")); err != nil {
		t.Fatal(err)
	}
	check("after overpay clamp", "5000.00", "0.00", "5000.00")
}

// TestRecordPaymentTargetsOpening proves an admin payment flagged ApplyToOpening
// pays the old debt (not linked-first), and that overpaying it is rejected.
func TestRecordPaymentTargetsOpening(t *testing.T) {
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
	svc := NewService(conn)
	repo := NewRepository(conn)

	name := fmt.Sprintf("paytarget-test-%d", time.Now().UnixNano())
	phone := fmt.Sprintf("%010d", time.Now().UnixNano()%10000000000)
	cust, _, err := svc.Create(ctx, CreateInput{Name: name, Phone: &phone, OpeningBalance: "10000"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `DELETE FROM customers WHERE id = $1`, cust.ID)
	if err := repo.AddBalance(ctx, cust.ID, decimal.RequireFromString("5000")); err != nil {
		t.Fatal(err)
	} // outstanding 15000, opening 10000, linked 5000

	// Pay 3,000 against the old debt: opening 10000 -> 7000, linked stays 5000.
	if err := svc.RecordPayment(ctx, cust.ID,
		PaymentInput{Amount: "3000", Method: "cash", ApplyToOpening: true}, 0); err != nil {
		t.Fatal(err)
	}
	c, _ := svc.Get(ctx, cust.ID)
	if got := c.OpeningUnlinked.StringFixed(2); got != "7000.00" {
		t.Fatalf("opening = %s, want 7000.00", got)
	}
	if got := c.LinkedBalance().StringFixed(2); got != "5000.00" {
		t.Fatalf("linked = %s, want 5000.00", got)
	}

	// Overpaying the old debt (7,000 left) is rejected.
	if err := svc.RecordPayment(ctx, cust.ID,
		PaymentInput{Amount: "8000", Method: "cash", ApplyToOpening: true}, 0); err == nil {
		t.Fatal("expected rejection paying more than the old debt")
	}
}
