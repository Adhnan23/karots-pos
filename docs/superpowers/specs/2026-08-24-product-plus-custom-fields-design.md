# Product Plus — custom product fields (plugin)

**Date:** 2026-08-24
**Status:** Design approved (implementation pending)

## Overview

`productplus` is a compile-time plugin that lets a shop define its own extra
product attributes ("custom fields") without touching core. A different shop
needs different things — a pharmacy wants a description, a spare-parts dealer
wants model number, part year, and "genuine (yes/no)". Baking any of that into
core `products` would bloat every install, so it lives in a plugin that
**enriches** products: it injects admin-defined fields into the core product
create/edit form, resolves sensible defaults for products that predate a field,
and (opt-in per field) makes those values findable in **every** product search —
the till, the admin product list, stock-take, and reorder alike.

Like every plugin here, core never imports it; a core-only build behaves exactly
as today. It attaches through a small set of **new generic hooks** plus one
registered func-var to break an import cycle.

## Goals

- Admin defines fields: **label, type, default value, required, searchable**, and
  (for the dropdown type) a fixed **option list**. Fields are **global** — they
  apply to every product.
- The core product **create/edit form** shows those fields inline, pre-filled
  with the current value (edit) or the default (create).
- **Required** fields are enforced **on the admin product form only**. Other
  create paths (CSV import, till quick-add, the stock-capture app) are untouched
  and resolve to the default.
- A **searchable** field's value matches on *substring* and flows into **every**
  core product search through one seam.
- Enabling the plugin on a **live** database applies only its migrations and
  never rewrites existing products (see backfill).

## Non-goals (v1)

- Per-category fields (all fields are global; per-category is a later add).
- Enforcing required custom fields on non-admin create paths.
- Numeric range / comparison search (searchable = substring match only).
- Editing custom values anywhere except the core product form.
- Itemizing custom fields on receipts / exports (separate follow-up if wanted).

## Field types (v1)

`text` (short single line), `number`, `bool` (yes/no), `select` (fixed option
list). All values are **stored as text**; the type governs the form control,
validation, and how the value is rendered. `select` carries its allowed
`options` as a JSON array on the field definition.

## Data model (plugin-owned, goose suffix `productplus`)

```sql
-- pp_fields: one admin-defined custom field (global to all products).
CREATE TABLE pp_fields (
    id            BIGSERIAL PRIMARY KEY,
    key           TEXT NOT NULL UNIQUE,           -- stable slug, used as the form field name
    label         TEXT NOT NULL,                  -- shown on the product form
    type          TEXT NOT NULL CHECK (type IN ('text','number','bool','select')),
    default_value TEXT NOT NULL DEFAULT '',       -- resolved for products with no value row
    required      BOOLEAN NOT NULL DEFAULT false, -- enforced on the admin product form only
    searchable    BOOLEAN NOT NULL DEFAULT false, -- value participates in product search
    options       JSONB,                          -- select only: ["A","B","C"]
    sort_order    INTEGER NOT NULL DEFAULT 0,
    is_active     BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- pp_values: one product's value for one field. Absence means "use the field default".
CREATE TABLE pp_values (
    field_id   BIGINT NOT NULL REFERENCES pp_fields(id) ON DELETE CASCADE,
    product_id BIGINT NOT NULL,                   -- references core products(id); no FK (plugin→core, cross-schema-lifecycle)
    value      TEXT NOT NULL,
    PRIMARY KEY (field_id, product_id)
);
CREATE INDEX idx_pp_values_product ON pp_values (product_id);
-- Trigram index to keep searchable-field substring matches fast at scale.
CREATE INDEX idx_pp_values_value_trgm ON pp_values USING gin (value gin_trgm_ops);
```

**Backfill = absence-means-default.** A product with no `pp_values` row for a
field reads as that field's `default_value`. So marking a field `required` never
rewrites existing products — they already resolve to the default, and the form
pre-fills it. A `pp_values` row is written only when an admin sets a value that
differs from (or overrides) the default. This makes "enable on a live DB" a pure
additive migration with zero row churn.

`product_id` has **no FK** to core `products`: the plugin's schema versions
independently and must not add constraints into core tables. A product delete is
rare (core soft-deactivates); orphan `pp_values` rows are harmless and can be
swept lazily. (Documented as a known ceiling below.)

## New generic core hooks (in `internal/plugin/hooks.go`)

All are additive, registered from `Setup()`, read by the web/template layer, and
**inert when no plugin registers them** — a core-only build is unchanged.

### 1. `ProductFormSection` — inject fields into the product form
```go
type ProductFormSection struct {
    // Render returns the plugin's form fragment for this product. productID == 0
    // on create (render defaults). Errors fall back to rendering nothing so a
    // plugin issue never blocks the core product form.
    Render func(ctx context.Context, productID int64) (templ.Component, error)
}
```
The core `ProductForm` handler calls each registered section's `Render` and
passes the resulting components to the product-form templ, which drops them into
a dedicated slot (after the core fields, before the submit row). The plugin's
fragment is plain inputs named `pp_<key>` — no `hx-*`; they submit with the core
form.

### 2. `ProductFormValidate` — server-side required backstop
```go
type ProductFormValidate func(ctx context.Context, form url.Values) error
```
Run by `ProductCreate` / `ProductUpdate` **before** the product is saved. The
plugin checks that every active `required` field has a non-empty `pp_<key>` in
the form and returns an `apperr.Validation` otherwise, which core surfaces as the
form error (nothing is saved). Client-side `required`/pre-filled defaults cover
the everyday case; this is the backstop against a hand-crafted POST.

### 3. `ProductSaved` — persist values after save
```go
type ProductSaved func(ctx context.Context, productID int64, form url.Values) error
```
Run by `ProductCreate` / `ProductUpdate` **after** a successful save, with the
raw form. The plugin upserts `pp_values` for each field: write the row when the
submitted value differs from the field default; delete the row when it equals the
default (keeps "absence = default" tidy). A returned error is surfaced as a toast;
because validate-before already ran, this path essentially only does writes.

### 4. `ProductSearchContributor` — searchable fields in product search
```go
type ProductSearchContributor struct {
    // Match returns product IDs whose searchable custom-field values match the
    // query (substring). Cheap, one query; return (nil, err) defensively.
    Match func(ctx context.Context, query string) ([]int64, error)
}
```
**Cycle break:** `products.List`/`Count` live in `internal/features/products`,
which `internal/plugin` imports (via `Core`), so `products` cannot import
`plugin`. Instead `products` exposes a package-level func var:
```go
// in package products
var SearchContributor func(ctx context.Context, query string) ([]int64, error)
```
`products.List`/`Count`, when `q.Search != ""` and `SearchContributor != nil`,
call it and OR the returned ids into the existing search WHERE
(`... OR p.id = ANY($extra)`) — a single generic clause, inert when the var is
nil. The web layer, during plugin setup, sets `products.SearchContributor` to a
small aggregator over `plugin.ProductSearchContributors()`. This is the same
DI-func-var pattern already used to break the cashflow↔cashregister cycle, and it
covers **every** surface at once because they all call `products.List`.

## Request flows

### Rendering the product form
`GET /admin/products/form[/:id]` → core `ProductForm` handler loads cats/units/
suppliers as today, then calls each `ProductFormSection.Render(ctx, id)` and
passes the components into `adminfragments.ProductForm(...)`, which renders them
in the new slot. Plugin fetches `pp_fields` (active, ordered) + this product's
`pp_values`, and renders one control per field (text/number input, checkbox,
or select), pre-filled with the value or default, marked `required` where set.

### Saving a product
`POST /admin/products` (or `PUT /:id`):
1. `ProductFormValidate` hooks run → block on missing required.
2. Core `products.Create`/`Update` runs as today.
3. `ProductSaved` hooks run → plugin upserts `pp_values` from the form.

Core changes here are two hook-call sites in the existing handlers; the plugin
owns all field knowledge.

### Searching products (any surface)
Handler builds `products.ListQuery{Search: term, ...}` → `products.List` →
if `SearchContributor` set, gathers plugin-matched ids → search WHERE matches
core columns **OR** `p.id = ANY(ids)`. Till `/api/products`, admin list,
stock-take, reorder all inherit this because they share `products.List`.

## Admin page (the plugin's own UI)

Registered via `AdminNavEntry` (new top-level "Product Plus" section, or nested
under an existing one — TBD in impl, default: its own section):
- **List** active/disabled fields with type, required/searchable badges, order.
- **Create / edit** a field: label, type, default value, required, searchable,
  and (type=select) the option list; validation: unique key (slugged from label),
  select requires ≥1 option, a required field should have a default.
- **Disable / re-enable** (hide-don't-delete, matching the app's convention);
  disabling stops injecting/searching it but keeps stored values.
- Reorder via `sort_order`.

## Files (new)

```
plugins/productplus/
  plugin.json            manifest (key: productplus) — makes it appear in cmd/bootstrap
  productplus.go         init()+Register, Plugin{Name,Migrations,Setup}, services over Core.DB
  store.go               pp_fields / pp_values queries (incl. Match for search)
  admin.go               admin field CRUD handlers
  form.go                ProductFormSection render + ProductSaved persist + Validate
  pages.templ            admin field-manager page + the product-form fragment
  migrations/embed.go + 0001_productplus.sql
  env.sample             (optional; likely none needed)
```

## Core changes (minimal, generic, additive)

- `internal/plugin/hooks.go`: add the 4 hook types + registrars + getters.
- `internal/web/web.go` (plugin setup): set `products.SearchContributor` to the
  aggregator over registered contributors.
- `internal/web/admin.go`: `ProductForm` renders `ProductFormSection`s;
  `ProductCreate`/`ProductUpdate` call validate-before + saved-after.
- `templates/fragments/admin` product form: one slot for injected sections.
- `internal/features/products`: add `SearchContributor` func var + OR-clause in
  `List`/`Count` (guarded, nil-safe).
- `cmd/server/enabled_plugins.go`: add the import for local dev (committed default
  stays core-only per its contract; bootstrap rewrites it per-shop anyway).

No core behavior changes when the var is nil / no hooks are registered.

## Testing

- **Plugin store (DB-guarded, rolled-back tx):** field CRUD; `pp_values` upsert
  writes/deletes around the default; `Match` returns the right product ids for a
  substring (incl. trigram index present).
- **Search seam (unit):** `products.List` with `SearchContributor` set OR-includes
  the ids; nil var = today's behavior (guard test, like the reassign-guard test).
- **Validate hook (unit):** missing required → validation error; present → nil.
- **Live E2E (Playwright):** define a `text` searchable required field → it shows
  on the product form pre-filled with the default → create a product with a value
  → that value is findable in the **till** search and the **admin list** →
  disabling the field removes it from the form and search.

## Known ceilings (mark with `ponytail:` comments)

- **Orphan `pp_values`** after a hard product delete (no FK). Harmless; sweep
  lazily or on field read. Core soft-deactivates, so rare.
- **Required enforced on the admin form only** — by design; other create paths
  resolve to the default.
- **Search = substring only**; no numeric range or exact-code fast path.
- **Custom values are not on receipts / CSV export** in v1.

## Open decisions deferred to implementation

- Whether the admin nav entry is its own top-level section or nested under an
  existing one (default: own section, matching other plugins).
- Exact placement of the injected slot within the product form (default: after
  core fields, before the submit row).
