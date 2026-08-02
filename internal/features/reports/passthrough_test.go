package reports

import (
	"context"
	"os"
	"testing"
	"time"

	appdb "karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// A pass-through line (resold airtime, bill face value) is money passing through
// the shop, not its margin — it must be excluded from revenue, COGS and profit.
// Runs in a rolled-back tx, on a far-future date so no real dev data is in range.

func reportsTestDB(t *testing.T) *sqlx.DB {
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

func TestComputeExcludesPassThrough(t *testing.T) {
	conn := reportsTestDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	mustR(t, err)
	defer tx.Rollback() //nolint:errcheck

	var catID, unitID, cashierID, normalID, ptID int64
	mustR(t, tx.GetContext(ctx, &catID, `SELECT id FROM categories LIMIT 1`))
	mustR(t, tx.GetContext(ctx, &unitID, `SELECT id FROM units LIMIT 1`))
	mustR(t, tx.GetContext(ctx, &cashierID, `SELECT id FROM users LIMIT 1`))
	mustR(t, tx.GetContext(ctx, &normalID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price)
		VALUES ('TEST normal', $1, $2, 60, 100) RETURNING id`, catID, unitID))
	mustR(t, tx.GetContext(ctx, &ptID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price, is_service, pass_through)
		VALUES ('TEST airtime', $1, $2, 0, 500, true, true) RETURNING id`, catID, unitID))

	var saleID int64
	mustR(t, tx.GetContext(ctx, &saleID, `
		INSERT INTO sales (receipt_no, subtotal, total, paid_amount, status, cashier_id, created_at)
		VALUES ('TESTPT-1', 600, 600, 600, 'completed', $1, '2099-01-01T10:00:00Z') RETURNING id`, cashierID))
	_, err = tx.ExecContext(ctx, `
		INSERT INTO sale_items (sale_id, product_id, quantity, unit_price, subtotal, cost_price)
		VALUES ($1,$2,1,100,100,60), ($1,$3,1,500,500,0)`, saleID, normalID, ptID)
	mustR(t, err)

	svc := &Service{db: tx}
	from := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2099, 1, 2, 0, 0, 0, 0, time.UTC)

	pl, err := svc.Compute(ctx, from, to)
	mustR(t, err)
	if !pl.GrossRevenue.Equal(decimal.NewFromInt(100)) {
		t.Errorf("GrossRevenue = %s, want 100 (airtime face value excluded)", pl.GrossRevenue)
	}
	if !pl.COGS.Equal(decimal.NewFromInt(60)) {
		t.Errorf("COGS = %s, want 60", pl.COGS)
	}
	if !pl.GrossProfit.Equal(decimal.NewFromInt(40)) {
		t.Errorf("GrossProfit = %s, want 40 (not 540)", pl.GrossProfit)
	}

	top, err := svc.TopProducts(ctx, from, to, "revenue", 10)
	mustR(t, err)
	for _, r := range top {
		if r.ProductName == "TEST airtime" {
			t.Errorf("Top Products includes a pass-through product")
		}
	}
}
