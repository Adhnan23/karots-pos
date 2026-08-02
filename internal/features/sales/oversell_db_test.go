package sales

import (
	"context"
	"os"
	"strings"
	"testing"

	appdb "karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// These prove the end-to-end found-at-till path through a real sale. Service.
// Create runs (and commits) its own transaction, so unlike the repo-level tests
// these cannot roll back — they seed a throwaway product, assert, then delete
// everything they created. The dev database is disposable.

func salesTestDB(t *testing.T) *sqlx.DB {
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

func mustSales(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestOversellSaleCorrectsCountUpAndSells(t *testing.T) {
	conn := salesTestDB(t)
	defer conn.Close()
	ctx := context.Background()

	var categoryID, unitID, productID, cashierID, sinceSaleID int64
	mustSales(t, conn.GetContext(ctx, &categoryID, `SELECT id FROM categories LIMIT 1`))
	mustSales(t, conn.GetContext(ctx, &unitID, `SELECT id FROM units LIMIT 1`))
	mustSales(t, conn.GetContext(ctx, &cashierID, `SELECT id FROM users LIMIT 1`))
	mustSales(t, conn.GetContext(ctx, &sinceSaleID, `SELECT COALESCE(MAX(id),0) FROM sales`))
	mustSales(t, conn.GetContext(ctx, &productID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price)
		VALUES ('TEST oversell', $1, $2, 10, 25) RETURNING id`, categoryID, unitID))

	// Clean up everything we create, FK-safe order.
	defer func() {
		conn.ExecContext(ctx, `DELETE FROM sales WHERE id > $1`, sinceSaleID) //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM stock_movements WHERE product_id = $1`, productID) //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM stock_batches WHERE product_id = $1`, productID)   //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM stock WHERE product_id = $1`, productID)           //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM products WHERE id = $1`, productID)                //nolint:errcheck
	}()

	svc := NewService(conn)

	// On hand is 0, but the customer is holding one. Approve the oversell.
	_, err := svc.Create(ctx, CreateInput{
		SaleType: "retail",
		Items:    []ItemInput{{ProductID: productID, Quantity: "1", AllowOversell: true}},
		Payments: []PaymentInput{{Method: "cash", Amount: "25"}},
	}, cashierID)
	mustSales(t, err)

	var onHand decimal.Decimal
	mustSales(t, conn.GetContext(ctx, &onHand, `SELECT quantity FROM stock WHERE product_id = $1`, productID))
	if !onHand.Equal(decimal.Zero) {
		t.Errorf("on hand = %s, want 0 (found +1 then sold -1)", onHand)
	}

	var adjustCount, sellCount int
	mustSales(t, conn.GetContext(ctx, &adjustCount, `SELECT count(*) FROM stock_movements WHERE product_id = $1 AND type = 'adjust' AND quantity > 0`, productID))
	mustSales(t, conn.GetContext(ctx, &sellCount, `SELECT count(*) FROM stock_movements WHERE product_id = $1 AND type = 'sale'`, productID))
	if adjustCount != 1 {
		t.Errorf("found (+adjust) movements = %d, want 1", adjustCount)
	}
	if sellCount != 1 {
		t.Errorf("sell movements = %d, want 1", sellCount)
	}
}

func TestSaleWithoutApprovalStillRefusesAShortItem(t *testing.T) {
	conn := salesTestDB(t)
	defer conn.Close()
	ctx := context.Background()

	var categoryID, unitID, productID, cashierID int64
	mustSales(t, conn.GetContext(ctx, &categoryID, `SELECT id FROM categories LIMIT 1`))
	mustSales(t, conn.GetContext(ctx, &unitID, `SELECT id FROM units LIMIT 1`))
	mustSales(t, conn.GetContext(ctx, &cashierID, `SELECT id FROM users LIMIT 1`))
	mustSales(t, conn.GetContext(ctx, &productID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price)
		VALUES ('TEST oversell blocked', $1, $2, 10, 25) RETURNING id`, categoryID, unitID))
	defer func() {
		conn.ExecContext(ctx, `DELETE FROM stock_movements WHERE product_id = $1`, productID) //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM stock_batches WHERE product_id = $1`, productID)   //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM stock WHERE product_id = $1`, productID)           //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM products WHERE id = $1`, productID)                //nolint:errcheck
	}()

	svc := NewService(conn)
	_, err := svc.Create(ctx, CreateInput{
		SaleType: "retail",
		Items:    []ItemInput{{ProductID: productID, Quantity: "1", AllowOversell: false}},
		Payments: []PaymentInput{{Method: "cash", Amount: "25"}},
	}, cashierID)
	if err == nil {
		t.Fatal("a short item without approval should be refused")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "insufficient stock") {
		t.Errorf("error = %v, want an insufficient-stock refusal", err)
	}
	var onHand decimal.Decimal
	mustSales(t, conn.GetContext(ctx, &onHand, `SELECT quantity FROM stock WHERE product_id = $1`, productID))
	if !onHand.Equal(decimal.Zero) {
		t.Errorf("on hand = %s, want 0 (nothing sold, tx rolled back)", onHand)
	}
}
