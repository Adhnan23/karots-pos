package customers

import (
	"context"
	"testing"
)

// A cashier adding a customer who already exists (same phone) must NOT create a
// duplicate — the second attempt reuses the existing row. Phone is required.
// Runs inside a rolled-back transaction.

func TestCreateDedupReusesExistingByPhone(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	svc := &Service{repo: NewRepository(tx)}
	phone := "077-123 4567"

	first, existed, err := svc.Create(ctx, CreateInput{Name: "TEST Alice", Phone: &phone, CreditLimit: "100"})
	must(t, err)
	if existed {
		t.Fatal("the first create should not report an existing customer")
	}

	// Same phone, cosmetically different formatting + different name.
	dupPhone := "0771234567"
	second, existed, err := svc.Create(ctx, CreateInput{Name: "TEST Alice again", Phone: &dupPhone, CreditLimit: "999"})
	must(t, err)
	if !existed {
		t.Error("the second create should reuse the existing customer")
	}
	if second.ID != first.ID {
		t.Errorf("second.ID = %d, want the existing %d (no duplicate)", second.ID, first.ID)
	}

	var count int
	must(t, tx.GetContext(ctx, &count,
		`SELECT count(*) FROM customers WHERE phone = $1 AND is_active = true`, "0771234567"))
	if count != 1 {
		t.Errorf("active customers with that phone = %d, want 1", count)
	}
}

func TestCreateRejectsBlankPhone(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	svc := &Service{repo: NewRepository(tx)}
	blank := "   "
	if _, _, err := svc.Create(ctx, CreateInput{Name: "TEST No Phone", Phone: &blank, CreditLimit: "0"}); err == nil {
		t.Error("a blank phone must be rejected")
	}
	if _, _, err := svc.Create(ctx, CreateInput{Name: "TEST Nil Phone", Phone: nil, CreditLimit: "0"}); err == nil {
		t.Error("a nil phone must be rejected")
	}
}
