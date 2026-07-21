# Category-level inventory valuation and stock counts

**Date:** 2026-07-21
**Status:** approved, not yet implemented

## Problem

Two gaps in the Inventory Valuation report, both confirmed against the live
catalogue (180 categories over 4 levels, 617 stocked products).

**1. The report only sees root categories.** `rootCatsCTE` in
`internal/features/products/valuation.go` maps every category to the root of its
tree, so the breakdown is always the 12 top-level categories. The `category_id`
filter compares against `rt.root_id`, so passing a sub-category's id matches
nothing at all — there is no way to ask about "Batteries" or "Exercise Books".

**2. Nothing counts units.** The report has `SKUs` (how many products) and
`InStock` (how many of them have stock), but never `SUM(quantity)`. "How many
notebooks do I have" is not computable anywhere in the system today.

The answers the owner wants live at depth 3–4:

| Category | Products | Units |
|---|---|---|
| Office & School > Notebooks & Exercise Books > Exercise Books | 24 | 1,755 |
| Electronics > Batteries & Power > Batteries > AA | 6 | 806 |
| Office & School > Writing Instruments > Pen | 28 | 552 |

## Decisions

| Question | Decision |
|---|---|
| One report or two? | **One.** Extend Inventory Valuation with drill-down + a Units column. |
| Rollup or direct-only? | **Roll up the whole subtree.** "Batteries" includes AA, AAA, and anything filed directly under Batteries. |
| Mixed units in one branch? | **One number when the branch shares a unit; split by unit only when it genuinely mixes.** Never sum kg with pieces. |
| Schema change? | **None.** A recursive CTE measures 1.4 ms on this data. |

Mixed units are rare today (614 of 617 products are `pcs`; 3 are `btl`) but the
report must not silently produce a meaningless sum the first time something is
stocked by weight or volume.

## Design

### Data layer — `internal/features/products/valuation.go`

Replace `rootCatsCTE` with a subtree CTE parameterised by the category being
viewed. `NULL` means the whole catalogue, which keeps the existing top-level
behaviour intact.

```sql
WITH RECURSIVE sub AS (
    SELECT id, id AS top FROM categories
     WHERE ($1::bigint IS NULL AND parent_id IS NULL) OR parent_id = $1
  UNION ALL
    SELECT c.id, s.top FROM categories c JOIN sub s ON c.parent_id = s.id
)
```

Each row of `sub` maps a descendant category to the child-of-current it belongs
under, so grouping by `top` gives one rolled-up row per child.

New and changed types:

- `UnitTally []UnitQty` where `UnitQty{Abbr string; Qty decimal.Decimal}` —
  a branch's quantity broken down by unit.
- `ValuationNode` — one child category row: `CategoryID`, `Name`, `HasChildren`,
  `SKUs`, `InStock`, `Units UnitTally`, `CostValue`, `RetailValue`.
- `Valuation` gains `Breadcrumb []Crumb`, `Children []ValuationNode`, and
  `Units UnitTally` for the branch total. `Categories []ValuationRow` is
  replaced by `Children`.

Three repository methods:

- `ValuationChildren(ctx, categoryID *int64) ([]ValuationNode, error)`
- `ValuationBranchTotal(ctx, categoryID *int64) (Valuation, error)`
- `CategoryBreadcrumb(ctx, categoryID int64) ([]Crumb, error)` — walks
  `parent_id` upward; returns empty for the root view.

`ValuationRows` / `ValuationRowCount` change their category predicate from
`rt.root_id = $1` to membership of the subtree of `$1`, so the product list
under the summary lists everything in the branch being viewed.

**Products directly on an interior category** are included in that category's
branch total (the `sub` CTE seeds with the node itself), so nothing can be
filed somewhere that makes it invisible to its own parent's total.

### Units

`FormatTally` is a pure function, so it is testable without a database:

- one unit → `"1,755"`
- several → `"412 pcs · 12 btl"`, largest quantity first
- nothing → `"0"`

Quantities come back from SQL grouped by `unit_id`, so the tally is assembled
per branch rather than inferred from the rendered rows.

### UI — `templates/pages/admin/inventory.templ`

- Breadcrumb `All ▸ Office & School ▸ Notebooks`, each crumb a link, plus an
  up link when not at the root.
- Child category rows link to `?category_id=<id>`; a row with no children of its
  own is not a link.
- New **Units** column between `In stock` and `Cost value`.
- A branch-total row beneath the children.
- The existing `adminfragments.CategoryPicker` jumps straight to any level
  without drilling.
- CSV export keeps the current `category_id`, and gains a `Units` column.

### Handler — `internal/web/admin_reports.go`

`InventoryReport` already parses `category_id`; it passes it to the three new
methods instead of only to the detail list. An unknown or non-numeric
`category_id` falls back to the root view rather than erroring — a stale
bookmark should show the whole shop, not a 500.

## Testing

**Unit (no database):**
- `FormatTally`: single unit, mixed units, empty, ordering by quantity.
- `CategoryBreadcrumb` assembly from a flat parent map: root, one level, four
  levels, and a cycle guard so a corrupted `parent_id` loop terminates.

**Live, against the real catalogue:**
- Root view still totals exactly **15,008 units / Rs 2,087,632.50**.
- Every node's total equals the sum of its children plus its own direct products.
- `Electronics > Batteries & Power > Batteries > AA` reads **806 units / 6 SKUs**.
- Drilling to a leaf lists that leaf's products and no others.
- CSV at a sub-category exports only that branch.
- A `category_id` that does not exist renders the root view.

## Out of scope

- Any schema change, migration, or stored rollup.
- Changing how services are treated: they hold no stock and stay excluded.
- Editing categories from this report.
- Historical valuation ("what was it worth last month") — this is on-hand only.
