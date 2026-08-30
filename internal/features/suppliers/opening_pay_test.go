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

// TestSupplierPayOpening: paying the old debt down drops outstanding +
// opening_unlinked together, leaves linked alone, never goes below zero.
func TestSupplierPayOpening(t *testing.T) {
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

	name := fmt.Sprintf("sup-payopening-%d", time.Now().UnixNano())
	sup, err := svc.Create(ctx, CreateInput{Name: name, OpeningBalance: "10000"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `DELETE FROM suppliers WHERE id = $1`, sup.ID)

	if err := repo.AddBalance(ctx, sup.ID, decimal.RequireFromString("5000")); err != nil {
		t.Fatal(err)
	} // outstanding 15000, opening 10000, linked 5000

	if err := repo.PayOpening(ctx, sup.ID, decimal.RequireFromString("4000")); err != nil {
		t.Fatal(err)
	}
	s, _ := svc.Get(ctx, sup.ID)
	if got := s.OpeningUnlinked.StringFixed(2); got != "6000.00" {
		t.Fatalf("opening = %s, want 6000.00", got)
	}
	if got := s.LinkedBalance().StringFixed(2); got != "5000.00" {
		t.Fatalf("linked = %s, want 5000.00", got)
	}

	// Overpaying the old debt clamps opening at 0 and preserves linked.
	if err := repo.PayOpening(ctx, sup.ID, decimal.RequireFromString("99999")); err != nil {
		t.Fatal(err)
	}
	s, _ = svc.Get(ctx, sup.ID)
	if got := s.OpeningUnlinked.StringFixed(2); got != "0.00" {
		t.Fatalf("opening after overpay = %s, want 0.00", got)
	}
	if got := s.LinkedBalance().StringFixed(2); got != "5000.00" {
		t.Fatalf("linked after overpay = %s, want 5000.00", got)
	}
}
