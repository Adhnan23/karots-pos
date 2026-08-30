package supplierpay

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/suppliers"

	"github.com/shopspring/decimal"
)

// TestPayOpeningPortion: a payment carrying an Opening amount reduces the
// supplier's old debt (not linked-first) and records the full total; overpaying
// the opening is rejected.
func TestPayOpeningPortion(t *testing.T) {
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
	supSvc := suppliers.NewService(conn)
	supRepo := suppliers.NewRepository(conn)
	paySvc := NewService(conn)

	name := fmt.Sprintf("sp-open-%d", time.Now().UnixNano())
	sup, err := supSvc.Create(ctx, suppliers.CreateInput{Name: name, OpeningBalance: "8000"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `DELETE FROM supplier_payments WHERE supplier_id = $1`, sup.ID)
	defer conn.ExecContext(ctx, `DELETE FROM suppliers WHERE id = $1`, sup.ID)
	if err := supRepo.AddBalance(ctx, sup.ID, decimal.RequireFromString("2000")); err != nil {
		t.Fatal(err)
	} // outstanding 10000, opening 8000, linked 2000

	var userID int64
	if err := conn.GetContext(ctx, &userID, `SELECT id FROM users ORDER BY id LIMIT 1`); err != nil {
		t.Fatal(err)
	}

	// Pay 3,000 against the old debt only (no invoice allocations).
	if _, err := paySvc.Pay(ctx, sup.ID, PayInput{Method: "cash", Opening: decimal.RequireFromString("3000")}, userID); err != nil {
		t.Fatal(err)
	}
	s, _ := supSvc.Get(ctx, sup.ID)
	if got := s.OpeningUnlinked.StringFixed(2); got != "5000.00" {
		t.Fatalf("opening = %s, want 5000.00", got)
	}
	if got := s.LinkedBalance().StringFixed(2); got != "2000.00" {
		t.Fatalf("linked = %s, want 2000.00", got)
	}

	// Overpaying the old debt (5,000 left) is rejected.
	if _, err := paySvc.Pay(ctx, sup.ID, PayInput{Method: "cash", Opening: decimal.RequireFromString("6000")}, userID); err == nil {
		t.Fatal("expected rejection paying more than the old debt")
	}
}
