package products

import (
	"context"

	"karots-pos/internal/apperr"

	"github.com/shopspring/decimal"
)

// Inventory valuation: what the stock on hand is worth, at cost and at retail.
//
// The numbers are aggregated in SQL over the WHOLE catalog rather than summed
// from a page of rows — the report used to sum whatever List() happened to
// return, which silently clamps to 50 products, so a 600-product shop saw ~14%
// of its real stock value presented as the total.
//
// Cost basis is the product's current cost_price (not per-batch cost): it is
// what the owner typed during capture and what they expect to see back.

// ValuationRow is one root category's slice of the inventory.
type ValuationRow struct {
	CategoryID  int64           `db:"category_id"`
	Category    string          `db:"category"`
	SKUs        int             `db:"skus"`
	InStock     int             `db:"in_stock"`
	CostValue   decimal.Decimal `db:"cost_value"`
	RetailValue decimal.Decimal `db:"retail_value"`
	MissingCost int             `db:"missing_cost"`
	NegativeQty int             `db:"negative_qty"`
}

// Valuation is the whole-catalog summary plus its per-category breakdown.
type Valuation struct {
	SKUs        int
	InStock     int
	CostValue   decimal.Decimal
	RetailValue decimal.Decimal
	MissingCost int
	NegativeQty int
	Categories  []ValuationRow
}

// Margin is the potential profit locked up in stock (retail − cost).
func (v Valuation) Margin() decimal.Decimal { return v.RetailValue.Sub(v.CostValue) }

// MarginPct is Margin as a percentage of retail value; zero when nothing is held.
func (v Valuation) MarginPct() decimal.Decimal {
	if v.RetailValue.IsZero() {
		return decimal.Zero
	}
	return v.Margin().Div(v.RetailValue).Mul(decimal.NewFromInt(100)).Round(1)
}

// rootCatsCTE maps every category to the root of its tree, so the breakdown is
// a short list of top-level categories instead of 128 leaves.
const rootCatsCTE = `
	WITH RECURSIVE roots AS (
		SELECT id, id AS root_id, name AS root_name FROM categories WHERE parent_id IS NULL
		UNION ALL
		SELECT c.id, r.root_id, r.root_name FROM categories c JOIN roots r ON c.parent_id = r.id
	)`

func (r *Repository) Valuation(ctx context.Context) ([]ValuationRow, error) {
	var rows []ValuationRow
	err := r.db.SelectContext(ctx, &rows, rootCatsCTE+`
		SELECT rt.root_id AS category_id, rt.root_name AS category,
		       count(*)                                                   AS skus,
		       count(*) FILTER (WHERE COALESCE(s.quantity,0) > 0)         AS in_stock,
		       COALESCE(SUM(COALESCE(s.quantity,0) * p.cost_price), 0)    AS cost_value,
		       COALESCE(SUM(COALESCE(s.quantity,0) * p.selling_price), 0) AS retail_value,
		       count(*) FILTER (WHERE COALESCE(s.quantity,0) > 0 AND COALESCE(p.cost_price,0) = 0) AS missing_cost,
		       count(*) FILTER (WHERE COALESCE(s.quantity,0) < 0)         AS negative_qty
		FROM products p
		JOIN roots rt     ON rt.id = p.category_id
		LEFT JOIN stock s ON s.product_id = p.id
		WHERE p.is_active = true AND p.is_service = false
		GROUP BY rt.root_id, rt.root_name
		ORDER BY cost_value DESC, rt.root_name`)
	return rows, err
}

// ValuationQuery filters the (screen-only) product detail list under the summary.
type ValuationQuery struct {
	CategoryID  *int64 `query:"category_id"`
	IncludeZero bool   `query:"include_zero"`
	Page        int    `query:"page"`
}

func (q *ValuationQuery) Normalize() {
	if q.Page < 1 {
		q.Page = 1
	}
}

// ValuationRows lists the products behind the summary, most valuable first, so
// the first page answers "where is the money?" without paging at all. Passing
// limit <= 0 returns every match (used by the CSV export).
func (r *Repository) ValuationRows(ctx context.Context, q ValuationQuery, limit, offset int) ([]Product, error) {
	var rows []Product
	err := r.db.SelectContext(ctx, &rows, rootCatsCTE+selectProduct+`
		JOIN roots rt ON rt.id = p.category_id
		WHERE p.is_active = true AND p.is_service = false
		  AND ($1::bigint IS NULL OR rt.root_id = $1)
		  AND ($2 = true OR COALESCE(s.quantity,0) <> 0)
		ORDER BY COALESCE(s.quantity,0) * p.cost_price DESC, p.name
		LIMIT NULLIF($3, 0) OFFSET $4`,
		q.CategoryID, q.IncludeZero, limit, offset)
	return rows, err
}

func (r *Repository) ValuationRowCount(ctx context.Context, q ValuationQuery) (int, error) {
	var n int
	err := r.db.GetContext(ctx, &n, rootCatsCTE+`
		SELECT count(*)
		FROM products p
		JOIN roots rt     ON rt.id = p.category_id
		LEFT JOIN stock s ON s.product_id = p.id
		WHERE p.is_active = true AND p.is_service = false
		  AND ($1::bigint IS NULL OR rt.root_id = $1)
		  AND ($2 = true OR COALESCE(s.quantity,0) <> 0)`,
		q.CategoryID, q.IncludeZero)
	return n, err
}

// --- service ---

// Valuation returns whole-catalog stock value plus a per-root-category breakdown.
func (s *Service) Valuation(ctx context.Context) (*Valuation, error) {
	rows, err := s.repo.Valuation(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to value inventory", err)
	}
	v := &Valuation{Categories: rows}
	for _, r := range rows {
		v.SKUs += r.SKUs
		v.InStock += r.InStock
		v.MissingCost += r.MissingCost
		v.NegativeQty += r.NegativeQty
		v.CostValue = v.CostValue.Add(r.CostValue)
		v.RetailValue = v.RetailValue.Add(r.RetailValue)
	}
	return v, nil
}

// ValuationDetail returns page q.Page of the product list behind the summary,
// along with the total number of matching rows. pageSize is supplied by the
// caller so every report shares one page-size setting.
func (s *Service) ValuationDetail(ctx context.Context, q ValuationQuery, pageSize int) ([]Product, int, error) {
	q.Normalize()
	total, err := s.repo.ValuationRowCount(ctx, q)
	if err != nil {
		return nil, 0, apperr.Internal("failed to count inventory rows", err)
	}
	rows, err := s.repo.ValuationRows(ctx, q, pageSize, (q.Page-1)*pageSize)
	if err != nil {
		return nil, 0, apperr.Internal("failed to list inventory rows", err)
	}
	return rows, total, nil
}

// ValuationAll returns every matching row unpaginated, for the CSV export.
func (s *Service) ValuationAll(ctx context.Context, q ValuationQuery) ([]Product, error) {
	q.Normalize()
	rows, err := s.repo.ValuationRows(ctx, q, 0, 0)
	if err != nil {
		return nil, apperr.Internal("failed to list inventory rows", err)
	}
	return rows, nil
}
