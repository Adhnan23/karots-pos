package stock

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"
)

// FoundAtTill is the honest alternative to letting stock go negative: the goods
// physically existed but the count was short, so it corrects the count UP (a
// positive adjust movement) before the sale depletes it. These run inside a
// rolled-back transaction, so the dev database is untouched.

func seedProduct(t *testing.T, ctx context.Context, repo *Repository, name string) int64 {
	t.Helper()
	var categoryID, unitID, productID int64
	must(t, repo.q.GetContext(ctx, &categoryID, `SELECT id FROM categories LIMIT 1`))
	must(t, repo.q.GetContext(ctx, &unitID, `SELECT id FROM units LIMIT 1`))
	must(t, repo.q.GetContext(ctx, &productID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price)
		VALUES ($1, $2, $3, 10, 25) RETURNING id`, name, categoryID, unitID))
	return productID
}

func TestFoundAtTillOpensAFoundLotWhenNoneExists(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	repo := NewRepository(tx)
	productID := seedProduct(t, ctx, repo, "TEST found-at-till new")
	var userID int64
	must(t, tx.GetContext(ctx, &userID, `SELECT id FROM users LIMIT 1`))

	// On hand is 0 (the trigger seeds a zero stock row); no batches.
	must(t, repo.FoundAtTill(ctx, productID, 0, decimal.NewFromInt(1), decimal.NewFromInt(10), userID))

	onHand, err := repo.GetQuantity(ctx, productID)
	must(t, err)
	if !onHand.Equal(decimal.NewFromInt(1)) {
		t.Errorf("on hand = %s, want 1", onHand)
	}

	var lotQty decimal.Decimal
	var source string
	must(t, tx.GetContext(ctx, &lotQty, `SELECT qty_remaining FROM stock_batches WHERE product_id = $1`, productID))
	must(t, tx.GetContext(ctx, &source, `SELECT source FROM stock_batches WHERE product_id = $1`, productID))
	if !lotQty.Equal(decimal.NewFromInt(1)) {
		t.Errorf("found lot qty_remaining = %s, want 1", lotQty)
	}
	if source != "found" {
		t.Errorf("found lot source = %q, want \"found\"", source)
	}

	var mtype, note string
	var mqty decimal.Decimal
	must(t, tx.GetContext(ctx, &mtype, `SELECT type FROM stock_movements WHERE product_id = $1 ORDER BY id DESC LIMIT 1`, productID))
	must(t, tx.GetContext(ctx, &mqty, `SELECT quantity FROM stock_movements WHERE product_id = $1 ORDER BY id DESC LIMIT 1`, productID))
	must(t, tx.GetContext(ctx, &note, `SELECT COALESCE(note,'') FROM stock_movements WHERE product_id = $1 ORDER BY id DESC LIMIT 1`, productID))
	if mtype != string(MoveAdjust) {
		t.Errorf("movement type = %q, want %q", mtype, MoveAdjust)
	}
	if !mqty.Equal(decimal.NewFromInt(1)) {
		t.Errorf("movement quantity = %s, want +1", mqty)
	}
	if note != "found at till — count corrected before sale" {
		t.Errorf("movement note = %q", note)
	}
}

func TestFoundAtTillTopsUpTheNamedLot(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	repo := NewRepository(tx)
	productID := seedProduct(t, ctx, repo, "TEST found-at-till topup")
	var userID int64
	must(t, tx.GetContext(ctx, &userID, `SELECT id FROM users LIMIT 1`))

	batchID, err := repo.InsertBatch(ctx, NewBatch{
		ProductID: productID, Quantity: decimal.NewFromInt(2),
		CostPrice: decimal.NewFromInt(10), SellingPrice: decimal.NewFromInt(25),
		Source: "purchase",
	})
	must(t, err)
	must(t, repo.Increment(ctx, productID, decimal.NewFromInt(2))) // mirror the lot into on-hand

	// Sell 3 from a lot that only has 2: top the SAME lot up by the shortfall.
	must(t, repo.FoundAtTill(ctx, productID, batchID, decimal.NewFromInt(1), decimal.NewFromInt(10), userID))

	var lotQty decimal.Decimal
	must(t, tx.GetContext(ctx, &lotQty, `SELECT qty_remaining FROM stock_batches WHERE id = $1`, batchID))
	if !lotQty.Equal(decimal.NewFromInt(3)) {
		t.Errorf("named lot qty_remaining = %s, want 3 (topped up, not a new lot)", lotQty)
	}
	var lotCount int
	must(t, tx.GetContext(ctx, &lotCount, `SELECT count(*) FROM stock_batches WHERE product_id = $1`, productID))
	if lotCount != 1 {
		t.Errorf("lot count = %d, want 1 (no new lot opened)", lotCount)
	}
}
