# Clearance — Stale-Stock Markdowns Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship an optional `Clearance` plugin that flags products with stock but no sale in N days, suggests a margin-safe markdown the admin approves/adjusts, and offers to apply that discount at the till when the cashier rings the item.

**Architecture:** Everything reuses core's existing per-line discount (`sale_items.discount / discount_type / discount_value`) and the existing plugin badge/detail seams. The one new core piece is a generic `ProductSaleSuggestionProvider` seam that attaches an optional line-discount suggestion to a product's till API payload, bridged in `web.go` exactly like the existing badge provider. Detection, review UI, storage and settings all live inside the plugin. Core never imports the plugin.

**Tech Stack:** Go 1.x, Echo, sqlx, templ, HTMX + Alpine.js, PostgreSQL, goose migrations, shopspring/decimal.

**Spec:** `docs/superpowers/specs/2026-09-03-clearance-stale-markdowns-design.md`

## Global Constraints

- **Core never imports a plugin.** The plugin attaches only through generic hooks (`internal/plugin/hooks.go`) and `products` func-var seams. A core-only build must compile and run inert.
- **New seams must be generic**, not clearance-specific (a promo/happy-hour plugin must be able to reuse `ProductSaleSuggestionProvider`).
- **Discount fields match the DB contract:** `discount_type` is `"percent"` or `"fixed"`; the till cart line uses `discountType` (`"percent"`|`"fixed"`) and `discount` (string). Percent = off the line; fixed = per-unit × qty.
- **A markdown must never suggest a price at or below cost + min margin.** Suggested price is floored at `cost × (1 + min_margin_percent/100)`.
- **Committed `cmd/server/enabled_plugins.go` stays core-only.** A local dev import to test Clearance is temporary and must not be committed as a permanent default. The bootstrapper (`cmd/bootstrap`) selects plugins per-shop from `plugins/*/plugin.json`.
- **Plugin table/migration prefix is `clearance`** (goose version table `goose_db_version_clearance`).
- **Money/security logic gets a runnable test** (Go `_test.go`, `assert`-style, no new frameworks). Front-end wiring is verified with `node --check` + manual steps.
- Regenerate templ after editing any `.templ`: `templ generate templates/... plugins/clearance`. Commit only `.templ` source — generated `*_templ.go` are gitignored, including plugin ones (verified: `plugins/alternatives/pages_templ.go` is NOT tracked). Do NOT `git add` `plugins/clearance/pages_templ.go`.
- Plugin admin pages render via `response.RenderPage(c, PageTempl(...))` (verified pattern in `plugins/alternatives/admin.go`), never `c.Render`.
- A plugin reads the currency symbol from `p.core.Settings.Get(ctx)` → `cfg.CurrencySymbol` (verified in `plugins/recharge`), NOT `core.Cfg`.
- The till has NO promise-returning `confirm()`. Yes/no prompts use a state-object + a themed modal in `pos.templ`, resolved by two methods — the `oversellPrompt` / `approveOversell` / `cancelOversell` pattern (`static/js/app.js` ~1996, `pos.templ` ~919). Task 3 follows this exactly.

---

## File Structure

**Core (small, additive):**
- `internal/plugin/hooks.go` — new `SaleSuggestion` type, `ProductSaleSuggestionProvider` type, `AddProductSaleSuggestionProvider` registrar, `ProductSaleSuggestionProviders()` accessor, backing slice.
- `internal/features/products/products.go` — `Product.Suggestion *SaleSuggestion` field, `SaleSuggestion` struct, `SaleSuggestionProvider` func-var.
- `internal/features/products/api.go` — populate `Suggestion` in `List`, `Get`, `QuickPicks`.
- `internal/web/web.go` — bridge `plugin.ProductSaleSuggestionProviders()` → `products.SaleSuggestionProvider`.
- `static/js/app.js` — `addToCart` reads `p.suggestion`, prompts, applies the line discount.

**New plugin `plugins/clearance/`:**
- `clearance.go` — `init()`+`Setup`: routes, nav, hook registration.
- `store.go` — staleness query, markdown clamp math, settings, CRUD, badge/detail/suggestion providers.
- `store_test.go` — clamp + staleness selection tests.
- `admin.go` — admin handlers (list/approve/adjust/dismiss/settings).
- `pages.templ` (+ committed `pages_templ.go`) — the review page.
- `migrations/0001_clearance.sql`, `migrations/embed.go` — tables.
- `plugin.json` — bootstrapper manifest.

---

## Task 1: Core seam — `ProductSaleSuggestionProvider`

**Files:**
- Modify: `internal/plugin/hooks.go`
- Test: `internal/plugin/sale_suggestion_test.go` (create)

**Interfaces:**
- Produces:
  - `plugin.SaleSuggestion{ DiscountType, DiscountValue, Label, Prompt string }`
  - `plugin.ProductSaleSuggestionProvider{ Batch func(ctx context.Context, productIDs []int64) (map[int64]SaleSuggestion, error) }`
  - `func (r *Registry) AddProductSaleSuggestionProvider(p ProductSaleSuggestionProvider)`
  - `func ProductSaleSuggestionProviders() []ProductSaleSuggestionProvider`

- [ ] **Step 1: Write the failing test**

Create `internal/plugin/sale_suggestion_test.go`:

```go
package plugin

import (
	"context"
	"testing"
)

func TestSaleSuggestionProviderRegistration(t *testing.T) {
	before := len(ProductSaleSuggestionProviders())
	r := &Registry{}
	r.AddProductSaleSuggestionProvider(ProductSaleSuggestionProvider{
		Batch: func(_ context.Context, ids []int64) (map[int64]SaleSuggestion, error) {
			return map[int64]SaleSuggestion{ids[0]: {DiscountType: "percent", DiscountValue: "20", Label: "x", Prompt: "y"}}, nil
		},
	})
	if got := len(ProductSaleSuggestionProviders()); got != before+1 {
		t.Fatalf("provider not registered: got %d want %d", got, before+1)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugin/ -run TestSaleSuggestionProviderRegistration`
Expected: FAIL — `SaleSuggestion` / `ProductSaleSuggestionProvider` / `AddProductSaleSuggestionProvider` / `ProductSaleSuggestionProviders` undefined.

- [ ] **Step 3: Add the seam**

In `internal/plugin/hooks.go`, next to `ProductBadgeProvider` add:

```go
// SaleSuggestion is an optional line discount a plugin proposes when a product
// is added at the till (e.g. a clearance markdown). The cashier is prompted to
// apply or skip it; applying sets the line's existing discount fields.
type SaleSuggestion struct {
	DiscountType  string // "percent" | "fixed" (matches sale_items.discount_type)
	DiscountValue string // percent (e.g. "20") or fixed per-unit amount
	Label         string // short badge/summary, e.g. "Clearance -20%"
	Prompt        string // popup body, e.g. "Clearance item — apply 20% off?"
}

// ProductSaleSuggestionProvider supplies a line-discount suggestion per product
// for the till. Batch is called with the visible product ids and returns each
// id's suggestion (omit an id for none). Best-effort: an error drops the
// suggestion, never breaks the payload. One query per page — no per-row calls.
type ProductSaleSuggestionProvider struct {
	Batch func(ctx context.Context, productIDs []int64) (map[int64]SaleSuggestion, error)
}
```

Add the backing slice near the other provider slices (e.g. beside `productBadgeProviders`):

```go
var productSaleSuggesters []ProductSaleSuggestionProvider
```

Add the registrar beside `AddProductBadgeProvider`:

```go
func (r *Registry) AddProductSaleSuggestionProvider(p ProductSaleSuggestionProvider) {
	productSaleSuggesters = append(productSaleSuggesters, p)
}
```

Add the accessor beside `ProductBadgeProviders()`:

```go
func ProductSaleSuggestionProviders() []ProductSaleSuggestionProvider { return productSaleSuggesters }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/plugin/ -run TestSaleSuggestionProviderRegistration`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/plugin/hooks.go internal/plugin/sale_suggestion_test.go
git commit -m "feat(plugin): generic ProductSaleSuggestionProvider seam"
```

---

## Task 2: Core — attach `Suggestion` to till product payload

**Files:**
- Modify: `internal/features/products/products.go`
- Modify: `internal/features/products/api.go`
- Modify: `internal/web/web.go`
- Test: `internal/features/products/suggestion_test.go` (create)

**Interfaces:**
- Consumes: `plugin.SaleSuggestion`, `plugin.ProductSaleSuggestionProviders()` (Task 1).
- Produces:
  - `products.SaleSuggestion{ DiscountType, DiscountValue, Label, Prompt string }` (mirror struct so `products` doesn't import `plugin`).
  - `products.Product.Suggestion *SaleSuggestion` (`json:"suggestion,omitempty"`).
  - `var products.SaleSuggestionProvider func(ctx context.Context, productIDs []int64) map[int64]SaleSuggestion`.

> Note: `products` must NOT import `plugin` (import-cycle rule — same reason `BadgeProvider` is a func-var, not a plugin type). Define a mirror `SaleSuggestion` in `products` and convert in the `web.go` bridge.

- [ ] **Step 1: Write the failing test**

Create `internal/features/products/suggestion_test.go`:

```go
package products

import (
	"context"
	"testing"
)

func TestApplySuggestionSetsField(t *testing.T) {
	orig := SaleSuggestionProvider
	t.Cleanup(func() { SaleSuggestionProvider = orig })
	SaleSuggestionProvider = func(_ context.Context, ids []int64) map[int64]SaleSuggestion {
		return map[int64]SaleSuggestion{7: {DiscountType: "percent", DiscountValue: "20", Label: "Clearance -20%", Prompt: "Apply?"}}
	}
	rows := []Product{{ID: 7}, {ID: 9}}
	applySuggestions(context.Background(), rows)
	if rows[0].Suggestion == nil || rows[0].Suggestion.DiscountValue != "20" {
		t.Fatalf("id 7 should have suggestion, got %+v", rows[0].Suggestion)
	}
	if rows[1].Suggestion != nil {
		t.Fatalf("id 9 should have no suggestion")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/features/products/ -run TestApplySuggestionSetsField`
Expected: FAIL — `SaleSuggestion`, `SaleSuggestionProvider`, `applySuggestions`, `Product.Suggestion` undefined.

- [ ] **Step 3: Add the field, func-var, and helper**

In `internal/features/products/products.go`, add after the `Badges` field in `Product`:

```go
	// Plugin-contributed, not persisted: an optional till line-discount suggestion
	// (e.g. a clearance markdown) the cashier is prompted to apply.
	Suggestion *SaleSuggestion `db:"-" json:"suggestion,omitempty"`
```

Add the mirror struct and func-var near `var BadgeProvider ...`:

```go
// SaleSuggestion mirrors plugin.SaleSuggestion (products must not import plugin;
// the web layer converts and wires SaleSuggestionProvider from plugin hooks).
type SaleSuggestion struct {
	DiscountType  string `json:"discount_type"`
	DiscountValue string `json:"discount_value"`
	Label         string `json:"label"`
	Prompt        string `json:"prompt"`
}

// SaleSuggestionProvider returns a per-product till discount suggestion (set by
// the web layer from plugin providers). nil = none.
var SaleSuggestionProvider func(ctx context.Context, productIDs []int64) map[int64]SaleSuggestion

// applySuggestions stamps each row's Suggestion from the provider (no-op when
// unset — a core-only build). Best-effort: skips silently on nil map.
func applySuggestions(ctx context.Context, rows []Product) {
	if SaleSuggestionProvider == nil || len(rows) == 0 {
		return
	}
	ids := make([]int64, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	m := SaleSuggestionProvider(ctx, ids)
	if m == nil {
		return
	}
	for i := range rows {
		if s, ok := m[rows[i].ID]; ok {
			sc := s
			rows[i].Suggestion = &sc
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/features/products/ -run TestApplySuggestionSetsField`
Expected: PASS

- [ ] **Step 5: Call `applySuggestions` in the till payload paths**

In `internal/features/products/api.go`:

In `List`, right after the existing `BadgeProvider` block (after the `}` that closes it, before `return response.Paged(...)`):

```go
	applySuggestions(ctx, rows)
```

In `Get`, after the existing `BadgeProvider` block (before `return response.OK(c, p)`):

```go
	if p != nil {
		one := []Product{*p}
		applySuggestions(c.Request().Context(), one)
		p.Suggestion = one[0].Suggestion
	}
```

In `QuickPicks`, after `picks, err := ...` and its error check, before `return`:

```go
	applySuggestions(c.Request().Context(), picks)
```

- [ ] **Step 6: Add the `web.go` bridge**

In `internal/web/web.go`, right after the badge-provider bridge block (`// Same bridge for till-card badge providers ...`), add:

```go
	// Same bridge for till sale-suggestion providers → products.SaleSuggestionProvider.
	if sps := plugin.ProductSaleSuggestionProviders(); len(sps) > 0 {
		products.SaleSuggestionProvider = func(ctx context.Context, ids []int64) map[int64]products.SaleSuggestion {
			out := map[int64]products.SaleSuggestion{}
			for _, sp := range sps {
				if sp.Batch == nil {
					continue
				}
				m, err := sp.Batch(ctx, ids)
				if err != nil {
					continue
				}
				for id, s := range m {
					if _, taken := out[id]; taken {
						continue // first provider wins; in practice only one registers
					}
					out[id] = products.SaleSuggestion{
						DiscountType: s.DiscountType, DiscountValue: s.DiscountValue,
						Label: s.Label, Prompt: s.Prompt,
					}
				}
			}
			return out
		}
	}
```

- [ ] **Step 7: Build + vet + test**

Run: `go build ./... && go vet ./internal/features/products/ ./internal/web/ && go test ./internal/features/products/ -run TestApplySuggestionSetsField`
Expected: builds clean; PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/features/products/products.go internal/features/products/api.go internal/features/products/suggestion_test.go internal/web/web.go
git commit -m "feat(products): attach optional plugin sale suggestion to till payload"
```

---

## Task 3: Till — prompt & apply the suggestion in `addToCart`

**Files:**
- Modify: `static/js/app.js`
- Modify: `templates/pages/cashier/pos.templ`

**Interfaces:**
- Consumes: `product.suggestion` = `{ discount_type, discount_value, label, prompt }` on the till product payload (Task 2).
- The cart line already carries `discount` (string) and `discountType` (`"fixed"|"percent"`); this sets them.
- Follows the `oversellPrompt` state-object + modal pattern (no promise-confirm in this codebase).

- [ ] **Step 1: Add the `suggestionPrompt` state + apply/skip methods**

In `static/js/app.js`, next to `oversellPrompt: null,` (~line 1996) add:

```js
    suggestionPrompt: null,
    applySuggestion() {
      const sp = this.suggestionPrompt;
      if (!sp) return;
      const line = this.cart.find((x) => x._key === sp.key);
      if (line) {
        line.discount = String(sp.value || 0);
        line.discountType = sp.type === "percent" ? "percent" : "fixed";
      }
      this.suggestionPrompt = null;
    },
    skipSuggestion() {
      this.suggestionPrompt = null;
    },
```

- [ ] **Step 2: Raise the prompt when a new line with a suggestion is created**

In `addToCart`, find the tail:

```js
      this.syncSerials(this.cart[this.cart.length - 1]);
    },
```

Replace with:

```js
      const line = this.cart[this.cart.length - 1];
      this.syncSerials(line);
      // Clearance / promo suggestion: offer the plugin-proposed markdown once,
      // when the product carries one and this caller can answer it.
      if (p.suggestion && !(opts && opts.noPrompt)) {
        const s = p.suggestion;
        const unit = this.unitPriceFor(p);
        const off =
          s.discount_type === "percent"
            ? unit * (1 - (Number(s.discount_value) || 0) / 100)
            : unit - (Number(s.discount_value) || 0);
        this.suggestionPrompt = {
          key: line._key,
          name: p.name,
          prompt: s.prompt || "Apply the suggested discount?",
          type: s.discount_type,
          value: s.discount_value,
          oldPrice: unit,
          newPrice: Math.max(0, off),
        };
      }
    },
```

- [ ] **Step 3: Add the modal to `pos.templ`**

In `templates/pages/cashier/pos.templ`, immediately AFTER the `oversellPrompt` modal's closing `</div>` (the one at ~line 936, before the template's final `</div>` and closing brace), add a sibling modal:

```html
			<div x-show="suggestionPrompt" x-cloak x-on:keydown.escape.window="skipSuggestion()" class="fixed inset-0 bg-black/40 flex items-center justify-center z-50 p-4">
				<div class="bg-white rounded-2xl shadow-xl w-full max-w-sm p-6 space-y-3">
					<h3 class="text-lg font-semibold text-indigo-700">Clearance markdown</h3>
					<p class="text-sm text-slate-600">
						<span class="font-medium" x-text="suggestionPrompt && suggestionPrompt.name"></span> —
						<span x-text="suggestionPrompt && suggestionPrompt.prompt"></span>
					</p>
					<p class="text-sm">
						<span class="line-through text-slate-400" x-text="suggestionPrompt && (sym + ' ' + money(suggestionPrompt.oldPrice))"></span>
						<span class="mx-1">→</span>
						<span class="font-semibold text-emerald-600" x-text="suggestionPrompt && (sym + ' ' + money(suggestionPrompt.newPrice))"></span>
					</p>
					<div class="flex gap-2 pt-1">
						<button type="button" x-on:click="skipSuggestion()" class="flex-1 px-4 py-2.5 rounded-lg border font-semibold">Full price</button>
						<button type="button" x-on:click="applySuggestion()" class="flex-1 px-4 py-2.5 rounded-lg bg-indigo-600 text-white font-semibold">Apply discount</button>
					</div>
				</div>
			</div>
```

> Note: verify `sym` and `money(...)` are in scope in this Alpine component (the oversell modal and cart rows use them — they are). Match the exact nesting depth of the `oversellPrompt` block you're inserting beside.

- [ ] **Step 4: Regenerate templ + verify JS parses**

Run: `templ generate templates/pages/cashier && node --check static/js/app.js`
Expected: templ regenerates; JS valid (no output).

- [ ] **Step 5: Manual verification note (record in commit body)**

No JS harness. After the plugin exists (Task 7): add a clearance product → modal shows old→new price → "Apply discount" sets the line discount (struck price in cart) → "Full price" leaves it; re-adding the same product bumps qty without re-prompting (existing-line path returns before the push).

- [ ] **Step 6: Commit**

```bash
git add static/js/app.js templates/pages/cashier/pos.templ
git commit -m "feat(till): prompt to apply a plugin sale suggestion on add-to-cart"
```

---

## Task 4: Plugin scaffold — package, migrations, manifest

**Files:**
- Create: `plugins/clearance/clearance.go`
- Create: `plugins/clearance/migrations/0001_clearance.sql`
- Create: `plugins/clearance/migrations/embed.go`
- Create: `plugins/clearance/plugin.json`
- Modify (dev only, do NOT commit): `cmd/server/enabled_plugins.go`

**Interfaces:**
- Produces: a registered `Plugin` with `Name() "Clearance"`, `Migrations() (fs.FS, "clearance")`, and an empty-for-now `Setup`.

- [ ] **Step 1: Create the migration**

`plugins/clearance/migrations/0001_clearance.sql`:

```sql
-- +goose Up
CREATE TABLE clearance_markdowns (
    product_id     BIGINT PRIMARY KEY,
    discount_type  TEXT NOT NULL DEFAULT 'percent',  -- percent | fixed
    discount_value NUMERIC NOT NULL DEFAULT 0,
    status         TEXT NOT NULL DEFAULT 'approved',  -- approved | dismissed
    approved_by    BIGINT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE clearance_settings (
    id                 INTEGER PRIMARY KEY DEFAULT 1,
    stale_days         INTEGER NOT NULL DEFAULT 60,
    default_percent    NUMERIC NOT NULL DEFAULT 20,
    min_margin_percent NUMERIC NOT NULL DEFAULT 5,
    CONSTRAINT clearance_settings_singleton CHECK (id = 1)
);
INSERT INTO clearance_settings (id) VALUES (1);

-- +goose Down
DROP TABLE clearance_settings;
DROP TABLE clearance_markdowns;
```

- [ ] **Step 2: Create the migration embed**

`plugins/clearance/migrations/embed.go` (copy the shape of `plugins/alternatives/migrations/embed.go` — read that file first for the exact package name and pattern):

```go
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
```

- [ ] **Step 3: Create the plugin manifest**

`plugins/clearance/plugin.json`:

```json
{
  "key": "clearance",
  "name": "Clearance",
  "import": "karots-pos/plugins/clearance",
  "version": "1.0.0",
  "description": "Flags stale stock (has stock, no sale in N days), suggests a margin-safe markdown the owner approves, and offers to apply it at the till."
}
```

- [ ] **Step 4: Create the plugin skeleton**

`plugins/clearance/clearance.go`:

```go
// Package clearance flags slow-moving stock (has stock but no sale in N days),
// suggests a margin-safe markdown the owner approves or adjusts, and offers to
// apply that discount at the till. Core never imports it; it attaches through
// generic plugin hooks. Depends on no other plugin.
package clearance

import (
	"io/fs"

	"karots-pos/internal/plugin"
	"karots-pos/plugins/clearance/migrations"
)

func init() { plugin.Register(&Plugin{}) }

type Plugin struct {
	core  plugin.Core
	store *Store
}

func (p *Plugin) Name() string                { return "Clearance" }
func (p *Plugin) Migrations() (fs.FS, string) { return migrations.FS, "clearance" }

func (p *Plugin) Setup(reg *plugin.Registry) {
	p.core = reg.Core
	p.store = NewStore(reg.Core.DB)
	// routes + hooks wired in Task 6 and Task 7.
}
```

- [ ] **Step 5: Enable for dev (do not commit this file)**

In `cmd/server/enabled_plugins.go` add to the import block:

```go
	_ "karots-pos/plugins/clearance"
```

- [ ] **Step 6: Build + apply migration**

Run: `go build ./...`
Then build and run the server once so the plugin migration applies (see the repo's dev run pattern), and verify:

```bash
docker exec pos_db psql -U pos_user -d pos_db -c "\dt clearance_*"
```
Expected: `clearance_markdowns` and `clearance_settings` exist; `clearance_settings` has one row.

- [ ] **Step 7: Commit (plugin files only — NOT enabled_plugins.go)**

```bash
git add plugins/clearance/clearance.go plugins/clearance/migrations plugins/clearance/plugin.json
git commit -m "feat(clearance): plugin scaffold + migrations + manifest"
```

---

## Task 5: Plugin store — staleness, markdown clamp, settings, CRUD

**Files:**
- Create: `plugins/clearance/store.go`
- Test: `plugins/clearance/store_test.go`

**Interfaces:**
- Consumes: `*sqlx.DB` via `NewStore`.
- Produces (used by Tasks 6 & 7):
  - `NewStore(db *sqlx.DB) *Store`
  - `type Settings struct { StaleDays int; DefaultPercent, MinMarginPercent decimal.Decimal }`
  - `(*Store).GetSettings(ctx) (Settings, error)` / `SaveSettings(ctx, Settings) error`
  - `type StaleItem struct { ProductID int64; Name, Unit string; OnHand, Cost, Price decimal.Decimal; DaysSinceSale *int; Status string; MarkdownType string; MarkdownValue decimal.Decimal }`
  - `(*Store).StaleItems(ctx) ([]StaleItem, error)` — candidates + already-approved, excludes dismissed
  - `(*Store).Approve(ctx, productID int64, dtype string, value decimal.Decimal, userID int64) error`
  - `(*Store).Dismiss(ctx, productID, userID int64) error`
  - `(*Store).BadgesFor(ctx, ids []int64) (map[int64][]string, error)`
  - `(*Store).SuggestionsFor(ctx, ids []int64) (map[int64]plugin.SaleSuggestion, error)`
  - `suggestPercent(sell, cost, defaultPct, minMarginPct decimal.Decimal) decimal.Decimal` — pure, floored
  - `newPrice(sell, pct decimal.Decimal) decimal.Decimal` — pure

- [ ] **Step 1: Write the failing test for the clamp math**

Create `plugins/clearance/store_test.go`:

```go
package clearance

import (
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { d, _ := decimal.NewFromString(s); return d }

func TestSuggestPercentFlooredAtCost(t *testing.T) {
	cases := []struct {
		name                            string
		sell, cost, deflt, minMargin, want string
	}{
		// 20% off 120 = 96, but floor = cost*1.05 = 105 → clamp to 12.5%.
		{"clamped to floor", "120", "100", "20", "5", "12.5"},
		// 20% off 200 = 160 >= floor 105 → full 20%.
		{"unclamped", "200", "100", "20", "5", "20"},
		// price already at/under floor → nothing safe to give.
		{"already cheap", "100", "100", "20", "5", "0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := suggestPercent(dec(c.sell), dec(c.cost), dec(c.deflt), dec(c.minMargin))
			if !got.Equal(dec(c.want)) {
				t.Errorf("suggestPercent = %s, want %s", got, c.want)
			}
		})
	}
}

func TestNewPrice(t *testing.T) {
	if got := newPrice(dec("120"), dec("12.5")); !got.Equal(dec("105")) {
		t.Errorf("newPrice = %s, want 105", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./plugins/clearance/ -run 'TestSuggestPercent|TestNewPrice'`
Expected: FAIL — `suggestPercent` / `newPrice` undefined.

- [ ] **Step 3: Implement the store with the pure helpers**

Create `plugins/clearance/store.go`:

```go
package clearance

import (
	"context"

	"karots-pos/internal/plugin"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

type Store struct{ db *sqlx.DB }

func NewStore(db *sqlx.DB) *Store { return &Store{db: db} }

type Settings struct {
	StaleDays        int             `db:"stale_days"`
	DefaultPercent   decimal.Decimal `db:"default_percent"`
	MinMarginPercent decimal.Decimal `db:"min_margin_percent"`
}

func (s *Store) GetSettings(ctx context.Context) (Settings, error) {
	var out Settings
	err := s.db.GetContext(ctx, &out,
		`SELECT stale_days, default_percent, min_margin_percent FROM clearance_settings WHERE id = 1`)
	return out, err
}

func (s *Store) SaveSettings(ctx context.Context, in Settings) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE clearance_settings SET stale_days=$1, default_percent=$2, min_margin_percent=$3 WHERE id = 1`,
		in.StaleDays, in.DefaultPercent, in.MinMarginPercent)
	return err
}

// suggestPercent returns the discount % to suggest: defaultPct, but never so
// much that the new price falls below cost*(1+minMargin/100). Returns 0 when the
// price is already at or under that floor (nothing safe to give).
func suggestPercent(sell, cost, defaultPct, minMarginPct decimal.Decimal) decimal.Decimal {
	hundred := decimal.NewFromInt(100)
	if !sell.IsPositive() {
		return decimal.Zero
	}
	floor := cost.Mul(hundred.Add(minMarginPct)).Div(hundred) // cost*(1+m/100)
	// max % that still keeps price >= floor: (1 - floor/sell) * 100
	maxPct := hundred.Sub(floor.Div(sell).Mul(hundred))
	if maxPct.IsNegative() {
		maxPct = decimal.Zero
	}
	if defaultPct.LessThan(maxPct) {
		return defaultPct
	}
	return maxPct
}

// newPrice applies a percent off a selling price, rounded to 2dp.
func newPrice(sell, pct decimal.Decimal) decimal.Decimal {
	hundred := decimal.NewFromInt(100)
	return sell.Mul(hundred.Sub(pct)).Div(hundred).Round(2)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./plugins/clearance/ -run 'TestSuggestPercent|TestNewPrice'`
Expected: PASS

- [ ] **Step 5: Add the staleness query, CRUD, and providers**

Append to `plugins/clearance/store.go`:

```go
type StaleItem struct {
	ProductID     int64           `db:"product_id"`
	Name          string          `db:"name"`
	Unit          string          `db:"unit_abbr"`
	OnHand        decimal.Decimal `db:"stock_qty"`
	Cost          decimal.Decimal `db:"cost_price"`
	Price         decimal.Decimal `db:"selling_price"`
	DaysSinceSale *int            `db:"days_since_sale"` // nil = never sold
	Status        *string         `db:"status"`          // approved | dismissed | nil (candidate)
	MarkdownType  *string         `db:"discount_type"`
	MarkdownValue *decimal.Decimal `db:"discount_value"`
}

// StaleItems lists products with stock but no sale within stale_days (or never
// sold), plus any already-approved markdowns, excluding dismissed ones. Services
// and inactive products are excluded.
func (s *Store) StaleItems(ctx context.Context) ([]StaleItem, error) {
	cfg, err := s.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	var rows []StaleItem
	err = s.db.SelectContext(ctx, &rows, `
		SELECT p.id AS product_id, p.name, u.abbreviation AS unit_abbr,
		       COALESCE(st.quantity, 0) AS stock_qty, p.cost_price, p.selling_price,
		       CASE WHEN ls.last_sold IS NULL THEN NULL
		            ELSE EXTRACT(DAY FROM now() - ls.last_sold)::int END AS days_since_sale,
		       m.status, m.discount_type, m.discount_value
		FROM products p
		JOIN units u ON u.id = p.unit_id
		LEFT JOIN stock st ON st.product_id = p.id
		LEFT JOIN (
		    SELECT si.product_id, MAX(sa.created_at) AS last_sold
		    FROM sale_items si JOIN sales sa ON sa.id = si.sale_id
		    GROUP BY si.product_id
		) ls ON ls.product_id = p.id
		LEFT JOIN clearance_markdowns m ON m.product_id = p.id
		WHERE p.is_active = true AND p.is_service = false
		  AND COALESCE(st.quantity, 0) > 0
		  AND (ls.last_sold IS NULL OR ls.last_sold < now() - ($1 || ' days')::interval)
		  AND (m.status IS DISTINCT FROM 'dismissed')
		ORDER BY ls.last_sold ASC NULLS FIRST, p.name`,
		cfg.StaleDays)
	return rows, err
}

func (s *Store) Approve(ctx context.Context, productID int64, dtype string, value decimal.Decimal, userID int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO clearance_markdowns (product_id, discount_type, discount_value, status, approved_by, updated_at)
		VALUES ($1,$2,$3,'approved',$4, now())
		ON CONFLICT (product_id) DO UPDATE
		SET discount_type=$2, discount_value=$3, status='approved', approved_by=$4, updated_at=now()`,
		productID, dtype, value, userID)
	return err
}

func (s *Store) Dismiss(ctx context.Context, productID, userID int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO clearance_markdowns (product_id, discount_type, discount_value, status, approved_by, updated_at)
		VALUES ($1,'percent',0,'dismissed',$2, now())
		ON CONFLICT (product_id) DO UPDATE
		SET status='dismissed', approved_by=$2, updated_at=now()`,
		productID, userID)
	return err
}

// approvedMarkdowns returns approved (type,value) for the given product ids.
func (s *Store) approvedMarkdowns(ctx context.Context, ids []int64) (map[int64]struct {
	Type  string
	Value decimal.Decimal
}, error) {
	out := map[int64]struct {
		Type  string
		Value decimal.Decimal
	}{}
	if len(ids) == 0 {
		return out, nil
	}
	q, args, err := sqlx.In(
		`SELECT product_id, discount_type, discount_value FROM clearance_markdowns
		 WHERE status='approved' AND discount_value > 0 AND product_id IN (?)`, ids)
	if err != nil {
		return nil, err
	}
	q = s.db.Rebind(q)
	rows, err := s.db.QueryxContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var t string
		var v decimal.Decimal
		if err := rows.Scan(&id, &t, &v); err != nil {
			return nil, err
		}
		out[id] = struct {
			Type  string
			Value decimal.Decimal
		}{t, v}
	}
	return out, rows.Err()
}

// BadgesFor pins "Clearance -N%" (or "-Rs N") on approved products' till cards.
func (s *Store) BadgesFor(ctx context.Context, ids []int64) (map[int64][]string, error) {
	m, err := s.approvedMarkdowns(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := map[int64][]string{}
	for id, md := range m {
		if md.Type == "percent" {
			out[id] = []string{"Clearance -" + md.Value.String() + "%"}
		} else {
			out[id] = []string{"Clearance -" + md.Value.String()}
		}
	}
	return out, nil
}

// SuggestionsFor returns the till line-discount suggestion for approved products.
func (s *Store) SuggestionsFor(ctx context.Context, ids []int64) (map[int64]plugin.SaleSuggestion, error) {
	m, err := s.approvedMarkdowns(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := map[int64]plugin.SaleSuggestion{}
	for id, md := range m {
		label := "Clearance -" + md.Value.String()
		if md.Type == "percent" {
			label += "%"
		}
		out[id] = plugin.SaleSuggestion{
			DiscountType:  md.Type,
			DiscountValue: md.Value.String(),
			Label:         label,
			Prompt:        "Clearance item — apply the markdown?",
		}
	}
	return out, nil
}
```

> Note: confirm `units` table has an `abbreviation` column (the core product query joins `units u` and reads `u.abbreviation AS unit_abbr` — verify with `\d units`). If the column name differs, match it.

- [ ] **Step 6: Build + vet + full plugin test**

Run: `go build ./... && go vet ./plugins/clearance/ && go test ./plugins/clearance/`
Expected: builds clean; tests PASS.

- [ ] **Step 7: Commit**

```bash
git add plugins/clearance/store.go plugins/clearance/store_test.go
git commit -m "feat(clearance): store — staleness query, margin-safe clamp, CRUD, providers"
```

---

## Task 6: Plugin admin page — review, approve/adjust/dismiss, settings

**Files:**
- Create: `plugins/clearance/admin.go`
- Create: `plugins/clearance/pages.templ` (+ generated `pages_templ.go`)
- Modify: `plugins/clearance/clearance.go` (routes + nav)

**Interfaces:**
- Consumes: `Store` methods (Task 5), `plugin.Registry.Admin()`, `reg.AddAdminNav`, `middleware.CurrentUserID`.
- Produces: admin routes `/admin/clearance`, `/admin/clearance/:pid/approve`, `/admin/clearance/:pid/dismiss`, `/admin/clearance/settings`.

- [ ] **Step 1: Build the page ViewModel + templ**

`plugins/clearance/pages.templ` (read `plugins/alternatives/pages.templ` first for the exact layout import + admin page shell conventions):

```templ
package clearance

import (
	"strconv"

	"karots-pos/internal/money"
	"karots-pos/templates/layouts"
)

type Row struct {
	ProductID   int64
	Name        string
	Unit        string
	OnHand      string
	Cost        string
	Price       string
	Margin      string // current margin %, e.g. "17%"
	DaysLabel   string // "42 days" or "never sold"
	SuggestPct  string // "12.5"
	NewPrice    string
	NewMargin   string
	Approved    bool
	ApprovedPct string // when approved
}

type PageData struct {
	UserName         string
	Symbol           string
	Rows             []Row
	StaleDays        int
	DefaultPercent   string
	MinMarginPercent string
}

templ Page(d PageData) {
	@layouts.Admin("Clearance", d.UserName, "clearance") {
		<div class="flex items-center justify-between mb-6">
			<h1 class="text-2xl font-bold">Clearance / Stale stock</h1>
		</div>
		<form method="post" action="/admin/clearance/settings" class="bg-white rounded-2xl shadow-sm p-4 mb-6 flex flex-wrap items-end gap-4">
			<div>
				<label class="block text-xs text-slate-500 mb-1">Stale after (days)</label>
				<input type="number" name="stale_days" value={ strconv.Itoa(d.StaleDays) } min="1" class="border rounded-lg px-3 py-1.5 w-28"/>
			</div>
			<div>
				<label class="block text-xs text-slate-500 mb-1">Default discount %</label>
				<input type="number" step="0.5" name="default_percent" value={ d.DefaultPercent } min="0" class="border rounded-lg px-3 py-1.5 w-28"/>
			</div>
			<div>
				<label class="block text-xs text-slate-500 mb-1">Min margin % over cost</label>
				<input type="number" step="0.5" name="min_margin_percent" value={ d.MinMarginPercent } min="0" class="border rounded-lg px-3 py-1.5 w-28"/>
			</div>
			<button class="px-4 py-1.5 rounded-lg bg-indigo-600 text-white text-sm font-medium">Save</button>
		</form>
		<div class="bg-white rounded-2xl shadow-sm p-6">
			if len(d.Rows) == 0 {
				<p class="py-6 text-center text-slate-500">No stale items — everything is selling. 🎉</p>
			} else {
				<table class="w-full text-sm">
					<thead class="text-left text-slate-500 border-b">
						<tr>
							<th class="py-2 px-2">Product</th>
							<th class="py-2 px-2 text-right">On hand</th>
							<th class="py-2 px-2 text-right">Last sold</th>
							<th class="py-2 px-2 text-right">Cost</th>
							<th class="py-2 px-2 text-right">Price</th>
							<th class="py-2 px-2 text-right">Suggest %</th>
							<th class="py-2 px-2 text-right">New price</th>
							<th class="py-2 px-2"></th>
						</tr>
					</thead>
					<tbody>
						for _, r := range d.Rows {
							<tr class="border-b last:border-0">
								<td class="py-2 px-2 font-medium">
									{ r.Name }
									if r.Approved {
										<span class="ml-2 text-xs text-emerald-600">approved -{ r.ApprovedPct }%</span>
									}
								</td>
								<td class="py-2 px-2 text-right">{ r.OnHand } <span class="text-slate-400">{ r.Unit }</span></td>
								<td class="py-2 px-2 text-right text-slate-500">{ r.DaysLabel }</td>
								<td class="py-2 px-2 text-right">{ money.Format(d.Symbol, mustDec(r.Cost)) }</td>
								<td class="py-2 px-2 text-right">{ money.Format(d.Symbol, mustDec(r.Price)) }</td>
								<td class="py-2 px-2 text-right">
									<form method="post" action={ templ.SafeURL("/admin/clearance/" + strconv.FormatInt(r.ProductID, 10) + "/approve") } class="flex items-center justify-end gap-1">
										<input type="number" step="0.5" name="percent" value={ r.SuggestPct } min="0" max="100" class="border rounded px-2 py-1 w-20 text-right"/>%
								</td>
								<td class="py-2 px-2 text-right">{ money.Format(d.Symbol, mustDec(r.NewPrice)) }</td>
								<td class="py-2 px-2 text-right whitespace-nowrap">
										<button class="px-3 py-1 rounded-lg bg-emerald-600 text-white text-xs font-medium">Approve</button>
									</form>
									<form method="post" action={ templ.SafeURL("/admin/clearance/" + strconv.FormatInt(r.ProductID, 10) + "/dismiss") } class="inline">
										<button class="px-3 py-1 rounded-lg border text-slate-600 text-xs font-medium">Dismiss</button>
									</form>
								</td>
							</tr>
						}
					</tbody>
				</table>
			}
		</div>
	}
}
```

> Note: `mustDec` is a tiny helper the admin.go builds rows with pre-formatted strings; simpler is to pass already-formatted money strings in `Row` and print them directly. If `money.Format` needs a `decimal.Decimal`, format in `admin.go` and change the `Row` money fields to already-formatted strings printed with `{ r.Cost }` (no `money.Format` in the templ). Choose the already-formatted-string approach to avoid a templ helper; drop `mustDec` and the `money` import.

- [ ] **Step 2: Rewrite the templ money cells to print pre-formatted strings**

Adjust the templ so `Cost/Price/NewPrice` are printed directly (`{ r.Cost }`), remove the `money` import and `mustDec`. Formatting happens in `admin.go` (Step 3).

- [ ] **Step 3: Write the admin handlers**

`plugins/clearance/admin.go`:

```go
package clearance

import (
	"net/http"
	"strconv"

	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"

	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

type adminUI struct{ p *Plugin }

func (a *adminUI) Page(c echo.Context) error {
	ctx := c.Request().Context()
	cfg, err := a.p.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	items, err := a.p.store.StaleItems(ctx)
	if err != nil {
		return err
	}
	symbol := "Rs."
	if sc, serr := a.p.core.Settings.Get(ctx); serr == nil && sc != nil {
		symbol = sc.CurrencySymbol
	}
	rows := make([]Row, 0, len(items))
	for _, it := range items {
		pct := suggestPercent(it.Price, it.Cost, cfg.DefaultPercent, cfg.MinMarginPercent)
		np := newPrice(it.Price, pct)
		days := "never sold"
		if it.DaysSinceSale != nil {
			days = strconv.Itoa(*it.DaysSinceSale) + " days"
		}
		approved := it.Status != nil && *it.Status == "approved"
		apct := ""
		if approved && it.MarkdownValue != nil {
			apct = it.MarkdownValue.String()
			pct = *it.MarkdownValue // show the approved value in the box
			np = newPrice(it.Price, pct)
		}
		rows = append(rows, Row{
			ProductID:   it.ProductID,
			Name:        it.Name,
			Unit:        it.Unit,
			OnHand:      it.OnHand.String(),
			Cost:        money.Format(symbol, it.Cost),
			Price:       money.Format(symbol, it.Price),
			DaysLabel:   days,
			SuggestPct:  pct.String(),
			NewPrice:    money.Format(symbol, np),
			Approved:    approved,
			ApprovedPct: apct,
		})
	}
	return response.RenderPage(c, Page(PageData{
		UserName:         middleware.CurrentUserName(c),
		Symbol:           symbol,
		Rows:             rows,
		StaleDays:        cfg.StaleDays,
		DefaultPercent:   cfg.DefaultPercent.String(),
		MinMarginPercent: cfg.MinMarginPercent.String(),
	}))
}

func (a *adminUI) Approve(c echo.Context) error {
	pid, err := strconv.ParseInt(c.Param("pid"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	pct, err := decimal.NewFromString(c.FormValue("percent"))
	if err != nil || pct.IsNegative() {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid percent")
	}
	if err := a.p.store.Approve(c.Request().Context(), pid, "percent", pct, middleware.CurrentUserID(c)); err != nil {
		return err
	}
	return c.Redirect(http.StatusSeeOther, "/admin/clearance")
}

func (a *adminUI) Dismiss(c echo.Context) error {
	pid, err := strconv.ParseInt(c.Param("pid"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	if err := a.p.store.Dismiss(c.Request().Context(), pid, middleware.CurrentUserID(c)); err != nil {
		return err
	}
	return c.Redirect(http.StatusSeeOther, "/admin/clearance")
}

func (a *adminUI) SaveSettings(c echo.Context) error {
	days, _ := strconv.Atoi(c.FormValue("stale_days"))
	if days < 1 {
		days = 60
	}
	dp, err := decimal.NewFromString(c.FormValue("default_percent"))
	if err != nil {
		dp = decimal.NewFromInt(20)
	}
	mm, err := decimal.NewFromString(c.FormValue("min_margin_percent"))
	if err != nil {
		mm = decimal.NewFromInt(5)
	}
	if err := a.p.store.SaveSettings(c.Request().Context(), Settings{StaleDays: days, DefaultPercent: dp, MinMarginPercent: mm}); err != nil {
		return err
	}
	return c.Redirect(http.StatusSeeOther, "/admin/clearance")
}
```

> Note: the page render call must match how other plugins render a full admin page. Read `plugins/alternatives/admin.go`'s `Page` handler and copy its exact render mechanism (it likely uses `response.RenderPage` or a templ `.Render(ctx, w)` pattern, not `c.Render` with a name). Match it. Likewise use the real currency-symbol accessor the alternatives plugin uses (`a.p.core.Settings.Get(ctx)` → `CurrencySymbol`), not `Cfg.CurrencySymbol`, if that's the established pattern.

- [ ] **Step 4: Wire routes + nav in `Setup`**

In `plugins/clearance/clearance.go`, replace the `// routes + hooks wired ...` line in `Setup` with:

```go
	a := &adminUI{p: p}
	reg.Admin().GET("/clearance", a.Page)
	reg.Admin().POST("/clearance/settings", a.SaveSettings)
	reg.Admin().POST("/clearance/:pid/approve", a.Approve)
	reg.Admin().POST("/clearance/:pid/dismiss", a.Dismiss)

	reg.AddAdminNav(plugin.AdminNavEntry{
		SectionLabel: "Clearance", Icon: "🏷️",
		Href: "/admin/clearance", Label: "Stale stock", Key: "clearance",
		Desc: "Markdowns for slow-moving stock",
	})
```

- [ ] **Step 5: Regenerate templ + build + vet**

Run: `templ generate plugins/clearance && go build ./... && go vet ./plugins/clearance/`
Expected: builds clean.

- [ ] **Step 6: Manual verification**

Run the dev server; open `/admin/clearance`. Expected: settings bar + a table of stale items with suggested % and new price; Approve stores a markdown (row shows "approved -N%"); Dismiss removes it from the list; Save settings changes the stale window and re-filters.

- [ ] **Step 7: Commit**

```bash
templ generate plugins/clearance
git add plugins/clearance/admin.go plugins/clearance/pages.templ plugins/clearance/clearance.go
git commit -m "feat(clearance): admin review page — approve/adjust/dismiss + settings"
```
(pages_templ.go is gitignored — do not add it.)

---

## Task 7: Wire the till hooks (badge, suggestion, detail) + end-to-end

**Files:**
- Modify: `plugins/clearance/clearance.go` (register the three providers)

**Interfaces:**
- Consumes: `Store.BadgesFor`, `Store.SuggestionsFor` (Task 5); `plugin.AddProductBadgeProvider`, `plugin.AddProductSaleSuggestionProvider`, `plugin.AddProductDetailContributor` (Tasks 1 + existing).

- [ ] **Step 1: Register the providers in `Setup`**

In `plugins/clearance/clearance.go` `Setup`, after the nav entry, add:

```go
	// Till-card pin + the add-to-cart markdown suggestion + info-popup row.
	reg.AddProductBadgeProvider(plugin.ProductBadgeProvider{Batch: p.store.BadgesFor})
	reg.AddProductSaleSuggestionProvider(plugin.ProductSaleSuggestionProvider{Batch: p.store.SuggestionsFor})
	reg.AddProductDetailContributor(plugin.ProductDetailContributor{
		Rows: func(ctx context.Context, id int64) ([]plugin.DetailRow, error) {
			m, err := p.store.SuggestionsFor(ctx, []int64{id})
			if err != nil || len(m) == 0 {
				return nil, err
			}
			return []plugin.DetailRow{{Label: "Clearance", Value: m[id].Label}}, nil
		},
	})
```

Add `"context"` to the imports if not present.

- [ ] **Step 2: Build + vet + all plugin tests**

Run: `go build ./... && go vet ./plugins/clearance/ && go test ./plugins/clearance/ ./internal/plugin/ ./internal/features/products/`
Expected: builds clean; all PASS.

- [ ] **Step 3: End-to-end manual verification**

Dev server running with the clearance import. In `/admin/clearance`, Approve a markdown on a product. Then at the till:
- the product card shows a "Clearance -N%" badge;
- adding it pops the apply/skip prompt with old→new price;
- Apply sets the line discount (struck price in the cart); the sale completes and the receipt shows the discounted line;
- the ⓘ info popup shows a "Clearance" row.

- [ ] **Step 4: Core-only inertness check**

Temporarily comment the `_ "karots-pos/plugins/clearance"` dev import, `go build ./cmd/server`, run: the till payload has no `suggestion`, no clearance nav, no badge — core unaffected. Restore the dev import.

- [ ] **Step 5: Commit**

```bash
git add plugins/clearance/clearance.go
git commit -m "feat(clearance): wire till badge, markdown suggestion, and info-popup row"
```

---

## Self-Review Notes

- **Spec coverage:** detection (Task 5 `StaleItems`), admin review/approve/adjust/dismiss (Task 6), settings (Tasks 4–6), suggested markdown floored at cost (Task 5 `suggestPercent`, tested), till badge + apply/skip popup + info row (Tasks 3, 7), new generic seam (Tasks 1–2), bootstrapper via `plugin.json` (Task 4), core-only inertness (Task 7 Step 4). All covered.
- **Out of scope (spec):** auto-expiry, clearance report, peer-relative detection — no tasks, intentionally.
- **Type consistency:** `SaleSuggestion` fields (`DiscountType/DiscountValue/Label/Prompt`) identical in `plugin` (Task 1) and `products` mirror (Task 2); `discountType` values `"percent"|"fixed"` consistent across store, JS, and DB; `suggestPercent`/`newPrice` signatures match between Task 5 def and Task 6 use.
- **Verify-before-code flags** (noted inline): confirm the alternatives plugin's exact page-render call and currency-symbol accessor (Task 6 Step 3), that `units.abbreviation` is the real column (Task 5 Step 5), that `app.js` has a promise-returning `confirm` helper (Task 3 Step 1), and whether `pages_templ.go` is committed for existing plugins (Global Constraints).
```
