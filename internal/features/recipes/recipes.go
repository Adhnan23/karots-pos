package recipes

import (
	"context"

	"karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
)

type Repository struct{ q db.Queryer }

func NewRepository(q db.Queryer) *Repository { return &Repository{q: q} }

const selectComponents = `
	SELECT r.component_product_id, r.qty_per_unit, r.yield_units, r.whole_units,
	       p.name AS component_name, u.abbreviation AS unit_abbr, p.cost_price
	FROM product_recipes r
	JOIN products p ON p.id = r.component_product_id
	JOIN units u    ON u.id = p.unit_id`

// For returns a product's recipe, empty when it has none.
func (r *Repository) For(ctx context.Context, productID int64) ([]Component, error) {
	var cs []Component
	err := r.q.SelectContext(ctx, &cs, selectComponents+`
		WHERE r.product_id = $1 ORDER BY p.name`, productID)
	return cs, err
}

// Replace swaps a product's whole recipe in one transaction. Editing a recipe is
// always a wholesale replacement: a partial update could leave a component the
// user deleted still being consumed on the next sale.
func (r *Repository) Replace(ctx context.Context, tx *sqlx.Tx, productID int64, cs []Component) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM product_recipes WHERE product_id = $1`, productID); err != nil {
		return err
	}
	for _, c := range cs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO product_recipes
				(product_id, component_product_id, qty_per_unit, yield_units, whole_units)
			VALUES ($1,$2,$3,$4,$5)`,
			productID, c.ComponentProductID, c.QtyPerUnit, c.YieldUnits, c.WholeUnits); err != nil {
			return err
		}
	}
	return nil
}

// CostsFor returns a product's non-stock cost lines, empty when it has none.
func (r *Repository) CostsFor(ctx context.Context, productID int64) ([]CostLine, error) {
	var ls []CostLine
	err := r.q.SelectContext(ctx, &ls, `
		SELECT id, label, cost_per_unit FROM product_recipe_costs
		WHERE product_id = $1 ORDER BY label`, productID)
	return ls, err
}

// ReplaceCosts swaps a product's whole cost-line set, for the same reason
// Replace does: a partial update could leave a deleted line still being costed.
func (r *Repository) ReplaceCosts(ctx context.Context, tx *sqlx.Tx, productID int64, ls []CostLine) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM product_recipe_costs WHERE product_id = $1`, productID); err != nil {
		return err
	}
	for _, l := range ls {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO product_recipe_costs (product_id, label, cost_per_unit)
			VALUES ($1,$2,$3)`, productID, l.Label, l.CostPerUnit); err != nil {
			return err
		}
	}
	return nil
}

// Summaries returns the per-unit cost split for every product that has a recipe
// or a cost line, so the services list can show costs without a query per row.
// The arithmetic runs through CostPerUnit rather than being re-expressed in SQL,
// so a yield component is divided exactly as it is everywhere else.
func (r *Repository) Summaries(ctx context.Context) (map[int64]Costs, error) {
	var comps []struct {
		ProductID int64 `db:"product_id"`
		Component
	}
	// Spelled out rather than built from selectComponents: that constant ends
	// with its JOINs, so anything appended lands in the FROM clause.
	if err := r.q.SelectContext(ctx, &comps, `
		SELECT r.product_id, r.component_product_id, r.qty_per_unit, r.yield_units,
		       r.whole_units, p.name AS component_name, u.abbreviation AS unit_abbr,
		       p.cost_price
		FROM product_recipes r
		JOIN products p ON p.id = r.component_product_id
		JOIN units u    ON u.id = p.unit_id`); err != nil {
		return nil, err
	}
	var costs []struct {
		ProductID int64 `db:"product_id"`
		CostLine
	}
	if err := r.q.SelectContext(ctx, &costs,
		`SELECT product_id, id, label, cost_per_unit FROM product_recipe_costs`); err != nil {
		return nil, err
	}

	byProduct := map[int64][]Component{}
	for _, c := range comps {
		byProduct[c.ProductID] = append(byProduct[c.ProductID], c.Component)
	}
	costsBy := map[int64][]CostLine{}
	for _, l := range costs {
		costsBy[l.ProductID] = append(costsBy[l.ProductID], l.CostLine)
	}

	out := make(map[int64]Costs, len(byProduct))
	for id := range byProduct {
		out[id] = CostPerUnit(byProduct[id], costsBy[id])
	}
	for id := range costsBy {
		if _, ok := out[id]; !ok {
			out[id] = CostPerUnit(nil, costsBy[id])
		}
	}
	return out, nil
}

// Counts returns the number of ingredients per service product, so a list can
// show "3 ingredients" without loading every recipe.
func (r *Repository) Counts(ctx context.Context) (map[int64]int, error) {
	var rows []struct {
		ProductID int64 `db:"product_id"`
		N         int   `db:"n"`
	}
	if err := r.q.SelectContext(ctx, &rows,
		`SELECT product_id, COUNT(*) AS n FROM product_recipes GROUP BY product_id`); err != nil {
		return nil, err
	}
	out := make(map[int64]int, len(rows))
	for _, r := range rows {
		out[r.ProductID] = r.N
	}
	return out, nil
}
