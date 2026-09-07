package web

import (
	"context"
	"testing"

	"karots-pos/internal/features/purchases"

	"github.com/shopspring/decimal"
)

// TestReceivingDiscountsLowerRealCost proves the whole chain end-to-end against
// a real database (rolled back): free units + a per-item discount + a whole-bill
// discount all flow into the received lot's COST, while the product's master
// ("front") cost stays at the list price the supplier quoted.
//
// Scenario: buy 10 @ 100, +2 free, 10% off the line, then 10% off the bill.
//
//	line gross      = 10 × 100            = 1000
//	item discount   = 10% of 1000         =  100  → line net = 900
//	bill discount   = 10% of 900          =   90  → owed total = 810
//	cost basis      = 900 × 810/900       =  810  (bill discount spread pro-rata)
//	units received  = 10 + 2 free         =   12
//	real lot cost   = 810 / 12            =   67.50  ← what a sale actually costs
//	master cost     = list price          = 100.00  ← unchanged, shown up front
func TestReceivingDiscountsLowerRealCost(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()

	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // leave no trace

	var supplierID int64
	must(t, tx.GetContext(ctx, &supplierID,
		`INSERT INTO suppliers (name) VALUES ('TEST discount cost') RETURNING id`))
	productID := testProduct(t, ctx, tx)

	detail, err := purchases.CreateTx(ctx, tx, purchases.CreateInput{
		SupplierID:   supplierID,
		Discount:     "10",
		DiscountType: "percent",
		Items: []purchases.ItemInput{{
			ProductID: productID, Quantity: "10", FreeQty: "2",
			CostPrice: "100", SellingPrice: "150",
			Discount: "10", DiscountType: "percent",
		}},
	}, 1)
	if err != nil {
		t.Fatalf("receiving the delivery: %v", err)
	}

	// Header: net subtotal 900, bill discount 90, total owed 810.
	eq(t, "subtotal", detail.Purchase.Subtotal, "900")
	eq(t, "bill discount", detail.Purchase.Discount, "90")
	eq(t, "total owed", detail.Purchase.Total, "810")

	// The stock batch carries the REAL cost: 810 spread over 12 units = 67.50.
	var got struct {
		Cost decimal.Decimal `db:"cost_price"`
		Qty  decimal.Decimal `db:"qty_received"`
	}
	must(t, tx.GetContext(ctx, &got,
		`SELECT sb.cost_price, sb.qty_received
		   FROM stock_batches sb
		   JOIN purchase_items pi ON pi.id = sb.purchase_item_id
		  WHERE pi.purchase_id = $1`, detail.Purchase.ID))
	eq(t, "batch qty (paid + free)", got.Qty, "12")
	eq(t, "REAL per-unit lot cost", got.Cost, "67.5")

	// The product's master cost stays what the supplier quoted — shown up front,
	// even though the goods actually cost less.
	var masterCost decimal.Decimal
	must(t, tx.GetContext(ctx, &masterCost,
		`SELECT cost_price FROM products WHERE id = $1`, productID))
	eq(t, "master (front) cost", masterCost, "100")

	// The supplier is owed the discounted total, not the gross.
	var balance decimal.Decimal
	must(t, tx.GetContext(ctx, &balance,
		`SELECT outstanding_balance FROM suppliers WHERE id = $1`, supplierID))
	eq(t, "supplier balance", balance, "810")
}

func eq(t *testing.T, label string, got decimal.Decimal, want string) {
	t.Helper()
	if !got.Equal(decimal.RequireFromString(want)) {
		t.Errorf("%s = %s, want %s", label, got, want)
	}
}
