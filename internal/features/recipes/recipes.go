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
	       p.name AS component_name, u.abbreviation AS unit_abbr
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
