package recipes

import (
	"context"
	"time"

	"karots-pos/internal/apperr"

	"github.com/shopspring/decimal"
)

// VarianceRow is one ingredient's expected-vs-actual usage over a period.
type VarianceRow struct {
	ComponentName string          `db:"component_name"`
	UnitAbbr      string          `db:"unit_abbr"`
	Expected      decimal.Decimal `db:"expected"`
	Actual        decimal.Decimal `db:"actual"`
	Diff          decimal.Decimal `db:"diff"`
}

// DriftPct is the difference as a percentage of expected usage; zero when
// nothing was expected, so a component used without any recipe does not divide
// by zero. Computed in Go rather than SQL to keep the query's columns and this
// struct's db tags one-to-one.
func (v VarianceRow) DriftPct() decimal.Decimal {
	if v.Expected.IsZero() {
		return decimal.Zero
	}
	return v.Diff.Div(v.Expected).Mul(decimal.NewFromInt(100)).Round(1)
}

// Variance compares what the recipes SAY was consumed against what stock
// actually moved. A yield is an estimate — a bag rated for 50 cups may give 48
// or 53 — so this is the feedback loop that lets the estimate be corrected
// instead of quietly bleeding stock.
//
// Expected: for every sale line of a service with a recipe, the recipe's
// consumption for that line's quantity.
// Actual: the stock movements those sales actually produced.
//
// Both sides are restricted to products that appear in some recipe. Without
// that, every ordinary product sold in the period would surface as pure drift
// (a large "actual" against no "expected"), burying the handful of rows this
// report exists to show.
func (s *Service) Variance(ctx context.Context, from, to time.Time) ([]VarianceRow, error) {
	var rows []VarianceRow
	err := s.db.SelectContext(ctx, &rows, `
		WITH components AS (
			SELECT DISTINCT component_product_id AS pid FROM product_recipes
		),
		expected AS (
			SELECT r.component_product_id AS pid,
			       SUM(
			         CASE WHEN r.yield_units IS NOT NULL
			              THEN (si.quantity - si.returned_qty) / r.yield_units
			              ELSE (si.quantity - si.returned_qty) * r.qty_per_unit END
			       ) AS qty
			FROM sale_items si
			JOIN sales sa ON sa.id = si.sale_id
			JOIN product_recipes r ON r.product_id = si.product_id
			WHERE sa.status <> 'void' AND sa.created_at >= $1 AND sa.created_at < $2
			GROUP BY r.component_product_id
		),
		actual AS (
			SELECT m.product_id AS pid, -SUM(m.quantity) AS qty
			FROM stock_movements m
			JOIN components cp ON cp.pid = m.product_id
			WHERE m.type = 'sale' AND m.created_at >= $1 AND m.created_at < $2
			GROUP BY m.product_id
		)
		SELECT p.name AS component_name, u.abbreviation AS unit_abbr,
		       COALESCE(e.qty,0) AS expected,
		       COALESCE(a.qty,0) AS actual,
		       COALESCE(a.qty,0) - COALESCE(e.qty,0) AS diff
		FROM expected e
		FULL JOIN actual a ON a.pid = e.pid
		JOIN products p ON p.id = COALESCE(e.pid, a.pid)
		JOIN units u    ON u.id = p.unit_id
		WHERE COALESCE(e.qty,0) <> 0 OR COALESCE(a.qty,0) <> 0
		ORDER BY abs(COALESCE(a.qty,0) - COALESCE(e.qty,0)) DESC`,
		from, to)
	if err != nil {
		return nil, apperr.Internal("failed to compute recipe variance", err)
	}
	return rows, nil
}
