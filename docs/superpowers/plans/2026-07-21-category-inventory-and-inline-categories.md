# Category Inventory + Inline Category Creation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the owner value and count stock at any category depth, and create categories (nested) without leaving the form they are filling in.

**Architecture:** Two independent features sharing the categories domain. Feature A replaces the root-collapsing CTE in the inventory valuation with a subtree rollup parameterised by the category being viewed, and adds unit counts. Feature B adds an `allowCreate` affordance to the shared category picker backed by one thin endpoint that delegates to the existing `categories.FindOrCreateByPath`. Neither needs a migration.

**Tech Stack:** Go 1.x, Echo, sqlx, PostgreSQL 17, Templ, HTMX, Alpine.js, Tailwind, shopspring/decimal.

## Global Constraints

- **No migration.** Neither feature changes the schema. Do not create files in `migrations/`.
- **Leave these three files uncommitted, always:** `static/css/tailwind.css`, `cmd/server/enabled_plugins.go`, `.claude/settings.local.json`. Never `git add` them.
- **Never `git add` generated `*_templ.go` files** — they are gitignored.
- **Run `make css` after adding any new Tailwind utility class**; CSS is embedded in the binary, so the server must be rebuilt to see style changes.
- **Web-layer cycle rule:** feature packages (`internal/features/...`) never import `templates/...`.
- **Nothing may force recounting stock or re-entering products.**
- Commit directly to `main`. End every commit message with:
  `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`
- Dev login for live checks: phone `0000000001`, PIN `2273`.
- Baseline the dev DB must still show after any live testing: **618 products, 15,008 units, Rs 2,087,632.50, 0 sales.** Delete any test rows you create.

---

# Feature A — Category-level inventory valuation and counts

Spec: `docs/superpowers/specs/2026-07-21-category-inventory-design.md`

### Task 1: Quantity formatter

`money.Display` renders `1755.00`, which reads wrong for a count of notebooks. Add a quantity formatter beside it.

**Files:**
- Modify: `internal/money/money.go`
- Test: `internal/money/money_test.go` (create)

**Interfaces:**
- Consumes: nothing.
- Produces: `money.Qty(d decimal.Decimal) string`.

- [ ] **Step 1: Write the failing test**

Create `internal/money/money_test.go`:

```go
package money

import (
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// A whole count must read as a whole number: "1,755", never "1,755.00".
func TestQtyDropsTrailingZeros(t *testing.T) {
	if got := Qty(dec("1755.000000")); got != "1,755" {
		t.Errorf("Qty = %q, want %q", got, "1,755")
	}
}

// A genuine fraction survives — 3.6 bags of premix is the whole point.
func TestQtyKeepsRealFraction(t *testing.T) {
	if got := Qty(dec("3.600000")); got != "3.6" {
		t.Errorf("Qty = %q, want %q", got, "3.6")
	}
}

func TestQtyRoundsToThreePlaces(t *testing.T) {
	if got := Qty(dec("0.0204")); got != "0.02" {
		t.Errorf("Qty = %q, want %q", got, "0.02")
	}
}

func TestQtyHandlesZeroAndNegative(t *testing.T) {
	if got := Qty(decimal.Zero); got != "0" {
		t.Errorf("Qty(0) = %q, want %q", got, "0")
	}
	if got := Qty(dec("-12.5")); got != "-12.5" {
		t.Errorf("Qty(-12.5) = %q, want %q", got, "-12.5")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/money/ -run TestQty -v`
Expected: FAIL — `undefined: Qty`

- [ ] **Step 3: Write minimal implementation**

Add to `internal/money/money.go` (confirm `strings` is in the import block; add it if not):

```go
// Qty renders a stock quantity for display: thousands separated, up to three
// decimal places, with trailing zeros dropped. A count of 1755 reads "1,755"
// rather than "1,755.00", while a genuinely part-used 3.6 stays "3.6".
func Qty(d decimal.Decimal) string {
	s := d.Round(3).String()
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	if s == "" || s == "-" {
		return "0"
	}
	return withThousands(s)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/money/ -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/money/money.go internal/money/money_test.go
git commit -m "feat(money): Qty formats a stock count without trailing zeros

money.Display renders 1755.00, which reads wrong for a count of notebooks.
Qty gives 1,755 while keeping a genuine fraction like 3.6 bags intact.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: Unit tally

A branch may hold more than one unit. Summing kg with pieces is meaningless, so the tally keeps them apart and only collapses to one number when the branch genuinely shares a unit.

**Files:**
- Create: `internal/features/products/tally.go`
- Test: `internal/features/products/tally_test.go`

**Interfaces:**
- Consumes: `money.Qty` (Task 1).
- Produces: `products.UnitQty{Abbr string; Qty decimal.Decimal}`, `products.UnitTally []UnitQty`, methods `Total() decimal.Decimal` and `Format() string`.

- [ ] **Step 1: Write the failing test**

Create `internal/features/products/tally_test.go`:

```go
package products

import (
	"testing"

	"github.com/shopspring/decimal"
)

func tdec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// The normal case for this shop: one unit, so no unit name is needed.
func TestUnitTallyFormatsASingleUnitAsAPlainNumber(t *testing.T) {
	tl := UnitTally{{Abbr: "pcs", Qty: tdec("1755")}}
	if got := tl.Format(); got != "1,755" {
		t.Errorf("Format = %q, want %q", got, "1,755")
	}
}

// Mixed units must never be summed into one meaningless figure.
func TestUnitTallyFormatsMixedUnitsSeparately(t *testing.T) {
	tl := UnitTally{{Abbr: "btl", Qty: tdec("12")}, {Abbr: "pcs", Qty: tdec("412")}}
	if got := tl.Format(); got != "412 pcs · 12 btl" {
		t.Errorf("Format = %q, want %q", got, "412 pcs · 12 btl")
	}
}

func TestUnitTallyFormatsEmptyAsZero(t *testing.T) {
	if got := (UnitTally{}).Format(); got != "0" {
		t.Errorf("Format = %q, want %q", got, "0")
	}
}

// Total is only meaningful for reconciliation, but it must still be right.
func TestUnitTallyTotal(t *testing.T) {
	tl := UnitTally{{Abbr: "pcs", Qty: tdec("412")}, {Abbr: "btl", Qty: tdec("12")}}
	if got := tl.Total(); !got.Equal(tdec("424")) {
		t.Errorf("Total = %s, want 424", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/features/products/ -run UnitTally -v`
Expected: FAIL — `undefined: UnitTally`

- [ ] **Step 3: Write minimal implementation**

Create `internal/features/products/tally.go`:

```go
package products

import (
	"sort"
	"strings"

	"karots-pos/internal/money"

	"github.com/shopspring/decimal"
)

// UnitQty is how much of one unit a branch of the catalogue holds.
type UnitQty struct {
	Abbr string
	Qty  decimal.Decimal
}

// UnitTally is a branch's stock broken down by unit.
//
// It exists because "how many do I have" has no single answer once a branch
// mixes units: 500 pcs plus 12 btl is not 512 of anything. Almost every branch
// in a stationery shop is one unit, so Format collapses to a plain number in
// that case and only names units when it genuinely must.
type UnitTally []UnitQty

// Total sums across units. Only meaningful when the tally has one unit; used
// for ordering and reconciliation, not for display.
func (t UnitTally) Total() decimal.Decimal {
	sum := decimal.Zero
	for _, u := range t {
		sum = sum.Add(u.Qty)
	}
	return sum
}

// Format renders the tally: a bare number for one unit, otherwise the units
// spelled out, largest first.
func (t UnitTally) Format() string {
	if len(t) == 0 {
		return "0"
	}
	if len(t) == 1 {
		return money.Qty(t[0].Qty)
	}
	sorted := make(UnitTally, len(t))
	copy(sorted, t)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Qty.GreaterThan(sorted[j].Qty) })
	parts := make([]string, 0, len(sorted))
	for _, u := range sorted {
		parts = append(parts, money.Qty(u.Qty)+" "+u.Abbr)
	}
	return strings.Join(parts, " · ")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/features/products/ -v -run UnitTally`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/features/products/tally.go internal/features/products/tally_test.go
git commit -m "feat(products): UnitTally counts stock without summing kg with pieces

A branch of the catalogue may hold more than one unit. Format collapses to a
plain number when the branch shares a unit — the normal case here — and only
names units when summing them would be meaningless.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: Breadcrumb ordering

The repository will fetch a category's ancestors as an unordered set; ordering them root-first is pure logic and gets its own test, including a guard against a corrupted `parent_id` cycle.

**Files:**
- Create: `internal/features/products/breadcrumb.go`
- Test: `internal/features/products/breadcrumb_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `products.Crumb{ID int64; Name string}`, `products.catRow{ID int64; Name string; ParentID *int64}` (unexported, used by the repository), `products.orderAncestors(rows []catRow, leafID int64) []Crumb` (unexported).

- [ ] **Step 1: Write the failing test**

Create `internal/features/products/breadcrumb_test.go`:

```go
package products

import "testing"

func pid(v int64) *int64 { return &v }

func names(cs []Crumb) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func eq(a []string, b ...string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestOrderAncestorsReturnsRootFirst(t *testing.T) {
	rows := []catRow{
		{ID: 3, Name: "Batteries", ParentID: pid(2)},
		{ID: 1, Name: "Electronics", ParentID: nil},
		{ID: 2, Name: "Batteries & Power", ParentID: pid(1)},
	}
	got := names(orderAncestors(rows, 3))
	if !eq(got, "Electronics", "Batteries & Power", "Batteries") {
		t.Errorf("got %v", got)
	}
}

func TestOrderAncestorsHandlesARootLeaf(t *testing.T) {
	rows := []catRow{{ID: 1, Name: "Electronics", ParentID: nil}}
	if got := names(orderAncestors(rows, 1)); !eq(got, "Electronics") {
		t.Errorf("got %v", got)
	}
}

func TestOrderAncestorsReturnsNothingForAnUnknownLeaf(t *testing.T) {
	rows := []catRow{{ID: 1, Name: "Electronics", ParentID: nil}}
	if got := orderAncestors(rows, 99); len(got) != 0 {
		t.Errorf("got %d crumbs, want 0", len(got))
	}
}

// A corrupted parent_id loop must terminate rather than hang the report.
func TestOrderAncestorsSurvivesACycle(t *testing.T) {
	rows := []catRow{
		{ID: 1, Name: "A", ParentID: pid(2)},
		{ID: 2, Name: "B", ParentID: pid(1)},
	}
	got := orderAncestors(rows, 1)
	if len(got) > 2 {
		t.Errorf("cycle produced %d crumbs, want at most 2", len(got))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/features/products/ -run OrderAncestors -v`
Expected: FAIL — `undefined: catRow`, `undefined: orderAncestors`

- [ ] **Step 3: Write minimal implementation**

Create `internal/features/products/breadcrumb.go`:

```go
package products

// Crumb is one step of the category path shown above the inventory report.
type Crumb struct {
	ID   int64
	Name string
}

// catRow is a category as the ancestor query returns it — flat and unordered.
type catRow struct {
	ID       int64  `db:"id"`
	Name     string `db:"name"`
	ParentID *int64 `db:"parent_id"`
}

// orderAncestors walks from leafID up to the root and returns the path
// root-first. Ordering in Go rather than SQL keeps it testable without a
// database, and lets a corrupted parent_id cycle terminate instead of hanging
// the report: every id is visited at most once.
func orderAncestors(rows []catRow, leafID int64) []Crumb {
	byID := make(map[int64]catRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	var rev []Crumb
	seen := make(map[int64]bool, len(rows))
	for id := leafID; ; {
		r, ok := byID[id]
		if !ok || seen[id] {
			break
		}
		seen[id] = true
		rev = append(rev, Crumb{ID: r.ID, Name: r.Name})
		if r.ParentID == nil {
			break
		}
		id = *r.ParentID
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/features/products/ -v -run OrderAncestors`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/features/products/breadcrumb.go internal/features/products/breadcrumb_test.go
git commit -m "feat(products): order category ancestors root-first for the breadcrumb

Ordering in Go rather than SQL keeps it unit-testable and lets a corrupted
parent_id cycle terminate instead of hanging the report.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 4: Subtree rollup queries

Replace the root-collapsing CTE. This is the core of the feature.

**Files:**
- Modify: `internal/features/products/valuation.go`

**Interfaces:**
- Consumes: `UnitTally` (Task 2), `Crumb` / `catRow` / `orderAncestors` (Task 3).
- Produces:
  - `ValuationNode{CategoryID int64; Name string; Direct bool; HasChildren bool; SKUs int; InStock int; Units UnitTally; CostValue, RetailValue decimal.Decimal}`
  - `Valuation` gains `Children []ValuationNode`, `Breadcrumb []Crumb`, `Units UnitTally`; its `Categories []ValuationRow` field and the `ValuationRow` type are removed.
  - `(*Repository).ValuationChildren(ctx, categoryID *int64) ([]ValuationNode, error)`
  - `(*Repository).ValuationBranch(ctx, categoryID *int64) (Valuation, error)`
  - `(*Repository).Ancestors(ctx, categoryID int64) ([]Crumb, error)`
  - `(*Service).Valuation(ctx, categoryID *int64) (*Valuation, error)` — **signature change**, callers must pass the category.
  - `ValuationQuery.CategoryID` now means "anywhere in this subtree".

- [ ] **Step 1: Replace rootCatsCTE and the row type**

In `internal/features/products/valuation.go`, delete the `ValuationRow` type and the `rootCatsCTE` constant, and replace them with:

```go
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
```

Update the `Valuation` struct to:

```go
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
```

- [ ] **Step 2: Add the children query**

Add to `internal/features/products/valuation.go`:

```go
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
		       COALESCE(SUM(st.quantity * p.cost_price), 0)               AS cost_value,
		       COALESCE(SUM(st.quantity * p.selling_price), 0)            AS retail_value
		FROM sub s
		JOIN categories tc ON tc.id = s.top
		LEFT JOIN products p ON p.category_id = s.id AND p.is_active AND NOT p.is_service
		LEFT JOIN units u    ON u.id = p.unit_id
		LEFT JOIN stock st   ON st.product_id = p.id
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
```

- [ ] **Step 3: Add the branch-total and ancestor queries**

```go
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
		       COALESCE(SUM(st.quantity * p.cost_price), 0)           AS cost_value,
		       COALESCE(SUM(st.quantity * p.selling_price), 0)        AS retail_value,
		       count(p.id) FILTER (WHERE COALESCE(st.quantity,0) > 0 AND COALESCE(p.cost_price,0) = 0) AS missing_cost,
		       count(p.id) FILTER (WHERE COALESCE(st.quantity,0) < 0) AS negative_qty
		FROM sub s
		LEFT JOIN products p ON p.category_id = s.id AND p.is_active AND NOT p.is_service
		LEFT JOIN units u    ON u.id = p.unit_id
		LEFT JOIN stock st   ON st.product_id = p.id
		GROUP BY u.abbreviation`, categoryID)
	if err != nil {
		return Valuation{}, err
	}
	var v Valuation
	for _, r := range rows {
		v.SKUs += r.SKUs
		v.InStock += r.InStock
		v.MissingCost += r.MissingCost
		v.NegativeQty += r.NegativeQty
		v.CostValue = v.CostValue.Add(r.CostValue)
		v.RetailValue = v.RetailValue.Add(r.RetailValue)
		if r.UnitAbbr != "" && r.Units.IsPositive() {
			v.Units = append(v.Units, UnitQty{Abbr: r.UnitAbbr, Qty: r.Units})
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
		       COALESCE(SUM(st.quantity * p.cost_price), 0)           AS cost_value,
		       COALESCE(SUM(st.quantity * p.selling_price), 0)        AS retail_value
		FROM products p
		LEFT JOIN units u  ON u.id = p.unit_id
		LEFT JOIN stock st ON st.product_id = p.id
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
```

- [ ] **Step 4: Change the product-list filter to subtree membership**

In `ValuationRows` and `ValuationRowCount`, replace `rootCatsCTE` + `JOIN roots rt ON rt.id = p.category_id` + `AND ($1::bigint IS NULL OR rt.root_id = $1)` with subtree membership. `ValuationRows` becomes:

```go
func (r *Repository) ValuationRows(ctx context.Context, q ValuationQuery, limit, offset int) ([]Product, error) {
	var rows []Product
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
```

Note `branchCTE`'s NULL arm selects only root categories, which would wrongly narrow the "all categories" view — but the `$1::bigint IS NULL OR` guard short-circuits before the subquery is consulted, so the whole catalogue is returned. Keep both halves of that predicate.

- [ ] **Step 5: Update the service**

```go
// Valuation returns one branch of the catalogue: its totals, its child rows and
// its breadcrumb. A nil categoryID is the whole shop.
func (s *Service) Valuation(ctx context.Context, categoryID *int64) (*Valuation, error) {
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
		crumbs, cerr := s.repo.Ancestors(ctx, *categoryID)
		if cerr != nil {
			return nil, apperr.Internal("failed to resolve the category path", cerr)
		}
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
	return v, nil
}
```

- [ ] **Step 6: Build**

Run: `go build ./...`
Expected: fails only in `internal/web/admin_reports.go` (the `Valuation(ctx)` call now needs a category) and `templates/pages/admin/mgmt_reports.templ` (`Val.Categories` is gone). Both are fixed in Task 5.

- [ ] **Step 7: Commit after Task 5 builds** — this task is not independently buildable; commit at the end of Task 5.

---

### Task 5: Handler and template

**Files:**
- Modify: `internal/web/admin_reports.go:453-505`
- Modify: `templates/pages/admin/mgmt_reports.templ:549-621`

**Interfaces:**
- Consumes: everything from Task 4.
- Produces: `InventoryReportData` gains `Breadcrumb []products.Crumb`.

- [ ] **Step 1: Update the handler**

In `internal/web/admin_reports.go`, change the CSV branch to add a Units column and the render branch to pass the category through:

```go
	if wantsCSV(c) {
		rows, err := a.s.products.ValuationAll(ctx, q)
		if err != nil {
			return err
		}
		out := make([][]string, 0, len(rows))
		for _, p := range rows {
			out = append(out, []string{
				p.Name, ptrStr(p.Barcode), p.CategoryName, p.UnitAbbr,
				p.StockQty.String(), csvMoney(p.CostPrice), csvMoney(p.SellingPrice),
				csvMoney(p.StockQty.Mul(p.CostPrice)), csvMoney(p.StockQty.Mul(p.SellingPrice)),
			})
		}
		return writeCSV(c, "inventory_valuation_"+time.Now().Format("2006-01-02"),
			[]string{"Product", "Barcode", "Category", "Unit", "On hand",
				"Cost", "Retail", "Cost value", "Retail value"}, out)
	}

	val, err := a.s.products.Valuation(ctx, q.CategoryID)
	if err != nil {
		return err
	}
	rows, total, err := a.s.products.ValuationDetail(ctx, q, reportPageSize)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.InventoryReport(adminpages.InventoryReportData{
		ShopName:    a.shopName(ctx),
		Symbol:      a.symbol(ctx),
		Val:         *val,
		Breadcrumb:  val.Breadcrumb,
		Rows:        rows,
		Total:       total,
		Page:        q.Page,
		PageSize:    reportPageSize,
		CategoryID:  q.CategoryID,
		IncludeZero: q.IncludeZero,
	}))
```

The existing `category_id` parse already ignores non-numeric input, so a stale bookmark falls back to the root view. Leave it as it is.

- [ ] **Step 2: Update the template data struct and breakdown table**

In `templates/pages/admin/mgmt_reports.templ`, add `Breadcrumb []products.Crumb` to `InventoryReportData`, then replace the `By category` table (lines ~577-605) with:

```templ
		<div class="flex items-center gap-2 mb-2">
			<h2 class="font-semibold">By category</h2>
			<nav class="text-sm text-slate-500 flex items-center gap-1 flex-wrap">
				<a class="hover:underline" href="/admin/reports/inventory">All</a>
				for _, cr := range d.Breadcrumb {
					<span class="text-slate-300">▸</span>
					<a class="hover:underline" href={ templ.SafeURL("/admin/reports/inventory?category_id=" + strconv.FormatInt(cr.ID, 10)) }>{ cr.Name }</a>
				}
			</nav>
		</div>
		<table class="w-full text-sm mb-6">
			<thead class="text-left text-slate-500 border-b">
				<tr>
					<th class="py-1.5">Category</th>
					<th class="py-1.5 text-right">In stock</th>
					<th class="py-1.5 text-right">Units</th>
					<th class="py-1.5 text-right">Cost value</th>
					<th class="py-1.5 text-right">Retail value</th>
					<th class="py-1.5 text-right">Share</th>
				</tr>
			</thead>
			<tbody>
				for _, r := range d.Val.Children {
					<tr class="border-b last:border-0">
						<td class="py-1.5 font-medium">
							if r.Direct {
								<span class="text-slate-500 font-normal">directly in { r.Name }</span>
							} else if r.HasChildren {
								<a class="text-indigo-600 hover:underline" href={ templ.SafeURL("/admin/reports/inventory?category_id=" + strconv.FormatInt(r.CategoryID, 10)) }>{ r.Name } ▸</a>
							} else {
								<a class="text-indigo-600 hover:underline" href={ templ.SafeURL("/admin/reports/inventory?category_id=" + strconv.FormatInt(r.CategoryID, 10)) }>{ r.Name }</a>
							}
						</td>
						<td class="py-1.5 text-right">{ strconv.Itoa(r.InStock) } / { strconv.Itoa(r.SKUs) }</td>
						<td class="py-1.5 text-right">{ r.Units.Format() }</td>
						<td class="py-1.5 text-right">{ money.Format(d.Symbol, r.CostValue) }</td>
						<td class="py-1.5 text-right">{ money.Format(d.Symbol, r.RetailValue) }</td>
						<td class="py-1.5 text-right text-slate-500">{ pctOf(r.CostValue, d.Val.CostValue) }%</td>
					</tr>
				}
				if len(d.Val.Children) == 0 {
					<tr><td colspan="6" class="py-6 text-center text-slate-400">No products.</td></tr>
				}
			</tbody>
			<tfoot class="border-t-2 font-semibold">
				<tr>
					<td class="py-2">Total</td>
					<td class="py-2 text-right">{ strconv.Itoa(d.Val.InStock) } / { strconv.Itoa(d.Val.SKUs) }</td>
					<td class="py-2 text-right">{ d.Val.Units.Format() }</td>
					<td class="py-2 text-right">{ money.Format(d.Symbol, d.Val.CostValue) }</td>
					<td class="py-2 text-right">{ money.Format(d.Symbol, d.Val.RetailValue) }</td>
					<td class="py-2 text-right">100%</td>
				</tr>
			</tfoot>
		</table>
```

Also add a fifth stat card beside the existing four:

```templ
			@invStat("Units on hand", d.Val.Units.Format(), "text-slate-900")
```

and change the wrapper's grid classes from `md:grid-cols-4` to `md:grid-cols-5`.

- [ ] **Step 3: Replace the detail filter's category select with the tree picker**

The `<select name="category_id">` (lines ~616-621) iterated `d.Val.Categories`, which no longer exists and only ever held one level. Replace the whole `<div>` holding that select with:

```templ
				<div>
					<label class="block text-xs text-slate-500 mb-1">Category</label>
					@adminfragments.CategoryPicker(d.Categories, "category_id", invCatID(d), true, "All categories", false)
				</div>
```

Add `Categories []categories.TreeNode` to `InventoryReportData`, populate it in the handler with `a.s.categories.Tree(ctx)`, and add this helper beside the other template helpers in the same file:

```go
// invCatID renders the selected category for the picker's hidden input.
func invCatID(d InventoryReportData) string {
	if d.CategoryID == nil {
		return ""
	}
	return strconv.FormatInt(*d.CategoryID, 10)
}
```

Ensure the file imports `karots-pos/internal/features/categories` and `adminfragments "karots-pos/templates/fragments/admin"`.

- [ ] **Step 4: Generate, build, test**

```bash
templ generate && go build ./... && go vet ./... && go test ./...
```
Expected: all pass.

- [ ] **Step 5: Verify live against the real catalogue**

Rebuild and restart the server, then check each of these:

```bash
make css && go build -o /tmp/pos ./cmd/server
# restart the server with the .env loaded, then:
```

- Root view totals exactly **15,008 units / Rs 2,087,632.50** (the recorded baseline).
- `?category_id=` for `Batteries` drills to AA / AAA; `AA` reads **806 units / 6 SKUs**.
- Every node's children (including any `directly in …` row) sum to that node's total.
- CSV at a sub-category exports only that branch.
- A `category_id` that does not exist renders the root view rather than a 500.

- [ ] **Step 6: Commit**

```bash
git add internal/features/products/valuation.go internal/web/admin_reports.go templates/pages/admin/mgmt_reports.templ
git commit -m "feat(reports): value and count stock at any category depth

rootCatsCTE mapped every category to its root, so the breakdown could only
ever show the 12 top-level categories — and the category filter compared
against root_id, so passing a sub-category id matched nothing at all. A
subtree CTE parameterised by the category being viewed replaces it, with a
breadcrumb, drill-down links, and a branch total.

Also adds the Units column: nothing in the system summed stock quantity, so
'how many notebooks do I have' was not computable. Units keep their unit —
a branch mixing pcs and btl shows both rather than a meaningless sum.

Products filed directly on an interior category get their own row, so the
children always add up to the branch total.

No migration; the recursive CTE measures 1.4 ms on 180 categories.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

# Feature B — Inline category creation

Spec: `docs/superpowers/specs/2026-07-21-inline-category-create-design.md`

### Task 6: Quick-create validation

**Files:**
- Create: `internal/features/categories/quick.go`
- Test: `internal/features/categories/quick_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `categories.CleanName(raw string) (string, error)`.

- [ ] **Step 1: Write the failing test**

Create `internal/features/categories/quick_test.go`:

```go
package categories

import "testing"

func TestCleanNameTrims(t *testing.T) {
	got, err := CleanName("  9V Batteries  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "9V Batteries" {
		t.Errorf("got %q, want %q", got, "9V Batteries")
	}
}

func TestCleanNameRejectsBlank(t *testing.T) {
	for _, in := range []string{"", "   ", "\t\n"} {
		if _, err := CleanName(in); err == nil {
			t.Errorf("CleanName(%q) returned no error", in)
		}
	}
}

// The parent is supplied structurally by the row that was tapped, so a ">" in
// the name is part of the name — it must not silently create extra levels.
func TestCleanNameRejectsAPathSeparator(t *testing.T) {
	if _, err := CleanName("Batteries > 9V"); err == nil {
		t.Error("a name containing '>' was accepted")
	}
}

func TestCleanNameRejectsOverlyLongNames(t *testing.T) {
	long := ""
	for i := 0; i < 81; i++ {
		long += "x"
	}
	if _, err := CleanName(long); err == nil {
		t.Error("an 81-character name was accepted")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/features/categories/ -v`
Expected: FAIL — `undefined: CleanName`

- [ ] **Step 3: Write minimal implementation**

Create `internal/features/categories/quick.go`:

```go
package categories

import (
	"strings"

	"karots-pos/internal/apperr"
)

// maxNameLen matches the validate:"max=80" on CreateInput.Name.
const maxNameLen = 80

// CleanName validates a category name typed into the inline creator.
//
// A ">" is rejected rather than split: the parent comes from the row the user
// tapped, so treating the name as a path would quietly create levels they did
// not ask for and could not see.
func CleanName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", apperr.Validation("enter a category name")
	}
	if strings.Contains(name, ">") {
		return "", apperr.Validation("a category name cannot contain '>'")
	}
	if len(name) > maxNameLen {
		return "", apperr.Validation("that category name is too long")
	}
	return name, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/features/categories/ -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/features/categories/quick.go internal/features/categories/quick_test.go
git commit -m "feat(categories): CleanName validates an inline category name

Rejects '>' rather than splitting on it: the parent comes from the row that
was tapped, so treating the name as a path would create levels the user never
asked for and cannot see.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 7: Quick-create endpoint

**Files:**
- Modify: `internal/web/admin.go` (add handler beside `CategoryCreate`)
- Modify: `internal/web/web.go:411` (add route)

**Interfaces:**
- Consumes: `categories.CleanName` (Task 6), the existing `categories.Service.FindOrCreateByPath` and `categories.Service.Tree`.
- Produces: `POST /admin/categories/quick` returning `{"id":int64,"name":string,"depth":int}`.

- [ ] **Step 1: Add the handler**

Add to `internal/web/admin.go`, directly after `CategoryCreate`:

```go
// CategoryQuickCreate creates one category from inside a picker and returns it
// as JSON so the picker can select it without a page reload.
//
// Creation goes through FindOrCreateByPath — the same call the CSV product
// import uses — so asking twice for the same child selects the existing
// category instead of duplicating it.
func (a *adminUI) CategoryQuickCreate(c echo.Context) error {
	name, err := categories.CleanName(c.FormValue("name"))
	if err != nil {
		return err
	}
	ctx := c.Request().Context()

	path := name
	depth := 0
	if raw := strings.TrimSpace(c.FormValue("parent_id")); raw != "" && raw != "0" {
		pid, perr := strconv.ParseInt(raw, 10, 64)
		if perr != nil {
			return apperr.BadRequest("invalid parent category")
		}
		crumbs, aerr := a.s.products.CategoryPath(ctx, pid)
		if aerr != nil {
			return aerr
		}
		if len(crumbs) == 0 {
			return apperr.NotFound("parent category")
		}
		parts := make([]string, 0, len(crumbs)+1)
		for _, cr := range crumbs {
			parts = append(parts, cr.Name)
		}
		parts = append(parts, name)
		path = strings.Join(parts, " > ")
		depth = len(crumbs)
	}

	id, err := a.s.categories.FindOrCreateByPath(ctx, path)
	if err != nil {
		return err
	}
	a.logAudit(c, audit.ActionCreate, "category", strconv.FormatInt(id, 10), "created from picker: "+path)
	return response.Created(c, map[string]any{"id": id, "name": name, "depth": depth})
}
```

`response.Created` wraps the body as `{"success":true,"data":{…}}` and returns
201, which is why the picker's JS reads `json.data || json`.

Confirm `internal/web/admin.go` imports `strings`, `strconv`, `karots-pos/internal/features/categories`, `karots-pos/internal/apperr`, `karots-pos/internal/audit` and `karots-pos/internal/response`; add any that are missing.

- [ ] **Step 2: Expose the ancestor path to the web layer**

`Ancestors` lives on the products repository (Task 4) and is not reachable from the service the handler holds. Add a thin pass-through in `internal/features/products/valuation.go`:

```go
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
```

- [ ] **Step 3: Add the route**

In `internal/web/web.go`, directly after line 411 (`ag.POST("/categories", admin.CategoryCreate)`):

```go
	ag.POST("/categories/quick", admin.CategoryQuickCreate)
```

- [ ] **Step 4: Build and verify by hand**

```bash
go build ./... && go vet ./...
```

Restart the server, then:

```bash
curl -s -c /tmp/cj -X POST http://localhost:3000/login -d 'phone=0000000001&pin=2273' -o /dev/null
# top-level
curl -s -b /tmp/cj -X POST http://localhost:3000/admin/categories/quick -d 'name=ZZ Test Root'
# nested under it (substitute the id returned above)
curl -s -b /tmp/cj -X POST http://localhost:3000/admin/categories/quick -d 'name=ZZ Child' -d 'parent_id=<id>'
# idempotent: the same call again must return the SAME id
curl -s -b /tmp/cj -X POST http://localhost:3000/admin/categories/quick -d 'name=ZZ Child' -d 'parent_id=<id>'
# blank name must be rejected
curl -s -b /tmp/cj -o /dev/null -w '%{http_code}\n' -X POST http://localhost:3000/admin/categories/quick -d 'name=   '
```

Expected: two distinct ids, the third call returning the second's id unchanged, and `422` for the blank name.

- [ ] **Step 5: Delete the test categories**

```bash
docker compose exec -T postgres psql -U pos_user -d pos_db -c \
  "DELETE FROM categories WHERE name LIKE 'ZZ %';"
```

- [ ] **Step 6: Commit**

```bash
git add internal/web/admin.go internal/web/web.go internal/features/products/valuation.go
git commit -m "feat(categories): POST /admin/categories/quick creates one from a picker

Returns the new category as JSON so a picker can select it without losing the
half-filled form around it. Resolution delegates to FindOrCreateByPath, the
same call the CSV import uses, so asking twice returns the existing category
rather than duplicating it.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 8: Picker create panel

**Files:**
- Modify: `templates/fragments/admin/products.templ:104-134` (`CategoryPicker`)
- Modify: `templates/fragments/admin/helpers.go:17-37` (`categoryPickerData`)
- Modify: `static/js/app.js` (the `categoryPicker` Alpine component)

**Interfaces:**
- Consumes: `POST /admin/categories/quick` (Task 7).
- Produces: `CategoryPicker(nodes, fieldName, selected string, includeAll bool, allLabel string, reload bool, allowCreate bool)` — **one extra trailing parameter**; every call site must be updated (Task 9).

- [ ] **Step 1: Thread the flag through the data helper**

In `templates/fragments/admin/helpers.go`, add the parameter and config key:

```go
func categoryPickerData(nodes []categories.TreeNode, fieldName, selected string, includeAll bool, allLabel string, reload, allowCreate bool) string {
```

and inside `cfg`:

```go
		"allowCreate": allowCreate,
```

- [ ] **Step 2: Add the panel to the template**

Change the `CategoryPicker` signature to take `reload, allowCreate bool` and pass both to `categoryPickerData`. Inside the dropdown `<div>`, wrap the existing search box and `<ul>` in `<template x-if="!creating">…</template>`, then add after it:

```templ
				if allowCreate {
					<template x-if="!creating">
						<button type="button" x-on:click="startCreate(null)" class="w-full text-left px-3 py-2 border-t text-indigo-600 hover:bg-slate-50">
							＋ New top-level category
						</button>
					</template>
					<template x-if="creating">
						<div class="p-3 space-y-2">
							<div class="text-xs text-slate-500" x-text="createParent ? ('New under “' + createParent.name + '”') : 'New top-level category'"></div>
							<input
								x-ref="newName" x-model="newName" type="text" placeholder="Category name"
								x-on:keydown.enter.prevent="submitCreate()" x-on:keydown.escape.prevent="cancelCreate()"
								class="w-full border rounded-lg px-2 py-1.5 text-sm"
							/>
							<p class="text-xs text-rose-600" x-show="createError" x-cloak x-text="createError"></p>
							<div class="flex gap-2">
								<button type="button" x-on:click="submitCreate()" x-bind:disabled="createBusy" class="px-3 py-1.5 rounded-lg bg-indigo-600 text-white text-sm disabled:opacity-40">Create</button>
								<button type="button" x-on:click="cancelCreate()" class="px-3 py-1.5 rounded-lg border text-sm">Cancel</button>
							</div>
						</div>
					</template>
				}
```

Inside the option row `<li>`, add the ➕ beside the existing select button. Replace the `<li>` body with:

```templ
						<li class="flex items-center">
							<button type="button" x-on:click="pick(o)" x-bind:style="indent(o)" class="flex-1 text-left py-1.5 pr-3 hover:bg-slate-50" x-bind:class="String(o.id) === String(selected) ? 'bg-indigo-50 text-indigo-700 font-medium' : ''">
								<span x-show="o.depth > 0" class="text-slate-300">↳ </span><span x-text="o.name"></span>
							</button>
							if allowCreate {
								<button type="button" title="Add a category under this one" x-on:click.stop="startCreate(o)" class="px-2 py-1.5 text-slate-400 hover:text-indigo-600">＋</button>
							}
						</li>
```

`.stop` is required: without it the tap would also select the row.

- [ ] **Step 3: Add the Alpine behaviour**

In `static/js/app.js`, inside the object returned by `categoryPicker(cfg)`, add these fields and methods (keep the existing ones):

```js
    // --- inline category creation (only rendered when allowCreate) ---
    creating: false,
    createParent: null,
    newName: "",
    createError: "",
    createBusy: false,

    startCreate(parent) {
      this.creating = true;
      this.createParent = parent;
      this.newName = this.query || "";
      this.createError = "";
      this.$nextTick(() => this.$refs.newName && this.$refs.newName.focus());
    },
    cancelCreate() {
      this.creating = false;
      this.createParent = null;
      this.newName = "";
      this.createError = "";
    },
    async submitCreate() {
      if (this.createBusy) return;
      const name = (this.newName || "").trim();
      if (!name) {
        this.createError = "Enter a category name.";
        return;
      }
      this.createBusy = true;
      this.createError = "";
      try {
        const body = new URLSearchParams({ name: name });
        if (this.createParent) body.set("parent_id", String(this.createParent.id));
        const res = await fetch("/admin/categories/quick", {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/x-www-form-urlencoded", Accept: "application/json" },
          body: body,
        });
        const json = await res.json().catch(() => ({}));
        if (!res.ok) {
          this.createError = (json.error && json.error.message) || "Could not create that category.";
          return;
        }
        const created = json.data || json;
        // Splice the new option in directly after its parent so the indented
        // list still reads as a tree; append when it is top-level.
        const opt = { id: created.id, name: created.name, depth: created.depth || 0 };
        const at = this.createParent
          ? this.options.findIndex((o) => String(o.id) === String(this.createParent.id))
          : -1;
        const existing = this.options.findIndex((o) => String(o.id) === String(opt.id));
        if (existing >= 0) {
          this.options.splice(existing, 1, opt);
        } else if (at >= 0) {
          this.options.splice(at + 1, 0, opt);
        } else {
          this.options.push(opt);
        }
        this.selected = String(opt.id);
        this.query = "";
        this.cancelCreate();
        this.open = false;
      } finally {
        this.createBusy = false;
      }
    },
```

If the component closes its dropdown by setting `open = false` elsewhere, also reset `creating = false` there so reopening never lands mid-create.

- [ ] **Step 4: Commit after Task 9** — the template signature change breaks every call site; commit once they are updated.

---

### Task 9: Wire the call sites and verify

**Files:**
- Modify: `templates/pages/admin/intake.templ:72`
- Modify: `templates/pages/admin/services.templ:192`
- Modify: `templates/fragments/admin/products.templ:173`
- Modify: `templates/pages/admin/products.templ:61`
- Modify: `templates/pages/admin/stocktake.templ:68`
- Modify: `templates/pages/admin/mgmt_reports.templ` (the picker added in Task 5)

- [ ] **Step 1: Update every call site**

Creation contexts get `true`:

```templ
@adminfragments.CategoryPicker(d.Categories, "category_id", "", false, "", false, true)
```
— `intake.templ:72` and `services.templ:192`.

```templ
@CategoryPicker(cats, "category_id", productCatID(p), false, "", false, true)
```
— `fragments/admin/products.templ:173`.

Filter contexts get `false`:

```templ
@adminfragments.CategoryPicker(d.Categories, "category_id", d.CategoryID, true, "All categories", true, false)
```
— `products.templ:61` and `stocktake.templ:68`.

```templ
@adminfragments.CategoryPicker(d.Categories, "category_id", invCatID(d), true, "All categories", false, false)
```
— the inventory report picker from Task 5.

- [ ] **Step 2: Generate, build, test, restyle**

```bash
templ generate && go build ./... && go vet ./... && go test ./... && make css
```
Expected: all pass. Rebuild the binary and restart the server so the embedded CSS and templates are current.

- [ ] **Step 3: Verify live in a browser**

- Open **Admin → Inventory → Stock Intake**, search a name that does not exist, choose *Create new*, open the category picker: every row shows ➕ and there is a **＋ New top-level category** entry.
- Tap ➕ on an existing category, type a name, Create: the panel closes, that category is selected, and the item saves into it.
- Repeat the exact same name under the exact same parent: it selects the existing category and **no duplicate row appears** in Admin → Categories.
- Tapping ➕ must not also select that row.
- Open **Admin → Products** (the filter picker) and confirm there is **no** ➕ anywhere.
- Press Escape inside the create panel: it cancels and returns to the list.

- [ ] **Step 4: Clean up and confirm the baseline**

```bash
docker compose exec -T postgres psql -U pos_user -d pos_db -c \
  "DELETE FROM products WHERE name LIKE 'ZZ %'; DELETE FROM categories WHERE name LIKE 'ZZ %';"
docker compose exec -T postgres psql -U pos_user -d pos_db -t -A -c "
select 'products='||count(*) from products;
select 'units='||sum(quantity) from stock;
select 'value='||sum(s.quantity*p.cost_price) from stock s join products p on p.id=s.product_id;"
```
Expected: `products=618`, `units=15008.000000`, `value=2087632.50000000`.

- [ ] **Step 5: Commit**

```bash
git add templates/ static/js/app.js
git commit -m "feat(categories): create a category, nested, without leaving the form

Capturing a product dead-ended when its category did not exist yet: you had to
abandon the form, go to Admin - Categories, create it, and start over. Every
picker row now carries a plus that opens a one-line create panel nested under
that row, so the parent comes from where you tapped rather than a typed path.

Enabled only in the three creation contexts; the filter pickers are untouched.
Asking twice for the same child selects the existing category.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Self-review notes

**Spec coverage.** Feature A: subtree rollup (Task 4), units split only when mixed (Tasks 1–2, 4), breadcrumb (Tasks 3–5), drill-down links and Units column and branch total and CSV (Task 5), category picker to jump directly (Task 5 Step 3), unknown `category_id` falling back to the root view (Task 5 Step 1, existing parse). Feature B: `allowCreate` on the five call sites (Task 9), ➕ per row and top-level entry and panel (Task 8), endpoint delegating to `FindOrCreateByPath` (Task 7), name validation (Task 6), all live checks (Task 9 Step 3).

**Two deviations from the specs, both deliberate:**
1. Feature A adds a `Direct` pseudo-row. The spec required "every node's total equals the sum of its children plus its own direct products"; showing that remainder as a row is what makes the column actually add up on screen.
2. Feature A's spec named three repository methods `ValuationChildren` / `ValuationBranchTotal` / `CategoryBreadcrumb`. The plan uses `ValuationChildren` / `ValuationBranch` / `Ancestors` plus the unexported `directNode`. Names only.

**Signature changes that will break the build until their task completes:** `Service.Valuation` gains a category argument (Task 4, fixed in Task 5) and `CategoryPicker` gains `allowCreate` (Task 8, fixed in Task 9). Both are called out in their tasks.
