# Clearance — stale-stock markdowns (plugin)

**Date:** 2026-09-03
**Status:** Approved design, pre-implementation
**Type:** New compile-time plugin + one new generic core seam

## Problem

Slow-moving stock sits on the shelf tying up cash. The owner wants the system
to surface products that haven't sold in a while, suggest a safe markdown
(using the cost and selling price it already knows), let the admin approve or
adjust it, and then — when a cashier rings up that item — offer to apply the
markdown at the till. Not every shop wants this behaviour, so it must be
optional and decoupled.

## Decision: plugin, not core

The discount machinery is **already core and per-line**: `sale_items` carries
`discount / discount_type (fixed|percent) / discount_value`, and the till cart
(`app.js`) already computes and displays per-line discounts. Sales velocity is
already computed (the reorder report reads "sold last week/month/year").
Display seams already exist (`ProductBadgeProvider`, `ProductDetailContributor`).

So this feature adds **no new discount or pricing machinery to core**. It lives
as a plugin (`Clearance`) that:
- detects staleness,
- runs an admin review/approve page,
- stores approved markdowns,
- and, through generic hooks, badges the till card and suggests the discount
  when the product is added to the cart.

It depends on no other plugin, is inert when not installed, and its discounts
flow through core sales and P&L unchanged.

## The one new core seam

Everything reuses existing seams **except** the till-time suggestion. Today a
plugin can attach *badges* to a product's till payload (`products.BadgeProvider`
func-var, bridged from `plugin.ProductBadgeProviders()` in `web.go`), but there
is no way to attach a *suggested line discount + prompt*.

Add a sibling seam following the exact same pattern:

- **`plugin.ProductSaleSuggestionProvider`** (new hook type in
  `internal/plugin/hooks.go`):
  ```go
  // SaleSuggestion is an optional line-discount a plugin proposes when a product
  // is added at the till (e.g. a clearance markdown). The cashier is prompted to
  // apply or skip it; applying sets the line's existing discount fields.
  type SaleSuggestion struct {
      DiscountType  string // "percent" | "fixed" (matches sale_items.discount_type)
      DiscountValue string // percent (e.g. "20") or fixed per-unit amount
      Label         string // short badge/summary, e.g. "Clearance -20%"
      Prompt        string // popup body, e.g. "Clearance item — apply 20% off?"
  }
  type ProductSaleSuggestionProvider struct {
      Batch func(ctx context.Context, productIDs []int64) (map[int64]SaleSuggestion, error)
  }
  ```
  Registered via `reg.AddProductSaleSuggestionProvider(...)`, read via
  `plugin.ProductSaleSuggestionProviders()`. Batch, one query per page — same
  contract as `ProductBadgeProvider`.

- **`products.SaleSuggestionProvider`** func-var (new, in `products` package),
  bridged in `web.go` from the plugin hook — a copy of the existing
  `BadgeProvider` bridge (fan-out; first non-empty suggestion per id wins, since
  in practice only Clearance registers one).

- The till product payload (`Product` struct) gains an optional
  `Suggestion *SaleSuggestion` field (`json:"suggestion,omitempty"`), populated
  in the same API paths that already populate `Badges`: `List`, `Get`, the
  barcode/scan lookup, and `QuickPicks`. Absent → field omitted → no popup.

This seam is intentionally generic: a future promo / happy-hour / bundle plugin
can reuse it. Core owns *applying* discounts (unchanged); the plugin only
*proposes* one.

## Plugin structure (`plugins/clearance/`)

Mirrors `plugins/alternatives/`: `clearance.go` (register + `Setup`),
`store.go` (+ `store_test.go`), `admin.go`, `pages.templ`, `migrations/`,
`plugin.json`.

### Data (plugin migrations, table prefix `clearance_`)

- **`clearance_markdowns`** — one row per product the admin has acted on:
  | column | type | note |
  |---|---|---|
  | `product_id` | bigint PK | one markdown per product |
  | `discount_type` | text | `percent` \| `fixed` |
  | `discount_value` | numeric | the approved value |
  | `status` | text | `approved` \| `dismissed` |
  | `approved_by` | bigint | user id |
  | `created_at` / `updated_at` | timestamptz | |

  `dismissed` is stored (not deleted) so a dismissed item doesn't keep
  reappearing every time the admin opens the page. Staleness itself is computed
  live — never stored.

- **`clearance_settings`** — config the admin can change. No existing plugin
  stores settings, so this plugin owns a tiny **single-row** table (one row,
  `id = 1`) with columns `stale_days` (default 60), `default_percent`
  (default 20), `min_margin_percent` (default 5, the floor over cost). A single
  row (not key/value) keeps the store read a plain typed `SELECT`.

### Detection (live, on page load — no cron)

One SQL query in `store.go`: products with `stock_qty > 0` and **no
`sale_items` row (via `sales`) in the last `stale_days` days**, excluding
services and inactive products, excluding rows already `dismissed`. Returns the
product with cost, selling price, on-hand, unit, and days-since-last-sale
(`MAX(sales.created_at)` per product, or "never"). Ordered by staleness
(oldest/never first).

> ponytail: computed on demand when the admin opens the page — no background
> job, no denormalised "last sold" column. If the query is ever too slow on a
> large catalog, the upgrade path is a cached `last_sold_at` per product, not a
> cron.

### Suggested markdown (floored at cost)

For each stale item, suggest `default_percent` off the selling price, but
**clamp the resulting price to `cost × (1 + min_margin_percent/100)`** — never
suggest selling at or below cost. If the current price is already at/under that
floor, suggest 0% (nothing safe to give). The page shows: current price,
suggested %, resulting price, and resulting margin, so the admin sees the
trade-off. Admin can **Adjust** to any % or absolute new price (the same floor
warning shows, but the admin may override).

### Admin review page — "Clearance"

- Route group under `reg.Admin()`: `/admin/clearance` (list),
  `POST /admin/clearance/:pid/approve`, `POST /admin/clearance/:pid/dismiss`,
  `POST /admin/clearance/:pid/adjust`, and a settings save.
- `reg.AddAdminNav(...)` entry (section "Clearance", under Inventory-ish),
  mirroring the alternatives nav entry.
- Table columns: Product · On hand · Days since last sale · Cost · Price ·
  Margin · Suggested % · New price · New margin · actions (Approve / Adjust /
  Dismiss). Approved rows visibly marked; a filter/toggle to show approved vs
  candidates.
- Settings block: stale window (days), default %, min margin %.

### Till behaviour

- **Badge**: `reg.AddProductBadgeProvider` → `store.BadgesFor` returns
  "Clearance -20%" for approved products (same as alternatives' tier pin).
- **Suggestion**: `reg.AddProductSaleSuggestionProvider` →
  `store.SuggestionsFor` returns the `SaleSuggestion` for approved products.
- **Info popup**: `reg.AddProductDetailContributor` → a "Clearance" row.
- **`app.js` change** (core, small): in `addToCart`, if the product payload
  carries `suggestion`, show a themed confirm popup
  (*"Clearance item — apply 20% off (Rs 120 → Rs 96)? [Apply] [Skip]"*). Apply
  sets that cart line's `discount` / `discountType` fields (the existing
  per-line discount the cart already renders and posts); Skip leaves it full
  price. Re-adding the same line does not re-prompt (guard by a per-line flag).

## Reporting / money

No special handling: a clearance sale is an ordinary sale with a line discount,
so it already appears in sales, receipts, P&L, and tender reports. The struck
gross + net line price already prints on the receipt.

## Out of scope for v1 (add later if wanted)

- Auto-expiring / auto-dismissing a markdown once the item starts selling again.
- A dedicated "clearance recovered value / units cleared" report.
- Category-relative "selling slower than peers" detection (v1 is the simple
  "no sale in N days" rule the owner chose).
- Cashier-facing management (this is an admin/owner tool; cashier only sees the
  till popup).

## Testing

- `store_test.go` (pure/DB-light where possible, matching the repo style):
  - the markdown clamp — suggested price never drops below `cost × (1+margin)`,
    and an already-cheap item suggests 0%.
  - staleness selection — a product with a recent sale is excluded; one with no
    sale in the window (or never sold) with stock is included; a `dismissed`
    row is excluded; services / zero-stock excluded.
- A seam guard test: with no plugin registered, the product till payload has no
  `suggestion` and the badge bridge is inert (core-only build unaffected).

## Files touched

**Core (small, additive):**
- `internal/plugin/hooks.go` — new `SaleSuggestion` + `ProductSaleSuggestionProvider`
  type, registrar, accessor.
- `internal/features/products/products.go` (or `api.go`) — `Product.Suggestion`
  field + `SaleSuggestionProvider` func-var; populate in `List`, `Get`, scan,
  `QuickPicks`.
- `internal/web/web.go` — bridge `plugin.ProductSaleSuggestionProviders()` →
  `products.SaleSuggestionProvider` (copy of the badge bridge).
- `static/js/app.js` — `addToCart` reads `product.suggestion`, prompts, applies
  the line discount.

**New plugin (`plugins/clearance/`):** `clearance.go`, `store.go`,
`store_test.go`, `admin.go`, `pages.templ`, `migrations/0001_*.sql`,
`plugin.json`; compiled in by adding a blank import
`_ "karots-pos/plugins/clearance"` to `cmd/server/enabled_plugins.go` (same
place `alternatives` is listed).
