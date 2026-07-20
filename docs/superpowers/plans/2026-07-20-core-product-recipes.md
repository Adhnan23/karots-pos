# Core Product Recipes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let any service product declare what it consumes — paper, toner, coffee powder, cups — so selling it deducts real stock, books real COGS, and reports honest gross profit, with yields treated as estimates that can be reconciled against reality.

**Architecture:** Recipes move from the documents plugin into core as `product_recipes`, a per-product bill of materials. The core sale transaction already consumes declared components FEFO and uses their summed cost as the line's COGS (`internal/features/sales/service.go:246`); this plan feeds that seam automatically from a stored recipe instead of requiring a plugin to compute components client-side. Two properties make it usable in a real shop: components can be declared by **yield** ("this bag makes 50 cups") rather than by a tiny reciprocal, and each component says whether it must be consumed in **whole units** (a sheet of paper) or may be consumed fractionally (grams of powder). Because yields are estimates, a variance report compares expected consumption against counted stock so the estimate can be corrected.

**Tech Stack:** Go 1.x, Postgres 17, sqlx, goose migrations, Templ + HTMX + Alpine, shopspring/decimal.

## Global Constraints

- **Nothing in this plan may require recounting stock or re-entering products.** The catalog took five days to capture. Every migration here is additive (new tables, new nullable columns) or a precision *widening*, which preserves existing values exactly and rewrites no rows. Task 1 includes an explicit before/after check proving no stock value changed. If any step would force a recount, stop and raise it instead of proceeding.
- **Work on a branch**, not `main`: `git switch -c product-recipes` before Task 1. This touches the sale transaction, so it should be merged only after the owner has exercised it.
- Existing behaviour must not change for products without a recipe. A recipe is opt-in per product; selling anything that has none must follow exactly the path it follows today.

- Recipes apply **only to `is_service = true` products.** A service holds no stock of its own, so consuming its ingredients cannot double-count. Selling a stocked product must never expand a recipe.
- The core P&L must not change shape. Recipe cost reaches it through the existing `sale_items.cost_price` path only.
- Machine/servicing expenses are **not** COGS. They are operating expenses that become *attributable*, never per-unit ingredients.
- Plugins never alter core schema. The documents plugin reads core recipes; it does not own them.
- `plugins/documents` keeps its Go package name and `doc_*` table prefix. Only user-facing labels change (Task 9). Renaming tables and packages is churn with real restore/backup risk and no user benefit.
- Leave `static/css/tailwind.css`, `cmd/server/enabled_plugins.go` and `.claude/settings.local.json` uncommitted, as in every other commit on this repo.
- Run `make css` after adding Tailwind utility classes; the stylesheet is embedded in the binary.
- Every migration must be reversible, and any `-- +goose Down` that re-imposes a narrower constraint must first delete rows that violate it.

---

## File Structure

**Create:**
- `migrations/0045_quantity_precision.sql` — widen quantity columns so fractional consumption survives
- `migrations/0046_product_recipes.sql` — the recipe table
- `migrations/0047_expense_service_link.sql` — attributable expenses
- `internal/features/recipes/recipes.go` — model + repository (schema-facing)
- `internal/features/recipes/resolve.go` — pure consumption arithmetic (yield, whole-units)
- `internal/features/recipes/resolve_test.go` — tests for the arithmetic
- `internal/features/recipes/service.go` — service layer over the repository
- `internal/features/recipes/variance.go` — expected-vs-actual consumption
- `templates/fragments/admin/recipe.templ` — the recipe editor fragment
- `plugins/documents/migrations/00004_consumables_to_core.sql` — move existing paper rows into core recipes
- `migrations/0048_own_use_staff_movements.sql` — shop own-use and staff-welfare movement types

**Modify:**
- `internal/features/sales/service.go:246` — expand a stored recipe when no components were supplied
- `internal/web/web.go` — recipe + variance routes
- `internal/web/admin.go` — recipe editor handlers
- `internal/features/expenses/expenses.go` — optional service link
- `plugins/documents/cashier.go:118-131` — delete the `.Ceil()`, read core recipes
- `plugins/documents/documents.go:18,50,55,59-69` — display-name rename
- `plugins/documents/plugin.json` — display-name rename

---

### Task 1: Quantity precision

Fractional consumption is impossible today: `stock.quantity` is `numeric(12,3)`, so a toner yielding 5000 copies (0.0002 per copy) truncates to zero and deducts nothing. Widening to 6 decimals covers yields up to 1,000,000.

**Files:**
- Create: `migrations/0045_quantity_precision.sql`

**Interfaces:**
- Produces: quantity columns capable of 6 decimal places. Every later task depends on this; without it yield silently deducts nothing.

- [ ] **Step 1: Write the migration**

```sql
-- +goose Up
-- Quantities carried 3 decimal places, which is fine for sheets and bottles but
-- makes yield-based consumption impossible: a toner rated for 5000 copies is
-- 0.0002 per copy, which truncates to 0.000 and deducts nothing at all. Six
-- decimals covers a yield of 1,000,000.
--
-- Widening a numeric's scale rewrites no data and cannot fail on existing rows;
-- every current value is representable in the wider type.
ALTER TABLE stock          ALTER COLUMN quantity      TYPE numeric(14,6);
ALTER TABLE stock_batches  ALTER COLUMN qty_received  TYPE numeric(14,6);
ALTER TABLE stock_batches  ALTER COLUMN qty_remaining TYPE numeric(14,6);
ALTER TABLE stock_movements ALTER COLUMN quantity     TYPE numeric(14,6);

-- +goose Down
-- Narrowing rounds values that use the extra precision. Round explicitly first
-- so the change is visible in the data rather than silently applied by the cast.
UPDATE stock           SET quantity      = round(quantity, 3);
UPDATE stock_batches   SET qty_received  = round(qty_received, 3),
                           qty_remaining = round(qty_remaining, 3);
UPDATE stock_movements SET quantity      = round(quantity, 3);

ALTER TABLE stock           ALTER COLUMN quantity      TYPE numeric(12,3);
ALTER TABLE stock_batches   ALTER COLUMN qty_received  TYPE numeric(12,3);
ALTER TABLE stock_batches   ALTER COLUMN qty_remaining TYPE numeric(12,3);
ALTER TABLE stock_movements ALTER COLUMN quantity      TYPE numeric(12,3);
```

- [ ] **Step 2: Confirm `stock_movements.quantity` exists with that name**

Run: `docker compose exec -T postgres psql -U pos_user -d pos_db -c "\d stock_movements" | grep quantity`
Expected: a `quantity | numeric(12,3)` line. If the column is named differently, correct the migration before applying.

- [ ] **Step 3: Apply and verify the new precision**

Run:
```bash
make migrate
docker compose exec -T postgres psql -U pos_user -d pos_db -tAc "
SELECT table_name||'.'||column_name||' ('||numeric_precision||','||numeric_scale||')'
FROM information_schema.columns
WHERE (table_name,column_name) IN
 (('stock','quantity'),('stock_batches','qty_remaining'),('stock_movements','quantity'))
ORDER BY 1;"
```
Expected: every row reports `(14,6)`.

- [ ] **Step 4: Verify a sub-milli quantity round-trips**

Run:
```bash
docker compose exec -T postgres psql -U pos_user -d pos_db -tAc "
BEGIN;
UPDATE stock SET quantity = 0.000200 WHERE product_id = (SELECT min(product_id) FROM stock) RETURNING quantity;
ROLLBACK;"
```
Expected: `0.000200` — not `0.000`.

- [ ] **Step 5: Commit**

```bash
git add migrations/0045_quantity_precision.sql
git commit -m "feat(stock): widen quantity precision to 6dp for fractional consumption"
```

---

### Task 2: Recipe schema

**Files:**
- Create: `migrations/0046_product_recipes.sql`

**Interfaces:**
- Produces: table `product_recipes(id, product_id, component_product_id, qty_per_unit, yield_units, whole_units, note)`. Task 3 reads these columns by exactly these names.

- [ ] **Step 1: Write the migration**

```sql
-- +goose Up
-- A recipe is what one unit of a SERVICE consumes: a photocopy eats a sheet and
-- a slice of toner; a coffee eats powder, a cup and water.
--
-- Two ways to state a quantity, because shops think in both:
--   qty_per_unit — "18 grams of powder per cup"
--   yield_units  — "this bag makes 50 cups"  (consumed = qty / yield_units)
-- Exactly one is set. Yield is stored as the yield, never as its reciprocal:
-- 1/3000 written into a fixed-scale column loses precision, and the number the
-- owner actually knows is "3000", not "0.000333".
--
-- whole_units marks a component that cannot be part-used. A sheet of paper is
-- whole (a 1-copy job consumes 1 sheet, never 0.5); grams of coffee are not.
-- The documents plugin used to Ceil() EVERY component, which is right for paper
-- and catastrophic for a yield-based one — a single copy consumed a whole toner.
CREATE TABLE product_recipes (
	id                   BIGSERIAL PRIMARY KEY,
	product_id           BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
	component_product_id BIGINT NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
	qty_per_unit         NUMERIC(14,6),
	yield_units          NUMERIC(14,4),
	whole_units          BOOLEAN NOT NULL DEFAULT false,
	note                 TEXT,
	created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),

	-- Exactly one way of stating the quantity, both strictly positive.
	CONSTRAINT product_recipes_qty_xor_yield CHECK (
		(qty_per_unit IS NOT NULL AND qty_per_unit > 0 AND yield_units IS NULL)
		OR
		(yield_units IS NOT NULL AND yield_units > 0 AND qty_per_unit IS NULL)
	),
	-- A product consuming itself would deplete stock it does not have.
	CONSTRAINT product_recipes_no_self CHECK (product_id <> component_product_id),
	-- One row per (service, component): two rows for the same ingredient would
	-- double-consume it, which is how the old size-specific/NULL-size fallback
	-- could go wrong.
	CONSTRAINT product_recipes_unique_component UNIQUE (product_id, component_product_id)
);

CREATE INDEX idx_product_recipes_product ON product_recipes(product_id);

-- +goose Down
DROP TABLE product_recipes;
```

- [ ] **Step 2: Apply it**

Run: `make migrate`
Expected: `successfully migrated database to version: 46`

- [ ] **Step 3: Verify both constraints actually bite**

Run:
```bash
docker compose exec -T postgres psql -U pos_user -d pos_db -c "
BEGIN;
-- both quantity forms set: must fail
INSERT INTO product_recipes (product_id, component_product_id, qty_per_unit, yield_units)
VALUES (627, 628, 1, 50);
ROLLBACK;"
```
Expected: `ERROR ... product_recipes_qty_xor_yield`

Run:
```bash
docker compose exec -T postgres psql -U pos_user -d pos_db -c "
BEGIN;
INSERT INTO product_recipes (product_id, component_product_id, qty_per_unit) VALUES (627, 627, 1);
ROLLBACK;"
```
Expected: `ERROR ... product_recipes_no_self`

- [ ] **Step 4: Commit**

```bash
git add migrations/0046_product_recipes.sql
git commit -m "feat(recipes): product_recipes table with yield and whole-unit components"
```

---

### Task 3: Consumption arithmetic

The heart of the feature, and pure arithmetic — so it is tested without a database.

**Files:**
- Create: `internal/features/recipes/resolve.go`
- Create: `internal/features/recipes/resolve_test.go`

**Interfaces:**
- Produces:
  - `type Component struct { ComponentProductID int64; QtyPerUnit, YieldUnits decimal.NullDecimal; WholeUnits bool; ComponentName, UnitAbbr string }`
  - `func (c Component) Consumed(saleQty decimal.Decimal) decimal.Decimal`
  - `func Expand(cs []Component, saleQty decimal.Decimal) []Consumption`
  - `type Consumption struct { ProductID int64; Qty decimal.Decimal }`
- Task 4 calls `Expand`. Task 7 calls it from the documents plugin. Task 8 calls `Consumed` for the variance report.

- [ ] **Step 1: Write the failing tests**

```go
package recipes

import (
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { d, _ := decimal.NewFromString(s); return d }

func qty(s string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: dec(s), Valid: true}
}

// A whole-unit component always rounds UP: a single copy uses a whole sheet,
// and three double-sided impressions still use two sheets.
func TestConsumedWholeUnitsRoundsUp(t *testing.T) {
	sheet := Component{ComponentProductID: 1, QtyPerUnit: qty("1"), WholeUnits: true}
	cases := map[string]string{"1": "1", "3": "3", "10": "10"}
	for in, want := range cases {
		if got := sheet.Consumed(dec(in)); !got.Equal(dec(want)) {
			t.Errorf("Consumed(%s) = %s, want %s", in, got, want)
		}
	}
	half := Component{ComponentProductID: 1, QtyPerUnit: qty("0.5"), WholeUnits: true}
	if got := half.Consumed(dec("3")); !got.Equal(dec("2")) {
		t.Errorf("half-sheet x3 = %s, want 2 (rounded up)", got)
	}
}

// The bug this feature exists to fix: a yield component must NOT round up, or a
// one-copy job consumes an entire toner cartridge.
func TestConsumedYieldStaysFractional(t *testing.T) {
	toner := Component{ComponentProductID: 2, YieldUnits: qty("5000")}
	got := toner.Consumed(dec("1"))
	if !got.Equal(dec("0.0002")) {
		t.Fatalf("1 copy of a 5000-yield toner consumed %s, want 0.0002", got)
	}
	if got.Equal(dec("1")) {
		t.Fatal("yield component rounded up to a whole unit — the Ceil bug is back")
	}
	if got := toner.Consumed(dec("2500")); !got.Equal(dec("0.5")) {
		t.Errorf("2500 copies = %s, want 0.5 of a cartridge", got)
	}
}

// Grams of coffee: fractional and stated per unit, no yield involved.
func TestConsumedFractionalPerUnit(t *testing.T) {
	powder := Component{ComponentProductID: 3, QtyPerUnit: qty("18")}
	if got := powder.Consumed(dec("3")); !got.Equal(dec("54")) {
		t.Errorf("3 cups x 18g = %s, want 54", got)
	}
}

// A yield that does not divide evenly must not be silently truncated to zero.
func TestConsumedAwkwardYieldKeepsPrecision(t *testing.T) {
	bag := Component{ComponentProductID: 4, YieldUnits: qty("3000")}
	got := bag.Consumed(dec("1"))
	if got.IsZero() {
		t.Fatal("1/3000 truncated to zero — precision lost")
	}
	// Six decimal places is what the stock columns can store (migration 0045).
	if got.Exponent() < -6 {
		t.Errorf("Consumed returned %s, finer than stock can store (6dp)", got)
	}
}

func TestExpandSkipsNothingAndSumsPerComponent(t *testing.T) {
	cs := []Component{
		{ComponentProductID: 1, QtyPerUnit: qty("1"), WholeUnits: true},
		{ComponentProductID: 2, YieldUnits: qty("5000")},
		{ComponentProductID: 3, QtyPerUnit: qty("18")},
	}
	out := Expand(cs, dec("10"))
	if len(out) != 3 {
		t.Fatalf("Expand returned %d consumptions, want 3", len(out))
	}
	want := map[int64]string{1: "10", 2: "0.002", 3: "180"}
	for _, c := range out {
		if !c.Qty.Equal(dec(want[c.ProductID])) {
			t.Errorf("product %d consumed %s, want %s", c.ProductID, c.Qty, want[c.ProductID])
		}
	}
}

// A zero or negative sale quantity must consume nothing rather than produce a
// negative stock movement.
func TestExpandIgnoresNonPositiveQuantity(t *testing.T) {
	cs := []Component{{ComponentProductID: 1, QtyPerUnit: qty("1")}}
	if out := Expand(cs, dec("0")); len(out) != 0 {
		t.Errorf("qty 0 produced %d consumptions, want 0", len(out))
	}
	if out := Expand(cs, dec("-5")); len(out) != 0 {
		t.Errorf("negative qty produced %d consumptions, want 0", len(out))
	}
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `go test ./internal/features/recipes/ -v`
Expected: compilation failure — `undefined: Component`, `undefined: Expand`.

- [ ] **Step 3: Write the implementation**

```go
// Package recipes owns product bills of materials: what one unit of a service
// consumes when it is sold. The core sale transaction already depletes declared
// components FEFO and uses their summed cost as the line's COGS; a recipe is
// simply a stored, reusable declaration of those components.
package recipes

import "github.com/shopspring/decimal"

// stockScale is the number of decimal places the stock columns can hold
// (migration 0045). Consumption is rounded to it so what is computed is exactly
// what is stored, rather than being silently truncated by the database.
const stockScale = 6

// Component is one ingredient of a recipe. Exactly one of QtyPerUnit and
// YieldUnits is set, enforced by product_recipes_qty_xor_yield.
type Component struct {
	ComponentProductID int64               `db:"component_product_id"`
	QtyPerUnit         decimal.NullDecimal `db:"qty_per_unit"`
	YieldUnits         decimal.NullDecimal `db:"yield_units"`
	WholeUnits         bool                `db:"whole_units"`
	// joined, for display
	ComponentName string `db:"component_name"`
	UnitAbbr      string `db:"unit_abbr"`
}

// Consumption is one component and how much of it a sale line uses.
type Consumption struct {
	ProductID int64
	Qty       decimal.Decimal
}

// Consumed returns how much of this component a sale of saleQty units eats.
//
// A yield component divides (1 unit spread across YieldUnits sales) and stays
// fractional. A whole-unit component rounds UP, because a single copy uses a
// whole sheet of paper. Applying that rounding to everything — which the
// documents plugin used to do — made a one-copy job consume an entire toner.
func (c Component) Consumed(saleQty decimal.Decimal) decimal.Decimal {
	if !saleQty.IsPositive() {
		return decimal.Zero
	}
	var per decimal.Decimal
	switch {
	case c.YieldUnits.Valid && c.YieldUnits.Decimal.IsPositive():
		// DivRound rather than storing a reciprocal: the owner knows "50 cups",
		// and 1/3000 written into a fixed-scale column loses precision.
		per = decimal.NewFromInt(1).DivRound(c.YieldUnits.Decimal, stockScale+2)
	case c.QtyPerUnit.Valid:
		per = c.QtyPerUnit.Decimal
	default:
		return decimal.Zero
	}
	used := per.Mul(saleQty)
	if c.WholeUnits {
		return used.Ceil()
	}
	return used.Round(stockScale)
}

// Expand turns a recipe into the component list the sale transaction consumes.
// Components that work out to nothing are dropped rather than emitted as
// zero-quantity movements.
func Expand(cs []Component, saleQty decimal.Decimal) []Consumption {
	out := make([]Consumption, 0, len(cs))
	for _, c := range cs {
		q := c.Consumed(saleQty)
		if q.IsPositive() {
			out = append(out, Consumption{ProductID: c.ComponentProductID, Qty: q})
		}
	}
	return out
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/features/recipes/ -v`
Expected: all six tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/features/recipes/resolve.go internal/features/recipes/resolve_test.go
git commit -m "feat(recipes): consumption arithmetic with yield and whole-unit rounding"
```

---

### Task 4: Repository, service, and the sale path

**Files:**
- Create: `internal/features/recipes/recipes.go`
- Create: `internal/features/recipes/service.go`
- Modify: `internal/features/sales/service.go:246`

**Interfaces:**
- Consumes: `recipes.Component`, `recipes.Expand` from Task 3.
- Produces:
  - `func NewRepository(q db.Queryer) *Repository`
  - `func (r *Repository) For(ctx context.Context, productID int64) ([]Component, error)`
  - `func (r *Repository) Replace(ctx context.Context, productID int64, cs []Component) error`
  - `func NewService(db *sqlx.DB) *Service` with `For` and `Replace`
- Task 5 calls `Replace` from the admin UI; Task 7 calls `For` from the documents plugin.

- [ ] **Step 1: Write the repository**

```go
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
```

- [ ] **Step 2: Write the service**

```go
package recipes

import (
	"context"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
)

type Service struct {
	db   *sqlx.DB
	repo *Repository
}

func NewService(db *sqlx.DB) *Service { return &Service{db: db, repo: NewRepository(db)} }

func (s *Service) For(ctx context.Context, productID int64) ([]Component, error) {
	cs, err := s.repo.For(ctx, productID)
	if err != nil {
		return nil, apperr.Internal("failed to load recipe", err)
	}
	return cs, nil
}

// Replace validates and stores a product's whole recipe.
func (s *Service) Replace(ctx context.Context, productID int64, cs []Component) error {
	for _, c := range cs {
		if c.ComponentProductID == productID {
			return apperr.Validation("a product cannot consume itself")
		}
		qtySet := c.QtyPerUnit.Valid && c.QtyPerUnit.Decimal.IsPositive()
		yieldSet := c.YieldUnits.Valid && c.YieldUnits.Decimal.IsPositive()
		if qtySet == yieldSet {
			return apperr.Validation("each ingredient needs either a quantity per unit or a yield, not both")
		}
	}
	return appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		return s.repo.Replace(ctx, tx, productID, cs)
	})
}
```

- [ ] **Step 3: Expand the recipe inside the sale transaction**

In `internal/features/sales/service.go`, inside the `if p.IsService {` branch at line 246, immediately BEFORE the `for _, comp := range it.Components {` loop, insert:

```go
			// A stored recipe supplies the components when the caller did not.
			// The documents plugin passes explicit components (its paper choice
			// depends on the size picked at the till), so an explicit list always
			// wins; everything else — coffee, any service with a recipe — is
			// expanded here so no plugin is needed to sell it.
			if len(it.Components) == 0 {
				rcs, rerr := recipes.NewRepository(tx).For(ctx, p.ID)
				if rerr != nil {
					return apperr.Internal("failed to load recipe", rerr)
				}
				for _, cons := range recipes.Expand(rcs, qty) {
					it.Components = append(it.Components, ServiceComponent{
						ProductID: cons.ProductID,
						Quantity:  cons.Qty.String(),
					})
				}
			}
```

Add `"karots-pos/internal/features/recipes"` to the imports of that file.

- [ ] **Step 4: Verify it builds and the existing suite still passes**

Run: `go build ./... && go test ./... 2>&1 | grep -vE "no test files|^ok"`
Expected: no output (all packages pass).

- [ ] **Step 5: Verify a recipe is consumed end to end**

Run:
```bash
docker compose exec -T postgres psql -U pos_user -d pos_db -c "
SELECT id, name FROM products WHERE is_service AND is_active ORDER BY id LIMIT 3;"
```
Pick a service id and a stocked component id, insert a recipe row, sell one through the till, then confirm the component's stock dropped and `sale_items.cost_price` is non-zero for that line.

- [ ] **Step 6: Commit**

```bash
git add internal/features/recipes/ internal/features/sales/service.go
git commit -m "feat(recipes): expand stored recipes into consumed components at sale time"
```

---

### Task 5: Recipe editor

**Files:**
- Create: `templates/fragments/admin/recipe.templ`
- Modify: `internal/web/admin.go`, `internal/web/web.go`

**Interfaces:**
- Consumes: `recipes.Service.For`, `recipes.Service.Replace` from Task 4.
- Produces: routes `GET /admin/products/:id/recipe` and `POST /admin/products/:id/recipe`.

- [ ] **Step 1: Add the service to the web Server struct**

In `internal/web/web.go`, add `recipes *recipes.Service` to the `Server` struct and `recipes: recipes.NewService(db),` to its construction, plus the import.

- [ ] **Step 2: Write the editor fragment**

Create `templates/fragments/admin/recipe.templ`:

```go
package adminfragments

import (
	"strconv"

	"karots-pos/internal/features/products"
	"karots-pos/internal/features/recipes"
)

// RecipeForm edits what one service consumes. Rows post as parallel arrays;
// every row emits a hidden whole[] of "0" before its checkbox so an unchecked
// box still occupies its index — otherwise the arrays fall out of alignment and
// the wrong ingredient gets marked whole-unit.
templ RecipeForm(p products.Product, cs []recipes.Component) {
	@modalShell("Recipe — " + p.Name) {
		<form class="p-6 space-y-4" hx-post={ "/admin/products/" + strconv.FormatInt(p.ID, 10) + "/recipe" } hx-swap="none"
			x-data="recipeEditor()">
			<p class="text-sm text-slate-500">
				What one { p.UnitAbbr } of “{ p.Name }” uses. Stock is deducted and its cost becomes this line's cost when sold.
			</p>
			<table class="w-full text-sm">
				<thead class="text-left text-slate-500 border-b">
					<tr>
						<th class="py-1.5">Ingredient</th>
						<th class="py-1.5">Amount</th>
						<th class="py-1.5">Meaning</th>
						<th class="py-1.5 text-center">Whole units</th>
						<th class="py-1.5"></th>
					</tr>
				</thead>
				<tbody>
					for _, c := range cs {
						<tr class="border-b last:border-0">
							<td class="py-2">
								<input type="hidden" name="component_id[]" value={ strconv.FormatInt(c.ComponentProductID, 10) }/>
								{ c.ComponentName } <span class="text-slate-400">({ c.UnitAbbr })</span>
							</td>
							<td class="py-2">
								<input name="amount[]" type="number" step="0.000001" min="0" required
									value={ recipeAmount(c) } class="w-28 border rounded-lg px-2 py-1"/>
							</td>
							<td class="py-2">
								<select name="mode[]" class="border rounded-lg px-2 py-1">
									<option value="per_unit" selected?={ c.QtyPerUnit.Valid }>per unit sold</option>
									<option value="yield" selected?={ c.YieldUnits.Valid }>makes this many</option>
								</select>
							</td>
							<td class="py-2 text-center">
								<input type="hidden" name="whole[]" value="0"/>
								<input type="checkbox" name="whole[]" value="1" checked?={ c.WholeUnits } class="rounded"/>
							</td>
							<td class="py-2 text-right">
								<button type="button" class="text-rose-600 text-sm" x-on:click="$el.closest('tr').remove()">Remove</button>
							</td>
						</tr>
					}
					<template x-for="row in added" x-bind:key="row.id">
						<tr class="border-b last:border-0">
							<td class="py-2">
								<input type="hidden" name="component_id[]" x-bind:value="row.id"/>
								<span x-text="row.name"></span>
							</td>
							<td class="py-2"><input name="amount[]" type="number" step="0.000001" min="0" required class="w-28 border rounded-lg px-2 py-1"/></td>
							<td class="py-2">
								<select name="mode[]" class="border rounded-lg px-2 py-1">
									<option value="per_unit">per unit sold</option>
									<option value="yield">makes this many</option>
								</select>
							</td>
							<td class="py-2 text-center">
								<input type="hidden" name="whole[]" value="0"/>
								<input type="checkbox" name="whole[]" value="1" class="rounded"/>
							</td>
							<td class="py-2 text-right">
								<button type="button" class="text-rose-600 text-sm" x-on:click="added = added.filter(r => r.id !== row.id)">Remove</button>
							</td>
						</tr>
					</template>
				</tbody>
			</table>
			<div>
				<label class="block text-sm font-medium mb-1">Add an ingredient</label>
				@ProductPicker("recipe_add", 0, "", "Search stock…")
			</div>
			<p class="text-xs text-slate-500">
				“per unit sold” = 18 g of powder in every cup. “makes this many” = this bag makes 50 cups.
				Tick <strong>whole units</strong> for something that cannot be part-used, like a sheet of paper.
			</p>
			@modalButtons()
		</form>
	}
}

// recipeAmount shows whichever of the two quantity forms this component uses.
func recipeAmount(c recipes.Component) string {
	if c.YieldUnits.Valid {
		return c.YieldUnits.Decimal.String()
	}
	if c.QtyPerUnit.Valid {
		return c.QtyPerUnit.Decimal.String()
	}
	return ""
}
```

Add a `recipeEditor()` Alpine component to `static/js/app.js` holding `added: []`, appending `{id, name}` when the ProductPicker fires its choose event, and refusing an id already present in the table (the `product_recipes_unique_component` constraint would reject a duplicate anyway, but rejecting it in the UI explains why).

- [ ] **Step 3: Write the handlers**

```go
// ProductRecipeForm opens the recipe editor for a service product.
func (a *adminUI) ProductRecipeForm(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	p, err := a.s.products.Get(ctx, id)
	if err != nil {
		return err
	}
	// Recipes only make sense for a service: a stocked product has inventory of
	// its own, and consuming ingredients as well would count the cost twice.
	if !p.IsService {
		return apperr.Validation("only service products can have a recipe")
	}
	cs, err := a.s.recipes.For(ctx, id)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminfragments.RecipeForm(*p, cs))
}

// ProductRecipeSave replaces a product's whole recipe.
func (a *adminUI) ProductRecipeSave(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	form, err := c.FormParams()
	if err != nil {
		return apperr.BadRequest("invalid form")
	}
	ids := form["component_id[]"]
	amounts := form["amount[]"]
	modes := form["mode[]"]
	wholes := form["whole[]"]
	cs := make([]recipes.Component, 0, len(ids))
	for i, raw := range ids {
		cid, perr := strconv.ParseInt(raw, 10, 64)
		if perr != nil || cid <= 0 || i >= len(amounts) || i >= len(modes) {
			continue
		}
		amt, aerr := money.Parse(amounts[i])
		if aerr != nil || !amt.IsPositive() {
			return apperr.Validation("each ingredient needs a positive amount")
		}
		comp := recipes.Component{ComponentProductID: cid}
		if modes[i] == "yield" {
			comp.YieldUnits = decimal.NullDecimal{Decimal: amt, Valid: true}
		} else {
			comp.QtyPerUnit = decimal.NullDecimal{Decimal: amt, Valid: true}
		}
		comp.WholeUnits = i < len(wholes) && wholes[i] == "1"
		cs = append(cs, comp)
	}
	if err := a.s.recipes.Replace(c.Request().Context(), id, cs); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionUpdate, "product", strconv.FormatInt(id, 10), "recipe updated")
	return htmxDone(c, "Recipe saved", "reload-products")
}
```

Note: `whole[]` checkboxes do not post when unchecked, so index alignment with `component_id[]` is not guaranteed. Render a hidden `whole[]` input with value `0` immediately before each checkbox (value `1`) so every row posts exactly one value.

- [ ] **Step 4: Register the routes**

In `internal/web/web.go`, beside the other product routes:

```go
	ag.GET("/products/:id/recipe", admin.ProductRecipeForm)
	ag.POST("/products/:id/recipe", admin.ProductRecipeSave)
```

- [ ] **Step 5: Verify**

Run: `make templ && go build ./... && make css`
Then open a service product, add an ingredient by yield, save, reopen, and confirm the value round-trips.

- [ ] **Step 6: Commit**

```bash
git add templates/fragments/admin/recipe.templ internal/web/admin.go internal/web/web.go
git commit -m "feat(recipes): admin recipe editor for service products"
```

---

### Task 6: Attributable expenses

**Files:**
- Create: `migrations/0047_expense_service_link.sql`
- Modify: `internal/features/expenses/expenses.go`

**Interfaces:**
- Produces: `expenses.Expense.ServiceProductID *int64` and a filter by it, used by Task 8's report line.

- [ ] **Step 1: Write the migration**

```sql
-- +goose Up
-- A toner change or a machine repair is a real cost of running the photocopy
-- counter, but it is not consumed per copy — so it must not enter COGS, where
-- it would distort every line's margin. Tagging the expense to the service it
-- belongs to lets that service's report subtract it without touching the core
-- P&L, which keeps counting it once, as an operating expense.
ALTER TABLE expenses ADD COLUMN service_product_id BIGINT
	REFERENCES products(id) ON DELETE SET NULL;

CREATE INDEX idx_expenses_service_product ON expenses(service_product_id)
	WHERE service_product_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_expenses_service_product;
ALTER TABLE expenses DROP COLUMN service_product_id;
```

- [ ] **Step 2: Add the field to the model and the insert/select**

Add `ServiceProductID *int64 \`db:"service_product_id" json:"service_product_id,omitempty"\`` to the `Expense` struct and to `CreateInput` as a form field, then include the column in the repository's `INSERT` and `SELECT`.

- [ ] **Step 3: Add the picker to the expense form**

In the expense form template, add an optional select listing active service products, labelled "Relates to service (optional)" with a hint: *"Tag machine, toner or servicing costs so they show against that service's profit. Still counted once, as an operating expense."*

- [ ] **Step 4: Verify**

Run: `make migrate && make templ && go build ./...`
Create an expense tagged to a service and confirm the column is populated.

- [ ] **Step 5: Commit**

```bash
git add migrations/0047_expense_service_link.sql internal/features/expenses/
git commit -m "feat(expenses): optional service attribution for machine and servicing costs"
```

---

### Task 7: Move the documents plugin onto core recipes

**Files:**
- Create: `plugins/documents/migrations/00004_consumables_to_core.sql`
- Modify: `plugins/documents/cashier.go:118-131`

**Interfaces:**
- Consumes: `recipes.Expand` from Task 3, `recipes.Repository.For` from Task 4.

- [ ] **Step 1: Copy existing consumables into core recipes**

```sql
-- +goose Up
-- doc_consumable was a per-service bill of materials that only the documents
-- plugin could read. Core now owns recipes, so the rows move across and the
-- plugin reads them from there.
--
-- Size-specific rows cannot move: core recipes key on the product alone, and
-- the paper a job consumes depends on the size chosen at the till. Those stay
-- in doc_consumable and the plugin keeps resolving them. Only size-agnostic
-- rows (NULL size) — toner, ink, anything used regardless of paper size — are
-- the ones core can own.
INSERT INTO product_recipes (product_id, component_product_id, qty_per_unit, whole_units)
SELECT s.product_id, dc.product_id, dc.qty_per_unit, true
FROM doc_consumable dc
JOIN doc_service s ON s.id = dc.service_id
WHERE dc.size IS NULL
ON CONFLICT (product_id, component_product_id) DO NOTHING;

-- +goose Down
-- Only remove what this migration could have inserted.
DELETE FROM product_recipes pr
USING doc_consumable dc JOIN doc_service s ON s.id = dc.service_id
WHERE dc.size IS NULL
  AND pr.product_id = s.product_id
  AND pr.component_product_id = dc.product_id;
```

- [ ] **Step 2: Remove the Ceil and honour whole_units**

In `plugins/documents/cashier.go`, replace the consumable loop (lines 124-131) with:

```go
	cs, _ := h.p.store.ConsumablesFor(ctx, sid, size)
	comps := make([]map[string]any, 0, len(cs))
	consCost := decimal.Zero
	base := decimal.NewFromInt(int64(baseUnits))
	for _, cm := range cs {
		// Paper is a whole unit — a single copy uses a whole sheet. This used to
		// Ceil() every component, so a yield-based one (a toner rated for 5000
		// copies) consumed an entire cartridge on a one-copy job.
		comp := recipes.Component{
			ComponentProductID: cm.ProductID,
			QtyPerUnit:         decimal.NullDecimal{Decimal: cm.QtyPerUnit, Valid: true},
			WholeUnits:         true,
		}
		consumed := comp.Consumed(base)
		comps = append(comps, map[string]any{"product_id": cm.ProductID, "quantity": consumed.String()})
		consCost = consCost.Add(consumed.Mul(h.p.store.ConsumableCost(ctx, cm.ProductID)))
	}

	// Size-agnostic ingredients now live in core recipes (toner, ink) and are
	// fractional, so they are expanded without rounding up.
	core, _ := recipes.NewRepository(h.p.core.DB).For(ctx, sv.ProductID)
	for _, cons := range recipes.Expand(core, base) {
		comps = append(comps, map[string]any{"product_id": cons.ProductID, "quantity": cons.Qty.String()})
		consCost = consCost.Add(cons.Qty.Mul(h.p.store.ConsumableCost(ctx, cons.ProductID)))
	}
```

Confirm the plugin's Core API exposes the `*sqlx.DB` as `h.p.core.DB`; if the field is named differently, use the actual name.

- [ ] **Step 3: Verify no double-consumption**

Because size-specific rows stay in `doc_consumable` and only NULL-size rows moved to core, a component must never appear in both. Run:

```bash
docker compose exec -T postgres psql -U pos_user -d pos_db -c "
SELECT s.name, p.name AS component
FROM doc_consumable dc
JOIN doc_service s ON s.id = dc.service_id
JOIN products p ON p.id = dc.product_id
JOIN product_recipes pr ON pr.product_id = s.product_id AND pr.component_product_id = dc.product_id
WHERE dc.size IS NOT NULL;"
```
Expected: no rows. Any row here would be consumed twice per sale.

- [ ] **Step 4: Sell one photocopy and confirm one sheet moved**

Run a 1-copy A4 job through the till, then check `stock_movements` for exactly one `-1` against A4 Sheet and no other consumption.

- [ ] **Step 5: Commit**

```bash
git add plugins/documents/migrations/00004_consumables_to_core.sql plugins/documents/cashier.go
git commit -m "fix(documents): stop rounding every consumable up; read core recipes"
```

---

### Task 8: Yield variance report

A bag says 50 cups and gives 48 or 53. Yield is an estimate, so the system must show the drift rather than pretend the number is exact.

**Files:**
- Create: `internal/features/recipes/variance.go`

**Interfaces:**
- Consumes: `recipes.Component`, `Consumed` from Task 3.
- Produces: `func (s *Service) Variance(ctx context.Context, from, to time.Time) ([]VarianceRow, error)` and:

```go
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
```

- [ ] **Step 1: Write the query**

Expected consumption is what the recipes say should have been used for the services sold in the period. Actual is what `stock_movements` records for those components over the same period. The difference is the drift in the yield estimate.

```go
// Variance compares what the recipes SAY was consumed against what stock
// actually moved. A yield is an estimate — a bag rated for 50 cups may give 48
// or 53 — so this is the feedback loop that lets the estimate be corrected
// instead of quietly bleeding stock.
//
// Expected: for every sale line of a service with a recipe, the recipe's
// consumption for that line's quantity.
// Actual: the stock movements those sales actually produced.
func (s *Service) Variance(ctx context.Context, from, to time.Time) ([]VarianceRow, error) {
	var rows []VarianceRow
	err := s.db.SelectContext(ctx, &rows, `
		WITH expected AS (
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
	return rows, err
}
```

- [ ] **Step 2: Add the report page**

Register the hub card by adding this line to the `cards` slice in `templates/pages/admin/helpers.go:reportHubCards`:

```go
		{"/admin/reports/recipe-variance", "Recipe Variance", "Expected vs actual ingredient use"},
```

Add the route in `internal/web/web.go` beside the other report routes:

```go
	ag.GET("/reports/recipe-variance", admin.RecipeVarianceReport)
```

And the handler in `internal/web/admin_reports.go`, following the shape every other report in that file now uses — range presets, CSV, and the shared pager:

```go
// RecipeVarianceReport compares what recipes say was consumed against what
// stock actually moved. A yield is an estimate ("this bag makes 50 cups"), so
// drift is expected; this is what makes the drift visible instead of letting it
// quietly bleed stock.
func (a *adminUI) RecipeVarianceReport(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, preset, err := rangeStrings(c)
	if err != nil {
		return err
	}
	rows, err := a.s.recipes.Variance(ctx, from, to)
	if err != nil {
		return err
	}
	if wantsCSV(c) {
		out := make([][]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, []string{
				r.ComponentName, r.UnitAbbr, r.Expected.String(), r.Actual.String(),
				r.Diff.String(), r.DriftPct().String(),
			})
		}
		return writeCSV(c, "recipe_variance_"+fromStr+"_"+toStr,
			[]string{"Ingredient", "Unit", "Expected", "Actual", "Difference", "Drift %"}, out)
	}
	page := pageParam(c)
	return response.RenderPage(c, adminpages.RecipeVarianceReport(adminpages.RecipeVarianceData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx),
		From: fromStr, To: toStr, Preset: preset,
		Rows:  paginate(rows, page, reportPageSize),
		Total: len(rows), Page: page, PageSize: reportPageSize,
	}))
}
```

The template `RecipeVarianceReport` follows `BatchReport` in `templates/pages/admin/mgmt_reports.templ`: a `@layouts.Report` shell, summary cards, the table, then `@rptTruncNote` and `@rptPager(rangeQuery(d.Preset, d.From, d.To), d.Page, d.PageSize, d.Total)`.

- [ ] **Step 3: Verify against real data**

Sell several units of a service with a recipe, then confirm expected and actual agree exactly (they should, since the same recipe drove both). Then adjust a component's stock by hand to simulate real-world drift and confirm the variance appears.

- [ ] **Step 4: Commit**

```bash
git add internal/features/recipes/variance.go templates/pages/admin/
git commit -m "feat(recipes): expected-vs-actual ingredient variance report"
```

---

### Task 9: Rename the plugin

With recipes in core, the plugin no longer owns "things that consume stock" — it owns metered, size-priced counter jobs. The name should say that.

**Files:**
- Modify: `plugins/documents/plugin.json`, `plugins/documents/documents.go:18,50,55,59-69`

- [ ] **Step 1: Change the display names only**

Set `"name": "Print & Copy Counter"` in `plugin.json`, and update the display strings in `documents.go`: `Name()` returns `"Print & Copy"`, the cashier tab `Label` becomes `"🖨 Print & Copy"`, and every `SectionLabel`/`Label` currently reading `"Communication Store"` becomes `"Print & Copy"`.

Leave `"key": "documents"`, the Go package path, and every `doc_*` table name unchanged. The key is referenced by `cmd/server/enabled_plugins.go` and the goose version table `goose_db_version_documents`; renaming those would strand the migration history and gain nothing a user can see.

- [ ] **Step 2: Verify no functional string was caught**

Run: `grep -rn "Communication Store" --include="*.go" --include="*.json" . | grep -v _templ`
Expected: no matches.

Run: `go build ./... && make templ`
Expected: clean.

- [ ] **Step 3: Confirm the plugin still loads under its old key**

Restart the server and confirm the admin section renders and the goose version table is still `goose_db_version_documents`.

- [ ] **Step 4: Commit**

```bash
git add plugins/documents/plugin.json plugins/documents/documents.go
git commit -m "refactor(documents): rename to Print & Copy in the UI, key unchanged"
```

---

### Task 10: Shop own-use and staff-welfare stock movements

Stock leaves for two legitimate reasons that are neither a sale nor a loss: the
shop uses its own stock (adhesive, a pen for the counter), and staff eat edible
stock. Today the only options are **Adjust**, which books no cost at all so the
money silently vanishes, and **Damage**, which books the cost as a *loss* and so
mislabels deliberate use as breakage.

Both get their own P&L line. Neither is recoverable — the shop absorbs them.

**Files:**
- Create: `migrations/0048_own_use_staff_movements.sql`
- Modify: `internal/features/reports/reports.go:20-36,112-119`
- Modify: `internal/features/stock/service.go`

**Interfaces:**
- Produces: movement types `own_use` and `staff`; `reports.PL.OwnUse` and
  `reports.PL.StaffWelfare`; `stock.Service.Consume(ctx, in ConsumeInput, userID)`.
- Task 11 calls `Consume` from the admin and cashier UIs.

- [ ] **Step 1: Write the migration**

```sql
-- +goose NO TRANSACTION
-- +goose Up
-- Stock leaves for reasons that are neither a sale nor a loss:
--   own_use — the shop consumed its own stock (adhesive, cleaning supplies)
--   staff   — staff ate or took stock
-- Adjust books no cost, so using it here would make the money disappear without
-- ever reaching the P&L. Damage books cost as a LOSS, which reads as breakage or
-- theft and hides how much the shop deliberately consumes. Both need their own
-- type so their cost can be reported on its own line.
--
-- NO TRANSACTION: PostgreSQL forbids using an enum value in the same
-- transaction that added it, and goose wraps migrations in one by default.
ALTER TYPE stock_movement_type ADD VALUE IF NOT EXISTS 'own_use';
ALTER TYPE stock_movement_type ADD VALUE IF NOT EXISTS 'staff';

-- +goose Down
-- PostgreSQL cannot drop a value from an enum. Rows of these types would have to
-- be re-typed and the enum rebuilt, which is not a safe automatic rollback, so
-- the values are deliberately left in place. They are inert when unused.
SELECT 1;
```

- [ ] **Step 2: Apply and verify both values exist**

Run:
```bash
make migrate
docker compose exec -T postgres psql -U pos_user -d pos_db -tAc "
SELECT unnest(enum_range(NULL::stock_movement_type));" | grep -E "own_use|staff"
```
Expected: both `own_use` and `staff` are listed.

- [ ] **Step 3: Add the two P&L lines**

In `internal/features/reports/reports.go`, add to the `PL` struct beside `Losses`:

```go
	OwnUse       decimal.Decimal `json:"own_use"`        // stock the shop consumed itself
	StaffWelfare decimal.Decimal `json:"staff_welfare"`  // stock taken by staff
```

Then, immediately after the existing losses query, add:

```go
	// Stock the shop consumed itself, and stock taken by staff. Kept off the
	// Losses line on purpose: both are deliberate and expected, and folding them
	// into losses would make breakage look worse than it is while hiding how
	// much the shop actually consumes.
	if err := s.db.GetContext(ctx, &pl.OwnUse, `
		SELECT COALESCE(SUM(cost),0) FROM stock_movements
		WHERE type = 'own_use' AND created_at >= $1 AND created_at < $2`,
		from, to); err != nil {
		return nil, apperr.Internal("failed to compute own-use cost", err)
	}
	if err := s.db.GetContext(ctx, &pl.StaffWelfare, `
		SELECT COALESCE(SUM(cost),0) FROM stock_movements
		WHERE type = 'staff' AND created_at >= $1 AND created_at < $2`,
		from, to); err != nil {
		return nil, apperr.Internal("failed to compute staff welfare cost", err)
	}
```

- [ ] **Step 4: Subtract them from net profit**

Find the `NetProfit` calculation (its comment reads
`gross - expenses - losses + recoveries + other income`) and subtract the two new
figures, updating that comment to match. Without this the P&L overstates profit
by exactly the cost of everything consumed internally — the same bug the Losses
line exists to prevent.

- [ ] **Step 5: Generalise the damage path**

`stock.Service.Damage` already decrements stock, depletes FEFO for the cost and
writes a movement. Add a `Consume` that does the same for an arbitrary reason,
and reimplement `Damage` as a call to it so there is one code path:

```go
// ConsumeInput records stock leaving for a non-sale reason.
type ConsumeInput struct {
	ProductID int64  `json:"product_id" form:"product_id" validate:"required,gt=0"`
	Quantity  string `json:"quantity"   form:"quantity"   validate:"required"`
	Reason    string `json:"reason"     form:"reason"`
	Note      string `json:"note"       form:"note"`
}

// consumeReasons maps a UI reason to its movement type. Anything not listed is
// rejected rather than defaulting, so a typo can never silently book stock to
// the wrong P&L line.
var consumeReasons = map[string]string{
	"damage":  MoveDamage,
	"own_use": MoveOwnUse,
	"staff":   MoveStaff,
}
```

Add `MoveOwnUse = "own_use"` and `MoveStaff = "staff"` to the movement-type
constants in `internal/features/stock/stock.go`.

- [ ] **Step 6: Verify the cost reaches the right line**

Record an own-use movement for a product with a known cost, then:

```bash
docker compose exec -T postgres psql -U pos_user -d pos_db -c "
SELECT type, quantity, cost FROM stock_movements WHERE type IN ('own_use','staff') ORDER BY id DESC LIMIT 5;"
```
Expected: a non-zero `cost`. A zero cost means FEFO depletion was skipped and the
P&L line will always read zero.

Then open Finance / P&L and confirm the figure appears on its own line and that
Stock losses did **not** change.

- [ ] **Step 7: Commit**

```bash
git add migrations/0048_own_use_staff_movements.sql internal/features/reports/reports.go internal/features/stock/
git commit -m "feat(stock): own-use and staff-welfare movements with their own P&L lines"
```

---

### Task 11: Recording shop use and staff consumption

The cashier is who notices that the counter needs adhesive or that someone took a
drink, so this must be reachable at the till — exactly like Damage already is.

**Files:**
- Modify: `internal/web/cashier.go`, `internal/web/admin_more.go`, `internal/web/web.go`
- Modify: `templates/pages/cashier/more.templ`, `templates/pages/admin/stock.templ`
- Modify: `templates/layouts/cashier.templ`

**Interfaces:**
- Consumes: `stock.Service.Consume`, `stock.ConsumeInput` from Task 10.

- [ ] **Step 1: Replace the Damage form with a reason-driven one**

The existing Damage modal already picks a product and a quantity. Add a reason
selector so one form covers all three, and reuse it in both admin and cashier:

```html
<div>
	<label class="block text-sm font-medium mb-1">Reason</label>
	<select name="reason" class="w-full border rounded-lg px-3 py-2">
		<option value="damage">Damaged / expired — written off as a loss</option>
		<option value="own_use">Shop used it — counter supplies, cleaning</option>
		<option value="staff">Staff took it — food, drinks</option>
	</select>
	<p class="text-xs text-slate-500 mt-1">
		Each reason reports on its own line, so deliberate use is never mistaken for breakage.
	</p>
</div>
```

- [ ] **Step 2: Point the handlers at Consume**

In both `cashierUI.DamageRecord` and `adminUI.DamageRecord`, bind
`stock.ConsumeInput` instead of `stock.DamageInput` and call
`h.s.stock.Consume(ctx, in, middleware.CurrentUserID(c))`. Keep the routes and
their names: `/cashier/damage` and `/admin/stock/damage` are already linked from
the nav, and renaming them would break bookmarks for no user benefit. Update the
nav labels from "Damage" to "Write-off" in `templates/layouts/cashier.templ`.

- [ ] **Step 3: Verify each reason lands on its own line**

Record one movement of each reason from the till, then confirm three distinct
types appear and that Finance shows three separate figures:

```bash
docker compose exec -T postgres psql -U pos_user -d pos_db -c "
SELECT type, count(*), SUM(cost) FROM stock_movements
WHERE type IN ('damage','own_use','staff') GROUP BY type ORDER BY type;"
```

- [ ] **Step 4: Verify the Stock Movements report can filter them**

The type filter added earlier lists movement types explicitly. Add `own_use` and
`staff` options to it in `templates/pages/admin/stock.templ`, then confirm
filtering by each returns only those rows.

- [ ] **Step 5: Commit**

```bash
git add internal/web/ templates/
git commit -m "feat(stock): record shop own-use and staff consumption from admin and till"
```

---

## Open decisions for the owner

**Size-specific paper stays in the plugin.** Core recipes key on the product alone, but which paper a job consumes depends on the size chosen at the till. Task 7 moves only size-agnostic ingredients (toner, ink) to core. If per-size recipes are wanted in core later, `product_recipes` needs a nullable variant key — deliberately not built now (YAGNI).

**Water as an ingredient.** A coffee consumes water, but few shops stock water as a counted product. Model it only if it is actually bought and counted; otherwise leave it out rather than carry a fictional stock item.
