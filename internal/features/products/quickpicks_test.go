package products

import (
	"context"
	"os"
	"testing"
	"time"

	appdb "karots-pos/internal/db"
)

// FrequentProducts must rank the shop's best-sellers by net quantity, exclude
// services and pass-through (reload) lines, and skip anything out of stock — the
// grid only offers real, tappable stock. Runs in a rolled-back tx on far-future
// dates so no real dev data falls in range.
func TestFrequentProducts(t *testing.T) {
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
	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	var catID, unitID, cashierID int64
	must(tx.GetContext(ctx, &catID, `SELECT id FROM categories LIMIT 1`))
	must(tx.GetContext(ctx, &unitID, `SELECT id FROM units LIMIT 1`))
	must(tx.GetContext(ctx, &cashierID, `SELECT id FROM users LIMIT 1`))

	newProd := func(name string, service, passThrough bool, stockQty int) int64 {
		var id int64
		must(tx.GetContext(ctx, &id, `
			INSERT INTO products (name, category_id, unit_id, cost_price, selling_price, is_service, pass_through)
			VALUES ($1,$2,$3,10,100,$4,$5) RETURNING id`, name, catID, unitID, service, passThrough))
		if !service {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO stock (product_id, quantity) VALUES ($1,$2)
				ON CONFLICT (product_id) DO UPDATE SET quantity = EXCLUDED.quantity`, id, stockQty)
			must(err)
		}
		return id
	}
	high := newProd("QP high", false, false, 50)        // best-seller, in stock
	low := newProd("QP low", false, false, 50)          // sold less, in stock
	airtime := newProd("QP airtime", true, true, 0)     // pass-through service — excluded
	oos := newProd("QP out-of-stock", false, false, 0)  // sold, but 0 stock — excluded

	var s1 int64
	must(tx.GetContext(ctx, &s1, `
		INSERT INTO sales (receipt_no, subtotal, total, paid_amount, status, cashier_id, created_at)
		VALUES ('QP-1', 1000, 1000, 1000, 'completed', $1, '2099-01-01T10:00:00Z') RETURNING id`, cashierID))
	_, err = tx.ExecContext(ctx, `
		INSERT INTO sale_items (sale_id, product_id, quantity, unit_price, subtotal, cost_price)
		VALUES ($1,$2,10,100,1000,10), ($1,$3,3,100,300,10), ($1,$4,5,100,500,0), ($1,$5,9,100,900,10)`,
		s1, high, low, airtime, oos)
	must(err)

	rows, err := NewRepository(tx).FrequentProducts(ctx, time.Date(2098, 12, 31, 0, 0, 0, 0, time.UTC), 16)
	must(err)

	got := map[int64]int{}
	for i, p := range rows {
		got[p.ID] = i
	}
	if _, ok := got[airtime]; ok {
		t.Error("pass-through/service product appeared in frequent grid")
	}
	if _, ok := got[oos]; ok {
		t.Error("out-of-stock product appeared in frequent grid")
	}
	hi, okHi := got[high]
	lo, okLo := got[low]
	if !okHi || !okLo {
		t.Fatalf("expected both in-stock sellers present; got %v", got)
	}
	if hi >= lo {
		t.Errorf("best-seller not ranked first: high at %d, low at %d", hi, lo)
	}
}
