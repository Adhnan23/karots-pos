package stock

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"
)

// A return used to always open a NEW lot. One customer bringing a bottle back
// left the shop holding three lots of the same thing where it had one — the
// batch list became unreadable, FEFO order started depending on which return
// came first, and a product whose lots all agreed on price could sprout a
// spurious "which price?" prompt.

func TestRestockLotPutsItBackWhereItCameFrom(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	repo, productID, lotA, lotB := twoPricedLots(t, ctx, tx)
	before := countLots(t, ctx, repo, productID)

	ok, err := repo.RestockLot(ctx, &lotA, productID, decimal.NewFromInt(2))
	must(t, err)
	if !ok {
		t.Fatal("RestockLot reported it could not restock a live lot")
	}
	if after := countLots(t, ctx, repo, productID); after != before {
		t.Errorf("lot count went %d -> %d; a return must not fragment the batch list", before, after)
	}
	// lot A held 4, so it should now hold 6 — and lot B must be untouched.
	if got := lotQty(t, ctx, repo, lotA); !got.Equal(decimal.NewFromInt(6)) {
		t.Errorf("original lot holds %s, want 6", got)
	}
	if got := lotQty(t, ctx, repo, lotB); !got.Equal(decimal.NewFromInt(10)) {
		t.Errorf("the other lot changed to %s, want 10 untouched", got)
	}
}

// An old sale carries no lot, and a lot can be gone by the time goods come back.
// Both must fall back to opening a return lot rather than losing the stock.
func TestRestockLotDeclinesWhenTheLotIsUnknown(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	repo, productID, _, _ := twoPricedLots(t, ctx, tx)

	ok, err := repo.RestockLot(ctx, nil, productID, decimal.NewFromInt(1))
	must(t, err)
	if ok {
		t.Error("a sale with no recorded lot must not claim to have restocked one")
	}

	gone := int64(999999)
	ok, err = repo.RestockLot(ctx, &gone, productID, decimal.NewFromInt(1))
	must(t, err)
	if ok {
		t.Error("a lot that no longer exists must not claim to have been restocked")
	}
}

// Another product's lot must never absorb this product's return.
func TestRestockLotRefusesAnotherProductsLot(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	repo, _, lotA, _ := twoPricedLots(t, ctx, tx)

	var otherProduct int64
	var categoryID, unitID int64
	must(t, tx.GetContext(ctx, &categoryID, `SELECT id FROM categories LIMIT 1`))
	must(t, tx.GetContext(ctx, &unitID, `SELECT id FROM units LIMIT 1`))
	must(t, tx.GetContext(ctx, &otherProduct, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price)
		VALUES ('TEST other product', $1, $2, 10, 20) RETURNING id`, categoryID, unitID))

	ok, err := repo.RestockLot(ctx, &lotA, otherProduct, decimal.NewFromInt(1))
	must(t, err)
	if ok {
		t.Error("restocked one product's return into another product's lot")
	}
}

func countLots(t *testing.T, ctx context.Context, repo *Repository, productID int64) int {
	t.Helper()
	var n int
	must(t, repo.q.GetContext(ctx, &n, `SELECT COUNT(*) FROM stock_batches WHERE product_id = $1`, productID))
	return n
}

func lotQty(t *testing.T, ctx context.Context, repo *Repository, batchID int64) decimal.Decimal {
	t.Helper()
	var q decimal.Decimal
	must(t, repo.q.GetContext(ctx, &q, `SELECT qty_remaining FROM stock_batches WHERE id = $1`, batchID))
	return q
}
