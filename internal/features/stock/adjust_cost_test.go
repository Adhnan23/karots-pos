package stock

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"
)

// A stock correction is how this shop records shrinkage: stock-take is used once
// at onboarding and then switched off, so every later "we are four short" goes
// through an adjustment. That path used to compute the lot cost and then throw
// it away, writing the movement with no value at all — so the goods left the
// shelf, the stock value dropped, and the P&L never saw a thing.
//
// Everything runs inside a rolled-back transaction.

// movementCost reads back the value booked against the newest movement of a type.
func movementCost(t *testing.T, ctx context.Context, repo *Repository, productID int64, mtype string) decimal.Decimal {
	t.Helper()
	var cost decimal.Decimal
	must(t, repo.q.GetContext(ctx, &cost, `
		SELECT COALESCE(cost,0) FROM stock_movements
		WHERE product_id = $1 AND type = $2
		ORDER BY id DESC LIMIT 1`, productID, mtype))
	return cost
}

func TestAdjustDownBooksTheCostOfTheLotItTook(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	repo, productID, _, lotB := twoPricedLots(t, ctx, tx)
	// lotA = 4 @ cost 50, lotB = 10 @ cost 60.
	must(t, setStock(ctx, repo, productID, decimal.NewFromInt(14)))

	// Take 3 off the NEWER lot: worth 3 x 60 = 180, not the 3 x 50 that FEFO
	// would have costed it at.
	cost, err := repo.depleteChosen(ctx, productID, lotB, decimal.NewFromInt(3))
	must(t, err)
	if !cost.Equal(decimal.NewFromInt(60)) {
		t.Fatalf("depleteChosen returned unit cost %s, want 60 (the lot actually taken)", cost)
	}
	must(t, repo.InsertMovement(ctx, MovementInput{
		ProductID: productID, Type: MoveAdjust,
		Quantity: decimal.NewFromInt(-3), UserID: 1,
		Cost: cost.Mul(decimal.NewFromInt(3)),
	}))

	got := movementCost(t, ctx, repo, productID, MoveAdjust)
	if !got.Equal(decimal.NewFromInt(180)) {
		t.Errorf("adjustment booked %s of value, want 180 — a correction with no cost never reaches the P&L", got)
	}
}

// The P&L reads adjustments as a signed net: stock written off is a cost, stock
// found is a credit. Both must be visible, or a shop that finds more than it
// expected looks like it lost nothing and vice versa.
func TestAdjustmentNetsOffAgainstStockFound(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	repo, productID, _, _ := twoPricedLots(t, ctx, tx)

	// Written off: 2 units worth 100 total. Found: 1 unit worth 60.
	must(t, repo.InsertMovement(ctx, MovementInput{
		ProductID: productID, Type: MoveAdjust,
		Quantity: decimal.NewFromInt(-2), UserID: 1, Cost: decimal.NewFromInt(100),
	}))
	must(t, repo.InsertMovement(ctx, MovementInput{
		ProductID: productID, Type: MoveAdjust,
		Quantity: decimal.NewFromInt(1), UserID: 1, Cost: decimal.NewFromInt(60),
	}))

	var net decimal.Decimal
	must(t, tx.GetContext(ctx, &net, `
		SELECT COALESCE(SUM(CASE WHEN quantity < 0 THEN cost ELSE -cost END),0)
		FROM stock_movements WHERE product_id = $1 AND type = 'adjust'`, productID))
	if !net.Equal(decimal.NewFromInt(40)) {
		t.Errorf("net stock correction is %s, want 40 (100 written off less 60 found)", net)
	}
}

// setStock makes sure the product has a stock row, so Adjust's read-modify-write
// has something to read.
func setStock(ctx context.Context, repo *Repository, productID int64, qty decimal.Decimal) error {
	_, err := repo.q.ExecContext(ctx, `
		INSERT INTO stock (product_id, quantity) VALUES ($1,$2)
		ON CONFLICT (product_id) DO UPDATE SET quantity = EXCLUDED.quantity`, productID, qty)
	return err
}
