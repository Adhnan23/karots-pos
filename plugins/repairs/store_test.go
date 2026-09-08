package repairs

import (
	"context"
	"os"
	"testing"

	appdb "karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

func testDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	conn, err := appdb.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return conn
}

func TestNextTicketSequence(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	tx, err := conn.Beginx()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	s := newStoreQ(tx)
	ctx := context.Background()

	no1, code1, err := s.NextTicket(ctx)
	if err != nil {
		t.Fatalf("NextTicket: %v", err)
	}
	no2, _, err := s.NextTicket(ctx)
	if err != nil {
		t.Fatalf("NextTicket 2: %v", err)
	}
	if no1 == no2 {
		t.Fatalf("ticket did not increment: %s == %s", no1, no2)
	}
	// Format check (values depend on the pre-existing seq, so check shape).
	if len(code1) < 4 || code1[:3] != "RPR" {
		t.Fatalf("bad code format: %s", code1)
	}
}

func TestCreateJobRoundTrip(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	tx, err := conn.Beginx()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	s := newStoreQ(tx)
	ctx := context.Background()

	id, err := s.CreateJob(ctx, JobInput{
		CustomerName: "Nimal", CustomerPhone: "0771234567",
		RepairType: "Screen replace", DeviceModel: "iPhone 11",
		Fault: "cracked glass", WarrantyDays: 30, CreatedBy: 1,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.AddPart(ctx, id, PartInput{
		ProductID: 0, Qty: decimal.RequireFromString("1"),
		UnitCharge:   decimal.RequireFromString("1200"),
		DiscountType: "percent", DiscountValue: decimal.RequireFromString("10"),
	}); err != nil {
		t.Fatalf("AddPart: %v", err)
	}
	if err := s.AddCharge(ctx, id, "Labour", decimal.RequireFromString("500")); err != nil {
		t.Fatalf("AddCharge: %v", err)
	}
	if err := s.AddPayment(ctx, id, decimal.RequireFromString("400"), "deposit", 1); err != nil {
		t.Fatalf("AddPayment: %v", err)
	}

	d, err := s.GetJob(ctx, id)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if d.Job.RepairType != "Screen replace" || d.Job.DeviceModel != "iPhone 11" {
		t.Fatalf("job fields not persisted: %+v", d.Job)
	}
	if len(d.Parts) != 1 || len(d.Charges) != 1 || len(d.Payments) != 1 {
		t.Fatalf("children counts = %d/%d/%d, want 1/1/1", len(d.Parts), len(d.Charges), len(d.Payments))
	}
	// 10% off a 1200 gross part = 120 stored discount.
	if !d.Parts[0].Discount.Equal(decimal.RequireFromString("120")) {
		t.Fatalf("part discount = %s, want 120", d.Parts[0].Discount)
	}
}
