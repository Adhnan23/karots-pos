package stock

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	appdb "karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// These cover the machinery behind the till's "which price?" prompt: an old
// bottle stickered Rs 100 sitting next to newly-received stock at Rs 120 must be
// sellable at Rs 100, and selling it must drain the OLD lot, not whichever one
// FEFO would have reached for.
//
// Everything runs inside a transaction that is rolled back, so the dev database
// is untouched.

// twoPricedLots sets up a product with two live lots at different prices:
// lot A = 4 units @ 100 (the old stickered stock), lot B = 10 units @ 120.
// The product's own price is 120, matching a shop that just re-priced.
func twoPricedLots(t *testing.T, ctx context.Context, tx *sqlx.Tx) (repo *Repository, productID, lotA, lotB int64) {
	t.Helper()
	repo = NewRepository(tx)

	var categoryID, unitID int64
	must(t, tx.GetContext(ctx, &categoryID, `SELECT id FROM categories LIMIT 1`))
	must(t, tx.GetContext(ctx, &unitID, `SELECT id FROM units LIMIT 1`))
	must(t, tx.GetContext(ctx, &productID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price)
		VALUES ('TEST two-priced lots', $1, $2, 60, 120) RETURNING id`, categoryID, unitID))

	var err error
	lotA, err = repo.InsertBatch(ctx, NewBatch{
		ProductID: productID, Quantity: decimal.NewFromInt(4),
		CostPrice: decimal.NewFromInt(50), SellingPrice: decimal.NewFromInt(100),
		Source: "purchase",
	})
	must(t, err)
	lotB, err = repo.InsertBatch(ctx, NewBatch{
		ProductID: productID, Quantity: decimal.NewFromInt(10),
		CostPrice: decimal.NewFromInt(60), SellingPrice: decimal.NewFromInt(120),
		Source: "purchase",
	})
	must(t, err)
	return repo, productID, lotA, lotB
}

// The defect this whole feature exists to fix: the old lot must still offer its
// own price after the shelf price moved on.
func TestPriceOptionsOffersEachLotsOwnPrice(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck // the whole point: leave no trace

	repo, productID, lotA, _ := twoPricedLots(t, ctx, tx)

	opts, err := repo.PriceOptions(ctx, productID)
	must(t, err)
	if len(opts) != 2 {
		t.Fatalf("got %d price options, want 2", len(opts))
	}
	// FEFO order: neither lot has an expiry, so the older id comes first — that
	// is the one a blind Enter at the till would take.
	if opts[0].BatchID != lotA {
		t.Errorf("first option is batch %d, want the older lot %d", opts[0].BatchID, lotA)
	}
	if !opts[0].Price.Equal(decimal.NewFromInt(100)) {
		t.Errorf("old lot offers %s, want 100 (the price on its sticker)", opts[0].Price)
	}
	if !opts[1].Price.Equal(decimal.NewFromInt(120)) {
		t.Errorf("new lot offers %s, want 120", opts[1].Price)
	}
	if !opts[0].OwnPrice {
		t.Error("old lot should be flagged as carrying its own price")
	}
}

// A lot with no price of its own must follow the product — this is the sentinel
// that keeps every pre-existing batch behaving exactly as it did before.
func TestPriceOptionsZeroFollowsTheProduct(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	repo := NewRepository(tx)
	var categoryID, unitID, productID int64
	must(t, tx.GetContext(ctx, &categoryID, `SELECT id FROM categories LIMIT 1`))
	must(t, tx.GetContext(ctx, &unitID, `SELECT id FROM units LIMIT 1`))
	must(t, tx.GetContext(ctx, &productID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price)
		VALUES ('TEST sentinel lot', $1, $2, 60, 175) RETURNING id`, categoryID, unitID))
	_, err = repo.InsertBatch(ctx, NewBatch{
		ProductID: productID, Quantity: decimal.NewFromInt(3),
		CostPrice: decimal.NewFromInt(60), Source: "opening",
	})
	must(t, err)

	opts, err := repo.PriceOptions(ctx, productID)
	must(t, err)
	if len(opts) != 1 {
		t.Fatalf("got %d options, want 1", len(opts))
	}
	if !opts[0].Price.Equal(decimal.NewFromInt(175)) {
		t.Errorf("price %s, want the product's 175", opts[0].Price)
	}
	if opts[0].OwnPrice {
		t.Error("a lot with no price of its own must not be flagged as having one")
	}
}

// Selling the picked lot must drain THAT lot and leave the other alone —
// otherwise the customer pays the old price while the new stock disappears.
func TestDepleteBatchDrainsOnlyThePickedLot(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	repo, _, lotA, lotB := twoPricedLots(t, ctx, tx)

	cost, err := repo.DepleteBatch(ctx, lotA, decimal.NewFromInt(4))
	must(t, err)
	if !cost.Equal(decimal.NewFromInt(50)) {
		t.Errorf("COGS %s, want the picked lot's own cost 50", cost)
	}

	var remA, remB decimal.Decimal
	must(t, tx.GetContext(ctx, &remA, `SELECT qty_remaining FROM stock_batches WHERE id = $1`, lotA))
	must(t, tx.GetContext(ctx, &remB, `SELECT qty_remaining FROM stock_batches WHERE id = $1`, lotB))
	if !remA.IsZero() {
		t.Errorf("picked lot has %s left, want 0", remA)
	}
	if !remB.Equal(decimal.NewFromInt(10)) {
		t.Errorf("the other lot has %s left, want an untouched 10", remB)
	}
}

// A batch id arriving from the till is untrusted input: one belonging to a
// different product must be refused, not silently drained.
func TestLockBatchRefusesAnotherProductsLot(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	repo, productID, lotA, _ := twoPricedLots(t, ctx, tx)

	if _, err := repo.LockBatch(ctx, lotA, productID); err != nil {
		t.Fatalf("locking the product's own lot: %v", err)
	}
	var otherID int64
	must(t, tx.GetContext(ctx, &otherID,
		`SELECT id FROM products WHERE id <> $1 LIMIT 1`, productID))
	if _, err := repo.LockBatch(ctx, lotA, otherID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("locking another product's lot returned %v, want sql.ErrNoRows", err)
	}
}

// Products whose lots agree on price must not appear — that is what keeps the
// prompt invisible until per-lot prices are actually in use.
func TestMultiPriceProductsOnlyListsGenuineDisagreements(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	repo, productID, _, _ := twoPricedLots(t, ctx, tx)

	// A second product whose two lots agree (both follow the product's price).
	var categoryID, unitID, agreeID int64
	must(t, tx.GetContext(ctx, &categoryID, `SELECT id FROM categories LIMIT 1`))
	must(t, tx.GetContext(ctx, &unitID, `SELECT id FROM units LIMIT 1`))
	must(t, tx.GetContext(ctx, &agreeID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price)
		VALUES ('TEST agreeing lots', $1, $2, 10, 30) RETURNING id`, categoryID, unitID))
	for range 2 {
		_, err = repo.InsertBatch(ctx, NewBatch{
			ProductID: agreeID, Quantity: decimal.NewFromInt(5),
			CostPrice: decimal.NewFromInt(10), Source: "purchase",
		})
		must(t, err)
	}

	all, err := repo.MultiPriceProducts(ctx)
	must(t, err)
	if len(all[productID]) != 2 {
		t.Errorf("the two-priced product has %d options, want 2", len(all[productID]))
	}
	if _, listed := all[agreeID]; listed {
		t.Error("a product whose lots agree on price must not prompt")
	}
}

// EffectivePrice is the Go half of the sentinel rule; the SQL half is exercised
// by the tests above. They must agree.
func TestEffectivePriceMatchesTheSQLRule(t *testing.T) {
	own := Batch{SellingPrice: d("100")}
	if got := own.EffectivePrice(d("120")); !got.Equal(d("100")) {
		t.Errorf("lot with its own price gave %s, want 100", got)
	}
	follows := Batch{SellingPrice: decimal.Zero}
	if got := follows.EffectivePrice(d("120")); !got.Equal(d("120")) {
		t.Errorf("lot with no price of its own gave %s, want the product's 120", got)
	}
}

// testDB opens the dev database, skipping when there isn't one.
func testDB(t *testing.T) *sqlx.DB {
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

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Removing stock — a write-off, staff taking something, or a count correction —
// must be able to name the lot it came from. Taking the shortfall off the oldest
// lot is a guess, and once lots carry their own prices a wrong guess leaves the
// books claiming stock at a price the shop no longer has.
func TestDepleteChosenTakesFromTheNamedLot(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	repo, productID, lotA, lotB := twoPricedLots(t, ctx, tx)

	// Name the NEWER lot: the older one must be left completely alone.
	cost, err := repo.depleteChosen(ctx, productID, lotB, decimal.NewFromInt(3))
	must(t, err)
	if !cost.Equal(decimal.NewFromInt(60)) {
		t.Errorf("cost %s, want the named lot's 60", cost)
	}
	var remA, remB decimal.Decimal
	must(t, tx.GetContext(ctx, &remA, `SELECT qty_remaining FROM stock_batches WHERE id=$1`, lotA))
	must(t, tx.GetContext(ctx, &remB, `SELECT qty_remaining FROM stock_batches WHERE id=$1`, lotB))
	if !remA.Equal(decimal.NewFromInt(4)) {
		t.Errorf("older lot has %s left, want an untouched 4", remA)
	}
	if !remB.Equal(decimal.NewFromInt(7)) {
		t.Errorf("named lot has %s left, want 7", remB)
	}
}

// No lot named keeps the old behaviour exactly: oldest first.
func TestDepleteChosenFallsBackToFEFO(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	repo, productID, lotA, lotB := twoPricedLots(t, ctx, tx)

	if _, err := repo.depleteChosen(ctx, productID, 0, decimal.NewFromInt(3)); err != nil {
		t.Fatal(err)
	}
	var remA, remB decimal.Decimal
	must(t, tx.GetContext(ctx, &remA, `SELECT qty_remaining FROM stock_batches WHERE id=$1`, lotA))
	must(t, tx.GetContext(ctx, &remB, `SELECT qty_remaining FROM stock_batches WHERE id=$1`, lotB))
	if !remA.Equal(decimal.NewFromInt(1)) || !remB.Equal(decimal.NewFromInt(10)) {
		t.Errorf("FEFO took A=%s B=%s, want the oldest drained first (A=1, B=10)", remA, remB)
	}
}

// A lot with less in it than is being removed must be refused, not silently
// over-drawn into another lot.
func TestDepleteChosenRefusesMoreThanTheLotHolds(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	repo, productID, lotA, _ := twoPricedLots(t, ctx, tx)
	if _, err := repo.depleteChosen(ctx, productID, lotA, decimal.NewFromInt(99)); err == nil {
		t.Error("removing more than the lot holds was allowed")
	}
}
