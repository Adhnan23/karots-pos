# Alternatives plugin — design

**Date:** 2026-08-25
**Status:** Approved (design), pre-implementation
**Supersedes:** the earlier `alternatives-plugin-plan` memory sketch (pharmacy generics /
spare-part equivalents). Same core idea — group interchangeable products — but a richer,
concrete design driven by the retail pendrive use-case.

## Problem

Shops stock the *same thing* in several brands and quality tiers — e.g. a 32GB USB flash
drive as Kingston (genuine), a mid brand (normal), and a cheap no-name. Today the counter
and the reorder report treat each SKU in isolation, which causes three pains:

1. **Counter search is noisy.** Searching "32gb pendrive" per-item also surfaces unrelated
   things (SD cards); the cashier can't see "all our 32GB pendrives" at once.
2. **No substitution.** A customer asks for Kingston; it's out. The cashier has no fast way
   to see "we have the same-quality Samsung."
3. **Reorder is per-SKU.** Kingston 32GB reads low and gets reordered even though the shop
   has plenty of equivalent 32GB drives. Reorder should be judged by the *interchangeable
   group's* availability, not one SKU.

## Concept

An admin groups interchangeable products. A **group** (e.g. "USB Flash Drive 32GB") holds
admin-defined **quality tiers** (Genuine / Normal / Cheap — customizable per group). Each
tier holds member **products** and its own **reorder level**. Product Plus custom fields
(e.g. a "storage size" field) make finding the right products to add much easier — that is
why Product Plus was built first.

The grouping then powers: group-aware counter search, always-on sibling/alternative results
with a quality **pin** on each till card, and a **reorder-by-alternatives** report that rolls
qty up to the tier and compares against the tier reorder level.

## Membership rule

**Exactly one.** A product belongs to at most one group and one tier. Enforced by a UNIQUE
constraint on `alt_members.product_id`. Adding a product that already belongs elsewhere
prompts a "move it here?" confirm. A product in no group behaves exactly as today.

## Data model (plugin migrations, `alt_` prefix)

```
alt_groups
  id            bigserial pk
  name          text not null
  sort_order    int  not null default 0
  is_active     bool not null default true
  created_at    timestamptz not null default now()

alt_tiers
  id            bigserial pk
  group_id      bigint not null references alt_groups(id) on delete cascade
  name          text not null
  reorder_level int  not null default 0
  sort_order    int  not null default 0
  is_active     bool not null default true

alt_members
  product_id    bigint primary key          -- UNIQUE ⇒ exactly-one membership
  tier_id       bigint not null references alt_tiers(id) on delete cascade
  created_at    timestamptz not null default now()
```

- No FK on `alt_members.product_id` — products are soft-deleted (same posture as Product
  Plus `pp_values`); a stale member id is harmless (joins to `products` drop it).
- Group of a member is derived via `alt_tiers.group_id` (no denormalized group_id).
- Deleting a group cascades tiers; deleting a tier cascades its members.

## Admin page

Route group under `/admin/alternatives` (admin/manager only), own admin-nav entry.

- **`GET /admin/alternatives`** — groups list: name · #tiers · #products · total on-hand qty;
  "Show disabled" toggle (app convention); "+ Add group".
- **`GET /admin/alternatives/:id`** — one group:
  - Tiers, each: name, **editable reorder level**, member products (with remove), "Add
    products". Add / rename / disable / reorder tiers.
  - **Add products** opens a product search (reuses `/api/products?search=`); select and add
    many. If a picked product is already a member elsewhere → confirm "move from group X".
- CRUD routes: `POST /admin/alternatives` (group), `PUT /admin/alternatives/:id`,
  `POST /admin/alternatives/:id/active`; tier + member CRUD nested under the group.
- All mutations write to the shared **audit trail** (entity `alt_group` / `alt_tier` /
  `alt_member`), matching Product Plus.

## Cashier integration

Two seams, both **inert when the plugin is absent**.

### 4.1 Group-aware + sibling search (reuses existing seam)

Register a `plugin.ProductSearchContributor` (the seam fixed in the Product Plus round; the
web layer already fans all contributors into `products.SearchContributor`). One query, given
the raw search string `q`:

```sql
WITH hit AS (
  SELECT DISTINCT g.id AS group_id
  FROM alt_groups g
  JOIN alt_tiers t   ON t.group_id = g.id AND t.is_active
  JOIN alt_members m ON m.tier_id  = t.id
  JOIN products p    ON p.id = m.product_id AND p.is_active
  WHERE g.is_active AND (
    g.name ILIKE '%'||$1||'%' OR t.name ILIKE '%'||$1||'%' OR p.name ILIKE '%'||$1||'%'
  )
)
SELECT DISTINCT m.product_id
FROM alt_members m
JOIN alt_tiers t ON t.id = m.tier_id
JOIN hit h       ON h.group_id = t.group_id
JOIN products p  ON p.id = m.product_id AND p.is_active
```

This returns **all active members of any group whose name, a tier name, or a member product
name matched** — giving both group-name search ("32gb pendrive") and always-on siblings
(search "Kingston" → whole group). The core `List` ORs these ids into its results.

**Known limit:** the seam only *adds* ids (results order by name); it cannot rank the exact
match above its alternatives. The tier pin (below) is what distinguishes them. Accepted for v1.

### 4.2 Tier pin on the till card (new generic seam)

The till renders cards from `/api/products` JSON + Alpine, so a plugin cannot inject server
HTML. Add a generic, batched badge seam mirroring the search one:

- **New hook** `plugin.ProductBadgeProvider{ Batch func(ctx, ids []int64) (map[int64][]string, error) }`
  with `AddProductBadgeProvider` / `ProductBadgeProviders()`.
- **New func-var seam** `products.BadgeProvider func(ctx, ids []int64) map[int64][]string`,
  set by the web layer to fan out over all registered badge providers (same bridge pattern as
  `SearchContributor`).
- **New json-only field** `Product.Badges []string` (`db:"-" json:"badges,omitempty"`),
  populated in the `/api/products` `List` (and `Get`) handler from `BadgeProvider`. Empty
  when no provider → field omitted.
- The Alternatives plugin's badge provider returns each member product's **tier name**
  (e.g. `["Genuine"]`). `app.js` renders each badge as a small pin on the product card.

Batched: one query per rendered page, no per-row calls.

## Reorder-by-alternatives report

New plugin-owned page `GET /admin/alternatives/reorder` (chosen over a checkbox on the core
reorder report, to avoid restructuring a core report):

- **Per group:** group total on-hand qty; then per **tier**: summed on-hand qty of active
  members vs the tier's reorder level, flagged **low** when `total ≤ reorder_level`.
- So a low Kingston is *not* flagged while its Genuine tier total is healthy; the tier is
  flagged only when the whole interchangeable tier runs down.
- **Ungrouped low-stock section:** below the groups, list low-stock products that are in no
  group (from the core low-stock list), so nothing falls through the cracks.
- CSV export, matching other reports.

Qty source: core `Products`/`Stock` service (the plugin reads on-hand qty per product id).

## Core changes (complete list)

Minimal, generic, inert:

1. `internal/plugin/hooks.go` — new `ProductBadgeProvider` type + `AddProductBadgeProvider`
   + `ProductBadgeProviders()`.
2. `internal/web/web.go` — after `SetupAll`, bridge `ProductBadgeProviders()` into
   `products.BadgeProvider` (fan-out, skip-on-error), exactly like the search bridge.
3. `internal/features/products/products.go` — `var BadgeProvider func(...)`; add
   `Badges []string` (`db:"-" json:"badges,omitempty"`) to `Product`.
4. `internal/features/products/api.go` — in `List` (and `Get`), populate `Badges` from
   `BadgeProvider` when set.
5. `static/js/app.js` — render `p.badges` as small pins on the till product card.

Search needs **no** new core code — the seam already exists. No core report changes.

## Edge cases / decisions

- **Sibling ranking:** not supported by the seam; pin distinguishes match vs alternatives.
- **Qty sum:** plain sum of member on-hand qty — assumes same-unit, 1:1-interchangeable tiers
  (documented; no unit conversion).
- **Inactive products:** excluded from tier sums and from search/badges.
- **Exactly-one:** enforced by UNIQUE; add-elsewhere → move-with-confirm.
- **"Low" rule:** a tier is flagged low when `reorder_level > 0 AND total_qty ≤ reorder_level`.
  A `reorder_level` of `0` means "don't track" — never low (so an empty tier at level 0 is not
  flagged). Empty groups/tiers render gracefully with zero totals.
- **Group/tier disabled:** excluded from search, badges, and reorder rollup.

## Testing

- Store unit tests (rolled-back tx, `zz_`-prefixed names): group/tier/member CRUD;
  exactly-one move; `MatchProductIDs`-style contributor query returns whole group for a
  group-name, tier-name, and member-name hit; badge batch returns tier per product;
  reorder rollup sums active members and flags by tier level.
- Live E2E: build a "32GB pendrive" group with Genuine/Normal/Cheap; till search by group
  name and by a member shows the whole group with pins; reorder page flags a tier when its
  total ≤ level and hides a low SKU whose tier is healthy; ungrouped low item still listed.
- `go test ./...` green with the plugin compiled out (core seams inert).

## Out of scope (YAGNI / deferred)

- Custom-field-style CSV of group membership.
- Many-groups-per-product.
- Ranked search results (exact-above-alternatives) — needs a richer seam than id-adding.
- Cross-group "related" suggestions beyond same-group siblings.
- Unit conversion in tier sums.
