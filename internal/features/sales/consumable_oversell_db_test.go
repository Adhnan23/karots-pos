package sales

import (
	"context"
	"strings"
	"testing"

	"karots-pos/internal/apperr"

	"github.com/shopspring/decimal"
)

// Proves the found-at-till path for a SERVICE line's consumable (the copy-job
// paper case): the service itself holds no stock, but its component (paper) is
// short. Service.Create commits its own tx, so this seeds throwaway rows and
// deletes them afterwards. The dev DB is disposable. Reuses salesTestDB /
// mustSales from oversell_db_test.go (same package).

func TestConsumableFoundAtTillCorrectsUpAndSells(t *testing.T) {
	conn := salesTestDB(t)
	defer conn.Close()
	ctx := context.Background()

	var categoryID, unitID, cashierID, sinceSaleID, serviceID, paperID int64
	mustSales(t, conn.GetContext(ctx, &categoryID, `SELECT id FROM categories LIMIT 1`))
	mustSales(t, conn.GetContext(ctx, &unitID, `SELECT id FROM units LIMIT 1`))
	mustSales(t, conn.GetContext(ctx, &cashierID, `SELECT id FROM users LIMIT 1`))
	mustSales(t, conn.GetContext(ctx, &sinceSaleID, `SELECT COALESCE(MAX(id),0) FROM sales`))
	mustSales(t, conn.GetContext(ctx, &serviceID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price, is_service)
		VALUES ('TEST copy service', $1, $2, 0, 5, true) RETURNING id`, categoryID, unitID))
	mustSales(t, conn.GetContext(ctx, &paperID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price)
		VALUES ('TEST paper', $1, $2, 2, 0) RETURNING id`, categoryID, unitID))

	defer func() {
		conn.ExecContext(ctx, `DELETE FROM sales WHERE id > $1`, sinceSaleID)                                //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM stock_movements WHERE product_id IN ($1,$2)`, serviceID, paperID) //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM stock_batches WHERE product_id IN ($1,$2)`, serviceID, paperID)   //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM stock WHERE product_id IN ($1,$2)`, serviceID, paperID)           //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM products WHERE id IN ($1,$2)`, serviceID, paperID)                //nolint:errcheck
	}()

	svc := NewService(conn)

	// Paper is at 0 on hand; approve the oversell on the service line.
	_, err := svc.Create(ctx, CreateInput{
		SaleType: "retail",
		Items: []ItemInput{{
			ProductID:     serviceID,
			Quantity:      "1",
			PriceOverride: "5",
			AllowOversell: true,
			Components:    []ServiceComponent{{ProductID: paperID, Quantity: "1"}},
		}},
		Payments: []PaymentInput{{Method: "cash", Amount: "5"}},
	}, cashierID)
	mustSales(t, err)

	var paperOnHand decimal.Decimal
	mustSales(t, conn.GetContext(ctx, &paperOnHand, `SELECT quantity FROM stock WHERE product_id = $1`, paperID))
	if !paperOnHand.Equal(decimal.Zero) {
		t.Errorf("paper on hand = %s, want 0 (found +1 then consumed -1)", paperOnHand)
	}

	var adjustCount int
	mustSales(t, conn.GetContext(ctx, &adjustCount, `SELECT count(*) FROM stock_movements WHERE product_id = $1 AND type = 'adjust' AND quantity > 0`, paperID))
	if adjustCount != 1 {
		t.Errorf("paper found (+adjust) movements = %d, want 1", adjustCount)
	}
}

func TestConsumableWithoutApprovalRefusesWithCode(t *testing.T) {
	conn := salesTestDB(t)
	defer conn.Close()
	ctx := context.Background()

	var categoryID, unitID, cashierID, serviceID, paperID int64
	mustSales(t, conn.GetContext(ctx, &categoryID, `SELECT id FROM categories LIMIT 1`))
	mustSales(t, conn.GetContext(ctx, &unitID, `SELECT id FROM units LIMIT 1`))
	mustSales(t, conn.GetContext(ctx, &cashierID, `SELECT id FROM users LIMIT 1`))
	mustSales(t, conn.GetContext(ctx, &serviceID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price, is_service)
		VALUES ('TEST copy service blocked', $1, $2, 0, 5, true) RETURNING id`, categoryID, unitID))
	mustSales(t, conn.GetContext(ctx, &paperID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price)
		VALUES ('TEST paper blocked', $1, $2, 2, 0) RETURNING id`, categoryID, unitID))
	defer func() {
		conn.ExecContext(ctx, `DELETE FROM stock_movements WHERE product_id IN ($1,$2)`, serviceID, paperID) //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM stock_batches WHERE product_id IN ($1,$2)`, serviceID, paperID)   //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM stock WHERE product_id IN ($1,$2)`, serviceID, paperID)           //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM products WHERE id IN ($1,$2)`, serviceID, paperID)                //nolint:errcheck
	}()

	svc := NewService(conn)
	_, err := svc.Create(ctx, CreateInput{
		SaleType: "retail",
		Items: []ItemInput{{
			ProductID:     serviceID,
			Quantity:      "1",
			PriceOverride: "5",
			AllowOversell: false,
			Components:    []ServiceComponent{{ProductID: paperID, Quantity: "1"}},
		}},
		Payments: []PaymentInput{{Method: "cash", Amount: "5"}},
	}, cashierID)
	if err == nil {
		t.Fatal("a short consumable without approval should be refused")
	}
	ae, ok := apperr.As(err)
	if !ok || ae.Code != "CONSUMABLE_SHORT" {
		t.Errorf("error = %v, want an *AppError with code CONSUMABLE_SHORT", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "consumable") {
		t.Errorf("message = %q, want it to mention the consumable", err.Error())
	}
}
