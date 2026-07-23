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
// Cost basis is the LOTS the stock is actually made of, not the product's
// current price. Valuing every unit at the newest price silently marks up stock
// that was bought cheaper: 22 bottles bought at 780 sitting under a product
// repriced to 1000 were reported as 4,840 more than the shop paid, and the same
// figure computed from lots on the Net Position page disagreed by that much.
// Anything the lots do not account for still falls back to the product price, so
// stock recorded before lots existed keeps its old valuation rather than
// vanishing.
const (
	// lotValueJoin attaches each product's live-lot totals. LATERAL so the retail
	// side can resolve the per-lot price sentinel (0 = follow the product).
	lotValueJoin = `
		LEFT JOIN LATERAL (
		    SELECT SUM(b.qty_remaining)                    AS lot_qty,
		           SUM(b.qty_remaining * b.cost_price)     AS lot_cost,
		           SUM(b.qty_remaining * CASE WHEN b.selling_price > 0
		                                      THEN b.selling_price
		                                      ELSE p.selling_price END) AS lot_retail
		    FROM stock_batches b
		    WHERE b.product_id = p.id AND b.qty_remaining > 0
		) lv ON TRUE`

	costValueSQL = `COALESCE(SUM(
		COALESCE(lv.lot_cost,0)
		+ (COALESCE(st.quantity,0) - COALESCE(lv.lot_qty,0)) * COALESCE(p.cost_price,0)),0)`

	retailValueSQL = `COALESCE(SUM(
		COALESCE(lv.lot_retail,0)
		+ (COALESCE(st.quantity,0) - COALESCE(lv.lot_qty,0)) * COALESCE(p.selling_price,0)),0)`

	// missingCostSQL counts stock that is genuinely unvalued — neither its lots
	// nor the product carry a cost — rather than every product whose header cost
	// happens to be blank.
	missingCostSQL = `count(p.id) FILTER (
		WHERE COALESCE(st.quantity,0) > 0
		  AND COALESCE(lv.lot_cost,0) = 0 AND COALESCE(p.cost_price,0) = 0)`
)

// ValuationNode is one row of the category breakdown: a child category rolled
// up over its whole subtree, or — when Direct is true — the products filed
// directly on the category being viewed, which belong to no child.
type ValuationNode struct {
	CategoryID  int64
	Name        string
	Direct      bool
	HasChildren bool
	SKUs        int
	InStock     int
	Units       UnitTally
	CostValue   decimal.Decimal
	RetailValue decimal.Decimal
}

// Valuation is one branch of the catalogue: its own totals, the child rows
// beneath it, and the path back to the root.
type Valuation struct {
	SKUs        int
	InStock     int
	Units       UnitTally
	CostValue   decimal.Decimal
	RetailValue decimal.Decimal
	MissingCost int
	NegativeQty int
	Children    []ValuationNode
	Breadcrumb  []Crumb
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

// subtreeCTE maps every descendant category to the child-of-current it sits
// under, so grouping by `top` gives one rolled-up row per child. $1 NULL means
// the whole catalogue, whose children are the root categories.
//
// This replaces an earlier CTE that mapped every category to its ROOT, which
// made the report permanently blind below the top level: its category filter
// compared against root_id, so passing a sub-category's id matched nothing.
const subtreeCTE = `
	WITH RECURSIVE sub AS (
		SELECT id, id AS top FROM categories
		 WHERE ($1::bigint IS NULL AND parent_id IS NULL) OR parent_id = $1
	  UNION ALL
		SELECT c.id, s.top FROM categories c JOIN sub s ON c.parent_id = s.id
	)`

// branchCTE is the same walk seeded with the node itself, for totals: it
// includes products filed directly on an interior category, which belong to
// that branch but sit in none of its children.
const branchCTE = `
	WITH RECURSIVE sub AS (
		SELECT id FROM categories
		 WHERE ($1::bigint IS NULL AND parent_id IS NULL) OR id = $1
	  UNION ALL
		SELECT c.id FROM categories c JOIN sub s ON c.parent_id = s.id
	)`

// childRow is one (child category × unit) group, folded into ValuationNode below.
type childRow struct {
	CategoryID  int64           `db:"category_id"`
	Name        string          `db:"name"`
	HasChildren bool            `db:"has_children"`
	UnitAbbr    string          `db:"unit_abbr"`
	SKUs        int             `db:"skus"`
	InStock     int             `db:"in_stock"`
	Units       decimal.Decimal `db:"units"`
	CostValue   decimal.Decimal `db:"cost_value"`
	RetailValue decimal.Decimal `db:"retail_value"`
}

// ValuationChildren returns one rolled-up row per child of categoryID (or per
// root category when nil). Quantities come back grouped by unit so the tally
// can keep units apart; everything else sums across those groups, which is safe
// because a product has exactly one unit.
func (r *Repository) ValuationChildren(ctx context.Context, categoryID *int64) ([]ValuationNode, error) {
	var rows []childRow
	if err := r.db.SelectContext(ctx, &rows, subtreeCTE+`
		SELECT s.top AS category_id, tc.name,
		       EXISTS (SELECT 1 FROM categories k WHERE k.parent_id = s.top) AS has_children,
		       COALESCE(u.abbreviation, '')                               AS unit_abbr,
		       count(p.id)                                                AS skus,
		       count(p.id) FILTER (WHERE COALESCE(st.quantity,0) > 0)     AS in_stock,
		       COALESCE(SUM(st.quantity), 0)                              AS units,
		       `+costValueSQL+`                                           AS cost_value,
		       `+retailValueSQL+`                                         AS retail_value
		FROM sub s
		JOIN categories tc ON tc.id = s.top
		LEFT JOIN products p ON p.category_id = s.id AND p.is_active AND NOT p.is_service
		LEFT JOIN units u    ON u.id = p.unit_id
		LEFT JOIN stock st   ON st.product_id = p.id`+lotValueJoin+`
		GROUP BY s.top, tc.name, u.abbreviation
		ORDER BY tc.name`, categoryID); err != nil {
		return nil, err
	}
	return foldChildren(rows), nil
}

// foldChildren collapses the per-unit groups into one node per category,
// preserving input order so the caller's ORDER BY still decides the layout.
func foldChildren(rows []childRow) []ValuationNode {
	idx := make(map[int64]int, len(rows))
	out := make([]ValuationNode, 0, len(rows))
	for _, r := range rows {
		i, ok := idx[r.CategoryID]
		if !ok {
			out = append(out, ValuationNode{
				CategoryID: r.CategoryID, Name: r.Name, HasChildren: r.HasChildren,
			})
			i = len(out) - 1
			idx[r.CategoryID] = i
		}
		n := &out[i]
		n.SKUs += r.SKUs
		n.InStock += r.InStock
		n.CostValue = n.CostValue.Add(r.CostValue)
		n.RetailValue = n.RetailValue.Add(r.RetailValue)
		if r.UnitAbbr != "" && r.Units.IsPositive() {
			n.Units = append(n.Units, UnitQty{Abbr: r.UnitAbbr, Qty: r.Units})
		}
	}
	return out
}

// ValuationBranch totals the whole subtree rooted at categoryID, including
// products filed directly on it.
func (r *Repository) ValuationBranch(ctx context.Context, categoryID *int64) (Valuation, error) {
	var rows []struct {
		UnitAbbr    string          `db:"unit_abbr"`
		SKUs        int             `db:"skus"`
		InStock     int             `db:"in_stock"`
		Units       decimal.Decimal `db:"units"`
		CostValue   decimal.Decimal `db:"cost_value"`
		RetailValue decimal.Decimal `db:"retail_value"`
		MissingCost int             `db:"missing_cost"`
		NegativeQty int             `db:"negative_qty"`
	}
	err := r.db.SelectContext(ctx, &rows, branchCTE+`
		SELECT COALESCE(u.abbreviation, '')                           AS unit_abbr,
		       count(p.id)                                            AS skus,
		       count(p.id) FILTER (WHERE COALESCE(st.quantity,0) > 0) AS in_stock,
		       COALESCE(SUM(st.quantity), 0)                          AS units,
		       `+costValueSQL+`                                      AS cost_value,
		       `+retailValueSQL+`                                    AS retail_value,
		       `+missingCostSQL+`                                    AS missing_cost,
		       count(p.id) FILTER (WHERE COALESCE(st.quantity,0) < 0) AS negative_qty
		FROM sub s
		LEFT JOIN products p ON p.category_id = s.id AND p.is_active AND NOT p.is_service
		LEFT JOIN units u    ON u.id = p.unit_id
		LEFT JOIN stock st   ON st.product_id = p.id`+lotValueJoin+`
		GROUP BY u.abbreviation`, categoryID)
	if err != nil {
		return Valuation{}, err
	}
	var v Valuation
	for _, row := range rows {
		v.SKUs += row.SKUs
		v.InStock += row.InStock
		v.MissingCost += row.MissingCost
		v.NegativeQty += row.NegativeQty
		v.CostValue = v.CostValue.Add(row.CostValue)
		v.RetailValue = v.RetailValue.Add(row.RetailValue)
		if row.UnitAbbr != "" && row.Units.IsPositive() {
			v.Units = append(v.Units, UnitQty{Abbr: row.UnitAbbr, Qty: row.Units})
		}
	}
	return v, nil
}

// directNode returns the pseudo-row for products filed directly on categoryID —
// they belong to this branch but to none of its children, so without it the
// child rows would not add up to the branch total.
func (r *Repository) directNode(ctx context.Context, categoryID int64, name string) (*ValuationNode, error) {
	var rows []childRow
	if err := r.db.SelectContext(ctx, &rows, `
		SELECT $1::bigint AS category_id, $2::text AS name, false AS has_children,
		       COALESCE(u.abbreviation, '')                           AS unit_abbr,
		       count(p.id)                                            AS skus,
		       count(p.id) FILTER (WHERE COALESCE(st.quantity,0) > 0) AS in_stock,
		       COALESCE(SUM(st.quantity), 0)                          AS units,
		       `+costValueSQL+`                                      AS cost_value,
		       `+retailValueSQL+`                                    AS retail_value
		FROM products p
		LEFT JOIN units u  ON u.id = p.unit_id
		LEFT JOIN stock st ON st.product_id = p.id`+lotValueJoin+`
		WHERE p.category_id = $1 AND p.is_active AND NOT p.is_service
		GROUP BY u.abbreviation`, categoryID, name); err != nil {
		return nil, err
	}
	nodes := foldChildren(rows)
	if len(nodes) == 0 || nodes[0].SKUs == 0 {
		return nil, nil
	}
	n := nodes[0]
	n.Direct = true
	return &n, nil
}

// Ancestors returns the path from the root down to categoryID.
func (r *Repository) Ancestors(ctx context.Context, categoryID int64) ([]Crumb, error) {
	var rows []catRow
	if err := r.db.SelectContext(ctx, &rows, `
		WITH RECURSIVE up AS (
			SELECT id, name, parent_id FROM categories WHERE id = $1
		  UNION ALL
			SELECT c.id, c.name, c.parent_id FROM categories c JOIN up ON up.parent_id = c.id
		)
		SELECT id, name, parent_id FROM up`, categoryID); err != nil {
		return nil, err
	}
	return orderAncestors(rows, categoryID), nil
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
	// The $1 IS NULL arm short-circuits before the subquery is consulted, so the
	// whole catalogue is returned rather than being narrowed to root categories.
	err := r.db.SelectContext(ctx, &rows, branchCTE+selectProduct+`
		WHERE p.is_active = true AND p.is_service = false
		  AND ($1::bigint IS NULL OR p.category_id IN (SELECT id FROM sub))
		  AND ($2 = true OR COALESCE(s.quantity,0) <> 0)
		ORDER BY COALESCE(s.quantity,0) * p.cost_price DESC, p.name
		LIMIT NULLIF($3, 0) OFFSET $4`,
		q.CategoryID, q.IncludeZero, limit, offset)
	return rows, err
}

func (r *Repository) ValuationRowCount(ctx context.Context, q ValuationQuery) (int, error) {
	var n int
	err := r.db.GetContext(ctx, &n, branchCTE+`
		SELECT count(*)
		FROM products p
		LEFT JOIN stock s ON s.product_id = p.id
		WHERE p.is_active = true AND p.is_service = false
		  AND ($1::bigint IS NULL OR p.category_id IN (SELECT id FROM sub))
		  AND ($2 = true OR COALESCE(s.quantity,0) <> 0)`,
		q.CategoryID, q.IncludeZero)
	return n, err
}

// --- service ---

// Valuation returns one branch of the catalogue: its totals, its child rows and
// its breadcrumb. A nil categoryID is the whole shop.
func (s *Service) Valuation(ctx context.Context, categoryID *int64) (*Valuation, error) {
	// Resolve the path first, because it doubles as an existence check. A
	// category that has been deleted since someone bookmarked it would
	// otherwise render an empty branch reading Rs 0.00, which looks like the
	// shop has lost all its stock; fall back to the whole catalogue instead.
	var crumbs []Crumb
	if categoryID != nil {
		found, cerr := s.repo.Ancestors(ctx, *categoryID)
		if cerr != nil {
			return nil, apperr.Internal("failed to resolve the category path", cerr)
		}
		if len(found) == 0 {
			categoryID = nil
		}
		crumbs = found
	}

	v, err := s.repo.ValuationBranch(ctx, categoryID)
	if err != nil {
		return nil, apperr.Internal("failed to value inventory", err)
	}
	children, err := s.repo.ValuationChildren(ctx, categoryID)
	if err != nil {
		return nil, apperr.Internal("failed to break inventory down by category", err)
	}
	v.Children = children
	if categoryID != nil {
		v.Breadcrumb = crumbs
		name := ""
		if len(crumbs) > 0 {
			name = crumbs[len(crumbs)-1].Name
		}
		direct, derr := s.repo.directNode(ctx, *categoryID, name)
		if derr != nil {
			return nil, apperr.Internal("failed to total this category's own products", derr)
		}
		if direct != nil {
			v.Children = append(v.Children, *direct)
		}
	}
	return &v, nil
}

// CategoryPath returns the root-to-leaf path of a category. It lives here
// because this package already owns the ancestor walk used by the inventory
// breadcrumb; exposing it avoids a second implementation.
func (s *Service) CategoryPath(ctx context.Context, categoryID int64) ([]Crumb, error) {
	crumbs, err := s.repo.Ancestors(ctx, categoryID)
	if err != nil {
		return nil, apperr.Internal("failed to resolve the category path", err)
	}
	return crumbs, nil
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
