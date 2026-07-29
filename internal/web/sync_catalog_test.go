package web

import (
	"context"
	"testing"

	"karots-pos/internal/features/products"
)

// TestSyncCatalogReturnsPathAndQty proves the sync query returns the full
// category path, the current on-hand qty, and the current selling price for an
// active product — the exact shape the stock_capture app ingests.
func TestSyncCatalogReturnsPathAndQty(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck // leave no trace

	var parentID, childID, unitID int64
	must(t, tx.GetContext(ctx, &parentID,
		`INSERT INTO categories (name) VALUES ('SyncTest Beverages') RETURNING id`))
	must(t, tx.GetContext(ctx, &childID,
		`INSERT INTO categories (name, parent_id) VALUES ('SyncTest Soft Drinks', $1) RETURNING id`, parentID))
	must(t, tx.GetContext(ctx, &unitID,
		`INSERT INTO units (name, abbreviation) VALUES ('SyncTest Piece', 'stpcs') RETURNING id`))

	var prodID int64
	must(t, tx.GetContext(ctx, &prodID,
		`INSERT INTO products (name, barcode, category_id, unit_id, selling_price, is_active)
		 VALUES ('SyncTest Cola', '9990001', $1, $2, 120.00, true) RETURNING id`, childID, unitID))
	// A stock row may be auto-created with the product; upsert to set the qty.
	must(t, tx.GetContext(ctx, new(int64),
		`INSERT INTO stock (product_id, quantity) VALUES ($1, 7)
		 ON CONFLICT (product_id) DO UPDATE SET quantity = EXCLUDED.quantity
		 RETURNING product_id`, prodID))

	rows, err := products.NewRepository(tx).SyncCatalog(ctx)
	must(t, err)

	var got *products.SyncRow
	for i := range rows {
		if rows[i].ID == prodID {
			got = &rows[i]
		}
	}
	if got == nil {
		t.Fatalf("product %d not in sync catalog", prodID)
	}
	if got.Category != "SyncTest Beverages > SyncTest Soft Drinks" {
		t.Fatalf("category path = %q, want full path", got.Category)
	}
	if got.StockQty.String() != "7" {
		t.Fatalf("stock_qty = %s, want 7", got.StockQty)
	}
	if got.SellingPrice.String() != "120" {
		t.Fatalf("selling_price = %s, want 120", got.SellingPrice)
	}
}
