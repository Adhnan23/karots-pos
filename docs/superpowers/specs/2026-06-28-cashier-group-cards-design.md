# Cashier Group-Card Navigation — Design

**Date:** 2026-06-28
**Status:** Approved (design); pending spec review

## Problem

The cashier till's default (non-search) view is a flat grid of product cards driven
by `DefaultGrid` (pinned products first, then 30-day best sellers, then the rest).
Product **pinning** does not behave the way the shop wants, and a flat grid does not
scale to a large catalogue. The shop wants to navigate the till by **curated groups**
(like a menu): tap a group, drill into subgroups and/or products, with a Back button.

Search must keep working exactly as today (type → flat product results).

## Goals

- Replace the default product-card grid with a **hierarchy of group cards** the shop
  curates in admin.
- Groups are nestable to arbitrary depth; a group may contain **both** subgroups and
  directly-linked products ("mixed" content).
- A group has an optional **emoji** icon. A product **linked into a group** has an
  optional emoji **on that link** (per-group, not on the product).
- Products are linked into groups **many-to-many** (a product can be in several
  groups, or none).
- **Remove product pinning entirely** (column, form field, default-grid path).
- Search behaviour is unchanged.

## Non-Goals

- No emoji *picker* widget — a plain text input (paste/type an emoji) is enough.
- No automatic "Other/Uncategorised" bucket at the till. Unlinked products are
  reachable **only via search** (decided).
- No change to the `categories` table — it stays for reporting/CSV. Groups are a
  separate, parallel structure.
- No reporting on groups.

## Data Model

New feature package: `internal/features/productgroups`.

### Migration `0040_product_groups.sql`

```sql
CREATE TABLE product_groups (
    id         BIGSERIAL PRIMARY KEY,
    name       VARCHAR(80) NOT NULL,
    emoji      VARCHAR(16),                              -- optional icon for the group card
    parent_id  BIGINT REFERENCES product_groups(id) ON DELETE CASCADE,
    sort_order INT NOT NULL DEFAULT 0,
    is_active  BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_product_groups_parent ON product_groups(parent_id);

CREATE TABLE product_group_items (
    group_id   BIGINT NOT NULL REFERENCES product_groups(id) ON DELETE CASCADE,
    product_id BIGINT NOT NULL REFERENCES products(id)       ON DELETE CASCADE,
    emoji      VARCHAR(16),                              -- optional icon for THIS product IN THIS group
    sort_order INT NOT NULL DEFAULT 0,
    PRIMARY KEY (group_id, product_id)
);
CREATE INDEX idx_pgi_product ON product_group_items(product_id);
```

- `parent_id IS NULL` = a top-level group.
- `ON DELETE CASCADE` on `parent_id`: deleting a group deletes its subtree of groups
  and their item links (not the products themselves).
- `ON DELETE CASCADE` on `product_id`: deleting a product removes it from all groups.
- Emoji stored as text (holds a multi-byte emoji); `VARCHAR(16)` is generous headroom.

### Migration `0041_drop_is_pinned.sql`

```sql
-- Up
ALTER TABLE products DROP COLUMN is_pinned;
-- Down
ALTER TABLE products ADD COLUMN is_pinned BOOLEAN NOT NULL DEFAULT false;
```

## Backend

### `internal/features/productgroups` (new)

Repository (raw SQL on the shared `db.Queryer`) + Service wrapper, mirroring existing
feature packages.

- `Group` struct: id, name, emoji *string, parentID *int64, sortOrder, isActive,
  plus derived `HasChildren bool` and `ItemCount int` for the admin tree.
- `GroupItem` / `GroupProduct` struct: product id, name, selling price, unit, the
  link emoji, sort order (enough to render a product card without a second lookup).

Methods:
- `Children(ctx, parentID *int64) ([]Group, error)` — groups at one level
  (`parent_id = $1`, or top-level when nil), ordered by `sort_order, name`.
- `Products(ctx, groupID int64) ([]GroupProduct, error)` — linked products for a
  group, joined to `products` (active only), ordered by `sort_order, name`.
- `Get(ctx, id) (*Group, error)` — for breadcrumb/back (need its `parent_id`).
- `Tree(ctx) ([]Group, error)` — whole tree for the admin page.
- CRUD: `Create`, `Update` (name/emoji/parent/sort), `Delete`, `Reorder`.
- Links: `LinkProduct(ctx, groupID, productID, emoji)`, `UnlinkProduct`,
  `SetItemEmoji`, `ReorderItems`.

Every query orders by `(sort_order, name)` **plus** a unique tiebreaker (`id`) to keep
ordering stable (see the pagination bug fixed in `d5d044c`).

### Cashier endpoints (`internal/web`)

- `GET /api/groups` → JSON: top-level groups (cards for the default view).
- `GET /api/groups/:id` → JSON: `{ group, breadcrumb[], children[], products[] }`
  for one group — subgroup cards + linked product cards + the path for Back/breadcrumb.

These return JSON consumed by the existing Alpine POS view, alongside `/api/products`
and `/api/customers`. Product entries reuse the shape the cart already expects.

### Removals

- `products.is_pinned` column (migration 0041).
- `Product.IsPinned`, `CreateInput.IsPinned`, `UpdateInput.IsPinned`, `ImportRow.IsPinned`
  and their SQL/INSERT/UPDATE references in `internal/features/products/products.go`
  and `service.go`.
- `Service.DefaultGrid`, `Repository.DefaultGrid`, `APIHandler.DefaultGrid`, and the
  `GET /api/products/default` route in `internal/features/products/api.go`.
- The "Pin to cashier" checkbox in `templates/fragments/admin/products.templ`.
- `loadDefaultGrid()` and the `/api/products/default` call in `static/js/app.js`
  (replaced by the group view — see Cashier UI).

## Admin UI

New page **Setup → "Cashier Menu"** (`/admin/groups`), added to the Setup section in
`templates/layouts/admin.templ`.

- **Tree view** of groups (indented, showing emoji + name + item count). Each node:
  Add subgroup · Edit · Delete · reorder (up/down).
- **Group form** (modal, mirrors the product form modal): name, optional emoji
  (text input), parent (group picker; blank = top level), sort order.
- **Open a group** → its detail panel:
  - list of linked products with their per-link emoji input + unlink + reorder;
  - **Add products** via the existing product **search picker** (the reusable
    name/barcode picker already used by purchase orders); on pick, insert a
    `product_group_items` row, optionally with an emoji.

Handlers in a new `internal/web/admin_groups.go`; templates in
`templates/pages/admin/groups.templ`. Follows the existing HTMX modal + `reload-*`
fragment pattern.

## Cashier UI

The catalogue area (left of the cart in `templates/pages/cashier/pos.templ`) gains a
**group-navigation mode** that replaces the default product grid. Search is unchanged.

Alpine state additions:
- `groupStack: []` — breadcrumb of `{id, name}` from root to current group.
- `groupChildren: []`, and the existing `products: []` reused for the current group's
  linked products (and for search results).

Behaviour (`loadProducts()` rewrite):
- **Search non-empty** → flat product search exactly as today (`/api/products?search=`),
  sets `products`, hides group cards. (Back button hidden in search mode.)
- **Search empty, no group open** → `GET /api/groups`; show top-level group cards,
  no products, no Back.
- **Tap a group card** → `GET /api/groups/:id`; push onto `groupStack`; show its
  subgroup cards **then** its product cards; show **Back** (and a breadcrumb).
- **Back** → pop `groupStack`; reload the parent level (or top-level if empty).
- **Tap a product card** → add to cart (unchanged).
- **Clear search** → return to the current group level (or top-level).

Rendering: a group card shows `emoji + name`; a product card shows the link emoji (if
any) + name + price, identical interaction to today's product cards.

## Edge Cases

- **Empty group** (no children, no products) → show a muted "Nothing here yet" with
  Back. Admin can still link products.
- **Deleted/inactive product** linked in a group → excluded by the active-only join in
  `Products`.
- **Deleting a group** with subgroups → cascades (subtree + links removed); products
  untouched. Confirm dialog in admin.
- **A product in many groups** → appears in each; its emoji can differ per group.
- **No groups defined yet** → default till view is empty except the search bar; admin
  Cashier Menu page guides creating the first group. (Acceptable; search still works.)

## Migration / Rollout

- Two migrations (0040 create, 0041 drop is_pinned), both reversible.
- No data backfill: groups start empty; the shop curates them. Existing products are
  unaffected and remain searchable.
- Core-only change; no plugin involvement. `enabled_plugins.go` untouched.

## Testing

- `make templ && go build ./... && go vet ./...` green.
- Migrations up/down clean (`goose up`/`down`), `is_pinned` gone then restorable.
- E2E (Playwright + psql, admin 0000000001/2273):
  - Admin: create "Fruits & Veg" (emoji), child "Fruits" + "Vegetables", link 2
    products into "Fruits" with emojis; reorder; verify rows in `product_groups` /
    `product_group_items`.
  - Cashier: default view shows top-level cards; tap drills to subgroups then products;
    Back returns up the path; tapping a product adds to cart; search still returns flat
    results and Back hides; clearing search restores the group view.
  - Confirm `/api/products/default` is gone and the till no longer references pinning.

## Out of Scope / Future

- Drag-and-drop reordering (start with up/down).
- Emoji picker widget.
- Group-level reporting or sales analytics.
