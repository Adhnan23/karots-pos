package suppliers

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	appdb "karots-pos/internal/db"

	"github.com/shopspring/decimal"
)

// TestAdjustOpeningAndErosion proves the opening/linked split end-to-end against a
// real database: a payment erodes the still-unpaid opening only after the linked
// (transactional) part is settled, and an admin adjustment shifts outstanding by
// the delta while leaving the linked part untouched. The test creates and hard-
// deletes its own supplier row so the dev database is left as it was.
func TestAdjustOpeningAndErosion(t *testing.T) {
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

	name := fmt.Sprintf("opening-test-%d", time.Now().UnixNano())
	sup, err := svc.Create(ctx, CreateInput{Name: name, OpeningBalance: "10000"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `DELETE FROM suppliers WHERE id = $1`, sup.ID)

	check := func(step string, wantOutstanding, wantUnlinked, wantLinked string) {
		t.Helper()
		s, gerr := svc.Get(ctx, sup.ID)
		if gerr != nil {
			t.Fatalf("%s: get: %v", step, gerr)
		}
		if got := s.OutstandingBalance.StringFixed(2); got != wantOutstanding {
			t.Errorf("%s: outstanding = %s, want %s", step, got, wantOutstanding)
		}
		if got := s.OpeningUnlinked.StringFixed(2); got != wantUnlinked {
			t.Errorf("%s: opening_unlinked = %s, want %s", step, got, wantUnlinked)
		}
		if got := s.LinkedBalance().StringFixed(2); got != wantLinked {
			t.Errorf("%s: linked = %s, want %s", step, got, wantLinked)
		}
	}

	// Onboarding: all 10,000 is opening, nothing linked yet.
	check("create", "10000.00", "10000.00", "0.00")

	// A 5,000 credit purchase: linked rises, opening untouched.
	if err := repo.AddBalance(ctx, sup.ID, decimal.RequireFromString("5000")); err != nil {
		t.Fatal(err)
	}
	check("after purchase", "15000.00", "10000.00", "5000.00")

	// Pay 7,000: settles the 5,000 linked first, then the 2,000 overflow erodes
	// the opening down to 8,000.
	if err := repo.AddBalance(ctx, sup.ID, decimal.RequireFromString("-7000")); err != nil {
		t.Fatal(err)
	}
	check("after payment", "8000.00", "8000.00", "0.00")

	// Admin corrects the old debt up to 11,000: outstanding shifts by +3,000, the
	// linked part (0) stays put.
	if _, _, err := svc.AdjustOpening(ctx, sup.ID, "11000"); err != nil {
		t.Fatal(err)
	}
	check("after adjust up", "11000.00", "11000.00", "0.00")

	// Adjust the old debt back down to 2,000: outstanding follows, linked unchanged.
	if _, _, err := svc.AdjustOpening(ctx, sup.ID, "2000"); err != nil {
		t.Fatal(err)
	}
	check("after adjust down", "2000.00", "2000.00", "0.00")

	// A negative opening is allowed: a pre-system credit/advance the supplier owes
	// us. outstanding follows below zero; linked stays 0.
	if _, _, err := svc.AdjustOpening(ctx, sup.ID, "-1500"); err != nil {
		t.Fatal(err)
	}
	check("after negative opening", "-1500.00", "-1500.00", "0.00")

	// A later purchase raises linked without disturbing the negative opening credit.
	if err := repo.AddBalance(ctx, sup.ID, decimal.RequireFromString("4000")); err != nil {
		t.Fatal(err)
	}
	check("purchase over credit", "2500.00", "-1500.00", "4000.00")
}
