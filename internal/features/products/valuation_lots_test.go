package products

import (
	"context"
	"os"
	"testing"

	appdb "karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// Stock must be valued at what the shop actually paid for the units it is
// holding, not at whatever the product's price says today. Valuing every unit at
// the newest price marked stock up by the whole of a cost increase the moment it
// was entered, and made this page disagree with the Net Position page — which
// had always valued from lots — by exactly that amount.
//
// Rolled back, so the dev database is untouched.

func valuationDB(t *testing.T) *sqlx.DB {
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

// pricedStock creates a product repriced upward, holding older cheaper stock.
// 10 units bought at 50 while the product now reads cost 80 / sell 200.
func pricedStock(t *testing.T, ctx context.Context, tx *sqlx.Tx) (categoryID, productID int64) {
	t.Helper()
	var unitID int64
	if err := tx.GetContext(ctx, &categoryID, `
		INSERT INTO categories (name) VALUES ('TEST valuation cat') RETURNING id`); err != nil {
		t.Fatal(err)
	}
	if err := tx.GetContext(ctx, &unitID, `SELECT id FROM units LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.GetContext(ctx, &productID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price)
		VALUES ('TEST repriced stock', $1, $2, 80, 200) RETURNING id`, categoryID, unitID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO stock (product_id, quantity) VALUES ($1, 10)
		ON CONFLICT (product_id) DO UPDATE SET quantity = EXCLUDED.quantity`, productID); err != nil {
		t.Fatal(err)
	}
	// The lot it is really made of: bought at 50, stickered 150.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO stock_batches (product_id, qty_received, qty_remaining, cost_price, selling_price, source)
		VALUES ($1, 10, 10, 50, 150, 'purchase')`, productID); err != nil {
		t.Fatal(err)
	}
	return categoryID, productID
}

func TestValuationUsesLotCostNotTheRepricedProduct(t *testing.T) {
	conn := valuationDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck

	categoryID, _ := pricedStock(t, ctx, tx)
	repo := &Repository{db: tx}

	v, err := repo.ValuationBranch(ctx, &categoryID)
	if err != nil {
		t.Fatal(err)
	}
	// 10 x 50 = 500 at cost. The old query said 10 x 80 = 800.
	if !v.CostValue.Equal(decimal.NewFromInt(500)) {
		t.Errorf("cost value %s, want 500 (what the shop paid), not 800 (today's price)", v.CostValue)
	}
	// 10 x 150 = 1500 at retail, the price the lot will actually ring at.
	if !v.RetailValue.Equal(decimal.NewFromInt(1500)) {
		t.Errorf("retail value %s, want 1500 (the lot's own price)", v.RetailValue)
	}
}

// Stock with no lots behind it — recorded before lots existed — must still be
// valued from the product, or the fix would silently zero it out.
func TestValuationFallsBackToProductWhenThereAreNoLots(t *testing.T) {
	conn := valuationDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck

	var categoryID, unitID, productID int64
	if err := tx.GetContext(ctx, &categoryID, `
		INSERT INTO categories (name) VALUES ('TEST lotless cat') RETURNING id`); err != nil {
		t.Fatal(err)
	}
	if err := tx.GetContext(ctx, &unitID, `SELECT id FROM units LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.GetContext(ctx, &productID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price)
		VALUES ('TEST lotless stock', $1, $2, 30, 70) RETURNING id`, categoryID, unitID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO stock (product_id, quantity) VALUES ($1, 4)
		ON CONFLICT (product_id) DO UPDATE SET quantity = EXCLUDED.quantity`, productID); err != nil {
		t.Fatal(err)
	}

	repo := &Repository{db: tx}
	v, err := repo.ValuationBranch(ctx, &categoryID)
	if err != nil {
		t.Fatal(err)
	}
	if !v.CostValue.Equal(decimal.NewFromInt(120)) {
		t.Errorf("cost value %s, want 120 — lotless stock must keep its old valuation", v.CostValue)
	}
	if !v.RetailValue.Equal(decimal.NewFromInt(280)) {
		t.Errorf("retail value %s, want 280", v.RetailValue)
	}
}

// A lot on the price sentinel (0 = follow the product) must value at the
// product's price, not at zero.
func TestValuationResolvesTheLotPriceSentinel(t *testing.T) {
	conn := valuationDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck

	var categoryID, unitID, productID int64
	if err := tx.GetContext(ctx, &categoryID, `
		INSERT INTO categories (name) VALUES ('TEST sentinel cat') RETURNING id`); err != nil {
		t.Fatal(err)
	}
	if err := tx.GetContext(ctx, &unitID, `SELECT id FROM units LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.GetContext(ctx, &productID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price)
		VALUES ('TEST sentinel valuation', $1, $2, 25, 90) RETURNING id`, categoryID, unitID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO stock (product_id, quantity) VALUES ($1, 5)
		ON CONFLICT (product_id) DO UPDATE SET quantity = EXCLUDED.quantity`, productID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO stock_batches (product_id, qty_received, qty_remaining, cost_price, selling_price, source)
		VALUES ($1, 5, 5, 25, 0, 'opening')`, productID); err != nil {
		t.Fatal(err)
	}

	repo := &Repository{db: tx}
	v, err := repo.ValuationBranch(ctx, &categoryID)
	if err != nil {
		t.Fatal(err)
	}
	if !v.RetailValue.Equal(decimal.NewFromInt(450)) {
		t.Errorf("retail value %s, want 450 — a sentinel lot follows the product's price", v.RetailValue)
	}
}
