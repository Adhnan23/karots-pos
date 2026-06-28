# Cashier Group-Card Navigation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the cashier till's flat default product grid with a curated, nestable hierarchy of group cards (with emojis), and remove product pinning entirely.

**Architecture:** A new `productgroups` feature package backs two new tables (`product_groups` self-referencing tree + `product_group_items` many-to-many product links with a per-link emoji). Admin/manager curate the tree in a new admin page; the till consumes read-only JSON (`/api/groups`) and drills down with a Back button. Search behaviour is unchanged.

**Tech Stack:** Go, Echo v4, sqlx, lib/pq, Goose migrations, Templ, Tailwind v3, Alpine.js, HTMX, shopspring/decimal, Postgres 17.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-06-28-cashier-group-cards-design.md`.
- Do NOT commit `cmd/server/enabled_plugins.go` (keep remote core-only).
- Do NOT stage `static/css/tailwind.css`; run `make css` when new Tailwind classes are added, leave it unstaged.
- Commit messages end with: `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`.
- `_templ.go` are gitignored/generated — run `make templ` after editing `.templ`; never stage `_templ.go`.
- Testing convention: pure logic → table-driven unit test; repositories/handlers/UI → `go build ./... && go vet ./...` + E2E (Playwright + psql, dev admin `0000000001`/`2273`). Do NOT build a DB unit-test harness — none exists.
- Dev server runs via `make watch` (air live-reload) on `:3000`; edits hot-reload, no manual restart.
- Emoji is stored as text (`VARCHAR(16)`); the group emoji lives on `product_groups`, the per-product icon lives on the **link** (`product_group_items.emoji`), never on `products`.
- Every list query orders by `(sort_order, name)` plus a unique `id` tiebreaker (stable-ordering rule from commit `d5d044c`).

---

### Task 1: Migration — create group tables

**Files:**
- Create: `migrations/0040_product_groups.sql`

**Interfaces:**
- Produces: tables `product_groups(id, name, emoji, parent_id, sort_order, is_active, created_at)` and `product_group_items(group_id, product_id, emoji, sort_order)`.

- [ ] **Step 1: Write the migration**

```sql
-- +goose Up
CREATE TABLE product_groups (
    id         BIGSERIAL PRIMARY KEY,
    name       VARCHAR(80) NOT NULL,
    emoji      VARCHAR(16),
    parent_id  BIGINT REFERENCES product_groups(id) ON DELETE CASCADE,
    sort_order INT NOT NULL DEFAULT 0,
    is_active  BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_product_groups_parent ON product_groups(parent_id);

CREATE TABLE product_group_items (
    group_id   BIGINT NOT NULL REFERENCES product_groups(id) ON DELETE CASCADE,
    product_id BIGINT NOT NULL REFERENCES products(id)       ON DELETE CASCADE,
    emoji      VARCHAR(16),
    sort_order INT NOT NULL DEFAULT 0,
    PRIMARY KEY (group_id, product_id)
);
CREATE INDEX idx_pgi_product ON product_group_items(product_id);

-- +goose Down
DROP TABLE product_group_items;
DROP TABLE product_groups;
```

- [ ] **Step 2: Apply and verify up**

Run: `make migrate`
Expected: no error; then `docker exec -i pos_db psql -U pos_user -d pos_db -c "\d product_groups"` shows the table.

- [ ] **Step 3: Verify down/up cleanly**

Run: `set -a && . ./.env && set +a && go run ./cmd/server -migrate` is idempotent. Manually test rollback once: `docker exec -i pos_db psql -U pos_user -d pos_db -c "\dt product_group*"` lists both tables.

- [ ] **Step 4: Commit**

```bash
git add migrations/0040_product_groups.sql
git commit -m "feat(groups): migration for product_groups + product_group_items

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: `productgroups` feature package (repository + service)

**Files:**
- Create: `internal/features/productgroups/productgroups.go` (structs + Repository)
- Create: `internal/features/productgroups/service.go` (Service)

**Interfaces:**
- Consumes: `internal/db.Queryer`, `internal/apperr`, `github.com/jmoiron/sqlx`, `github.com/shopspring/decimal`.
- Produces:
  - `type Group struct { ID int64; Name string; Emoji *string; ParentID *int64; SortOrder int; IsActive bool; HasChildren bool; ItemCount int }`
  - `type GroupProduct struct { ProductID int64; Name string; SellingPrice decimal.Decimal; UnitAbbr string; Barcode *string; Emoji *string; SortOrder int }`
  - `type CreateInput struct { Name string; Emoji *string; ParentID *int64 }`
  - `type UpdateInput struct { Name string; Emoji *string }`
  - `Service` methods: `Children(ctx, *int64) ([]Group, error)`, `Get(ctx, int64) (*Group, error)`, `Products(ctx, int64) ([]GroupProduct, error)`, `Tree(ctx) ([]Group, error)`, `Create(ctx, CreateInput) (int64, error)`, `Update(ctx, int64, UpdateInput) error`, `Delete(ctx, int64) error`, `LinkProduct(ctx, groupID, productID int64, emoji *string) error`, `UnlinkProduct(ctx, groupID, productID int64) error`, `SetItemEmoji(ctx, groupID, productID int64, emoji *string) error`, `Move(ctx, id int64, dir string) error`.

- [ ] **Step 1: Write `productgroups.go`**

```go
package productgroups

import (
	"context"
	"strings"

	"karots-pos/internal/db"

	"github.com/shopspring/decimal"
)

// Group is one node in the cashier menu tree. HasChildren and ItemCount are
// derived (populated by Children/Tree) to drive the admin view and to decide
// whether a till card drills into subgroups or shows products.
type Group struct {
	ID          int64   `db:"id"`
	Name        string  `db:"name"`
	Emoji       *string `db:"emoji"`
	ParentID    *int64  `db:"parent_id"`
	SortOrder   int     `db:"sort_order"`
	IsActive    bool    `db:"is_active"`
	HasChildren bool    `db:"has_children"`
	ItemCount   int     `db:"item_count"`
}

// GroupProduct is a product linked into a group, with the per-link emoji and
// enough fields to render a till card without a second lookup.
type GroupProduct struct {
	ProductID    int64           `db:"product_id"`
	Name         string          `db:"name"`
	SellingPrice decimal.Decimal `db:"selling_price"`
	UnitAbbr     string          `db:"unit_abbr"`
	Barcode      *string         `db:"barcode"`
	Emoji        *string         `db:"emoji"`
	SortOrder    int             `db:"sort_order"`
}

type CreateInput struct {
	Name     string
	Emoji    *string
	ParentID *int64
}

type UpdateInput struct {
	Name  string
	Emoji *string
}

type Repository struct{ db db.Queryer }

func NewRepository(q db.Queryer) *Repository { return &Repository{db: q} }

// groupSelect lists groups at one level with derived child/item counts.
const groupSelect = `
	SELECT g.id, g.name, g.emoji, g.parent_id, g.sort_order, g.is_active,
	       EXISTS (SELECT 1 FROM product_groups c WHERE c.parent_id = g.id AND c.is_active) AS has_children,
	       (SELECT COUNT(*) FROM product_group_items i WHERE i.group_id = g.id)              AS item_count
	FROM product_groups g`

// Children returns active groups at one level: top-level when parentID is nil,
// otherwise the direct children of that group, ordered stably.
func (r *Repository) Children(ctx context.Context, parentID *int64) ([]Group, error) {
	var rows []Group
	err := r.db.SelectContext(ctx, &rows, groupSelect+`
		WHERE g.is_active
		  AND ($1::bigint IS NULL AND g.parent_id IS NULL OR g.parent_id = $1)
		ORDER BY g.sort_order, g.name, g.id`, parentID)
	return rows, err
}

// Tree returns every active group (any level) for the admin page, stably ordered.
func (r *Repository) Tree(ctx context.Context) ([]Group, error) {
	var rows []Group
	err := r.db.SelectContext(ctx, &rows, groupSelect+`
		WHERE g.is_active
		ORDER BY g.sort_order, g.name, g.id`)
	return rows, err
}

func (r *Repository) Get(ctx context.Context, id int64) (*Group, error) {
	var g Group
	if err := r.db.GetContext(ctx, &g, groupSelect+` WHERE g.id = $1`, id); err != nil {
		return nil, err
	}
	return &g, nil
}

// Products returns the active products linked into a group, stably ordered.
func (r *Repository) Products(ctx context.Context, groupID int64) ([]GroupProduct, error) {
	var rows []GroupProduct
	err := r.db.SelectContext(ctx, &rows, `
		SELECT p.id AS product_id, p.name, p.selling_price, u.abbreviation AS unit_abbr,
		       p.barcode, i.emoji, i.sort_order
		FROM product_group_items i
		JOIN products p ON p.id = i.product_id
		JOIN units u    ON u.id = p.unit_id
		WHERE i.group_id = $1 AND p.is_active
		ORDER BY i.sort_order, p.name, p.id`, groupID)
	return rows, err
}

func (r *Repository) Create(ctx context.Context, in CreateInput) (int64, error) {
	var id int64
	err := r.db.GetContext(ctx, &id, `
		INSERT INTO product_groups (name, emoji, parent_id, sort_order)
		VALUES ($1, $2, $3, COALESCE(
			(SELECT MAX(sort_order)+1 FROM product_groups
			 WHERE parent_id IS NOT DISTINCT FROM $3), 0))
		RETURNING id`, strings.TrimSpace(in.Name), in.Emoji, in.ParentID)
	return id, err
}

func (r *Repository) Update(ctx context.Context, id int64, in UpdateInput) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE product_groups SET name = $1, emoji = $2 WHERE id = $3`,
		strings.TrimSpace(in.Name), in.Emoji, id)
	return err
}

func (r *Repository) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM product_groups WHERE id = $1`, id)
	return err
}

func (r *Repository) LinkProduct(ctx context.Context, groupID, productID int64, emoji *string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO product_group_items (group_id, product_id, emoji, sort_order)
		VALUES ($1, $2, $3, COALESCE(
			(SELECT MAX(sort_order)+1 FROM product_group_items WHERE group_id = $1), 0))
		ON CONFLICT (group_id, product_id) DO UPDATE SET emoji = EXCLUDED.emoji`,
		groupID, productID, emoji)
	return err
}

func (r *Repository) UnlinkProduct(ctx context.Context, groupID, productID int64) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM product_group_items WHERE group_id = $1 AND product_id = $2`, groupID, productID)
	return err
}

func (r *Repository) SetItemEmoji(ctx context.Context, groupID, productID int64, emoji *string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE product_group_items SET emoji = $3 WHERE group_id = $1 AND product_id = $2`,
		groupID, productID, emoji)
	return err
}

// swapOrder moves group id one step up/down among its siblings by swapping
// sort_order with the adjacent sibling in the requested direction.
func (r *Repository) swapOrder(ctx context.Context, id int64, dir string) error {
	op := "<"
	ord := "DESC"
	if dir == "down" {
		op = ">"
		ord = "ASC"
	}
	_, err := r.db.ExecContext(ctx, `
		WITH me AS (SELECT id, parent_id, sort_order FROM product_groups WHERE id = $1),
		neighbor AS (
			SELECT g.id, g.sort_order FROM product_groups g, me
			WHERE g.parent_id IS NOT DISTINCT FROM me.parent_id
			  AND g.sort_order `+op+` me.sort_order
			ORDER BY g.sort_order `+ord+` LIMIT 1),
		swap AS (
			UPDATE product_groups SET sort_order = (SELECT sort_order FROM me)
			WHERE id = (SELECT id FROM neighbor) RETURNING 1)
		UPDATE product_groups SET sort_order = (SELECT sort_order FROM neighbor)
		WHERE id = $1 AND EXISTS (SELECT 1 FROM neighbor)`, id)
	return err
}
```

- [ ] **Step 2: Write `service.go`**

```go
package productgroups

import (
	"context"

	"karots-pos/internal/apperr"

	"github.com/jmoiron/sqlx"
)

type Service struct {
	db   *sqlx.DB
	repo *Repository
}

func NewService(db *sqlx.DB) *Service {
	return &Service{db: db, repo: NewRepository(db)}
}

func (s *Service) Children(ctx context.Context, parentID *int64) ([]Group, error) {
	rows, err := s.repo.Children(ctx, parentID)
	if err != nil {
		return nil, apperr.Internal("failed to list groups", err)
	}
	return rows, nil
}

func (s *Service) Tree(ctx context.Context) ([]Group, error) {
	rows, err := s.repo.Tree(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to load group tree", err)
	}
	return rows, nil
}

func (s *Service) Get(ctx context.Context, id int64) (*Group, error) {
	g, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, apperr.NotFound("group not found")
	}
	return g, nil
}

func (s *Service) Products(ctx context.Context, groupID int64) ([]GroupProduct, error) {
	rows, err := s.repo.Products(ctx, groupID)
	if err != nil {
		return nil, apperr.Internal("failed to load group products", err)
	}
	return rows, nil
}

// Breadcrumb returns the path from the root down to id (inclusive), for the
// till's Back/breadcrumb. Walks parent links; capped to avoid cycles.
func (s *Service) Breadcrumb(ctx context.Context, id int64) ([]Group, error) {
	var path []Group
	cur := &id
	for i := 0; cur != nil && i < 50; i++ {
		g, err := s.repo.Get(ctx, *cur)
		if err != nil {
			return nil, apperr.NotFound("group not found")
		}
		path = append([]Group{*g}, path...)
		cur = g.ParentID
	}
	return path, nil
}

func (s *Service) Create(ctx context.Context, in CreateInput) (int64, error) {
	if in.Name == "" {
		return 0, apperr.Validation("group name is required")
	}
	id, err := s.repo.Create(ctx, in)
	if err != nil {
		return 0, apperr.Internal("failed to create group", err)
	}
	return id, nil
}

func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) error {
	if in.Name == "" {
		return apperr.Validation("group name is required")
	}
	if err := s.repo.Update(ctx, id, in); err != nil {
		return apperr.Internal("failed to update group", err)
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return apperr.Internal("failed to delete group", err)
	}
	return nil
}

func (s *Service) LinkProduct(ctx context.Context, groupID, productID int64, emoji *string) error {
	if err := s.repo.LinkProduct(ctx, groupID, productID, emoji); err != nil {
		return apperr.Internal("failed to link product", err)
	}
	return nil
}

func (s *Service) UnlinkProduct(ctx context.Context, groupID, productID int64) error {
	if err := s.repo.UnlinkProduct(ctx, groupID, productID); err != nil {
		return apperr.Internal("failed to unlink product", err)
	}
	return nil
}

func (s *Service) SetItemEmoji(ctx context.Context, groupID, productID int64, emoji *string) error {
	if err := s.repo.SetItemEmoji(ctx, groupID, productID, emoji); err != nil {
		return apperr.Internal("failed to set emoji", err)
	}
	return nil
}

func (s *Service) Move(ctx context.Context, id int64, dir string) error {
	if dir != "up" && dir != "down" {
		return apperr.Validation("direction must be up or down")
	}
	if err := s.repo.swapOrder(ctx, id, dir); err != nil {
		return apperr.Internal("failed to reorder group", err)
	}
	return nil
}
```

- [ ] **Step 3: Verify apperr helpers exist**

Run: `grep -n "func NotFound\|func Validation\|func Internal" internal/apperr/*.go`
Expected: all three exist (use whatever the exact names are; adjust calls if the codebase uses e.g. `apperr.NotFound`).

- [ ] **Step 4: Build & vet**

Run: `go build ./... && go vet ./internal/features/productgroups/`
Expected: no output (clean). The package compiles but is not yet wired anywhere.

- [ ] **Step 5: Commit**

```bash
git add internal/features/productgroups/
git commit -m "feat(groups): productgroups repository + service

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: Wire the service + admin management UI

**Files:**
- Modify: `internal/web/server.go` (or wherever feature services are constructed — find with grep) to construct `productgroups.NewService` and expose it on the server struct as `s.groups`.
- Create: `internal/web/admin_groups.go` (handlers)
- Create: `templates/pages/admin/groups.templ` (page + fragments)
- Modify: `internal/web/web.go` (routes)
- Modify: `templates/layouts/admin.templ` (Setup nav entry)

**Interfaces:**
- Consumes: `productgroups.Service` (Task 2); the existing product search picker used by purchase orders (find with `grep -rn "ProductPicker\|product.*search.*picker" templates/fragments/admin/`).
- Produces: routes `GET /admin/groups`, `GET /admin/groups/tree`, `GET /admin/groups/form`, `GET /admin/groups/form/:id`, `POST /admin/groups`, `PUT /admin/groups/:id`, `DELETE /admin/groups/:id`, `POST /admin/groups/:id/move`, `GET /admin/groups/:id/items`, `POST /admin/groups/:id/items`, `DELETE /admin/groups/:id/items/:productId`, `PUT /admin/groups/:id/items/:productId/emoji`.

- [ ] **Step 1: Locate service construction & server struct**

Run: `grep -rn "NewService\|warranty\s*\*\|s\.warranty\|type Server struct\|adminUI struct" internal/web/*.go | head -30`
Identify where services like `warranty`, `lockers` are constructed and stored. Add a `groups *productgroups.Service` field and construct it with `productgroups.NewService(sqlxDB)` alongside the others. Expose to `adminUI` the same way other services are reached (the codebase uses `a.s.<service>`).

- [ ] **Step 2: Add nav entry** in `templates/layouts/admin.templ`, in the Setup section's `[]AdminLink`, after Settings or near Categories:

```go
{"/admin/groups", "Cashier Menu", "groups", "Group cards shown on the till"},
```

- [ ] **Step 3: Write `templates/pages/admin/groups.templ`**

Mirror the product page/modal patterns (`templates/pages/admin/products.templ`, `templates/fragments/admin/products.templ`). Components:
- `GroupsPage(d GroupsData)` — admin shell, a two-column layout: left = group tree (`GroupTree`), right = selected group detail loaded via HTMX.
- `GroupsData struct { UserName string; Symbol string; Tree []productgroups.Group }`
- `GroupTree(tree []productgroups.Group)` — render the tree indented by walking parent relationships (build a children map keyed by ParentID; render roots then recurse). Each row: emoji + name + `(itemCount)`; buttons: **+ Sub** (`hx-get /admin/groups/form?parent=<id>`), **Edit** (`hx-get /admin/groups/form/<id>`), **▲/▼** (`hx-post /admin/groups/<id>/move` with `dir`), **Delete** (`hx-delete` + `hx-confirm`), and **Open** (`hx-get /admin/groups/<id>/items` into the right panel).
- `GroupForm(g *productgroups.Group, parentID string)` — modal: name input, emoji text input (`maxlength="8"`), hidden `parent_id`; `hx-post /admin/groups` (create) or `hx-put /admin/groups/<id>` (edit), `hx-swap="none"`.
- `GroupItems(d GroupItemsData)` — right panel for one group: its linked products with a per-row emoji input (`hx-put .../emoji` on change) + Unlink button; plus an **Add products** control = the existing product search picker that posts `product_id` (+ optional emoji) to `POST /admin/groups/<id>/items`.
- `GroupItemsData struct { Symbol string; Group productgroups.Group; Items []productgroups.GroupProduct }`

Render emoji safely: `if g.Emoji != nil { {*g.Emoji} }`.

- [ ] **Step 4: Write `internal/web/admin_groups.go`** handlers mirroring `admin.go` product handlers (parse id via `strconv.ParseInt`, bind form values, `htmxDone`/`htmxReload`/`response.RenderFragment`). Emoji form value → `*string` (nil when blank). Example shape:

```go
func (a *adminUI) Groups(c echo.Context) error {
	ctx := c.Request().Context()
	tree, err := a.s.groups.Tree(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.GroupsPage(adminpages.GroupsData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Tree:     tree,
	}))
}

// emojiPtr returns nil for a blank emoji field, else a trimmed pointer.
func emojiPtr(c echo.Context, field string) *string {
	v := strings.TrimSpace(c.FormValue(field))
	if v == "" {
		return nil
	}
	return &v
}
```

Implement: `GroupsTree` (fragment), `GroupForm`, `GroupCreate`, `GroupUpdate`, `GroupDelete`, `GroupMove`, `GroupItems`, `GroupItemAdd`, `GroupItemRemove`, `GroupItemEmoji`. `GroupCreate` reads `name`, `emoji`, `parent_id` (blank → nil). `GroupItemAdd` reads `product_id` (int64) + `emoji`. All mutations end with `htmxDone`/`htmxReload` triggering a `reload-groups` event; the tree and items panels listen for it.

- [ ] **Step 5: Add routes** in `internal/web/web.go` admin group (`ag`), next to the products routes:

```go
ag.GET("/groups", admin.Groups)
ag.GET("/groups/tree", admin.GroupsTree)
ag.GET("/groups/form", admin.GroupForm)
ag.GET("/groups/form/:id", admin.GroupForm)
ag.POST("/groups", admin.GroupCreate)
ag.PUT("/groups/:id", admin.GroupUpdate)
ag.DELETE("/groups/:id", admin.GroupDelete)
ag.POST("/groups/:id/move", admin.GroupMove)
ag.GET("/groups/:id/items", admin.GroupItems)
ag.POST("/groups/:id/items", admin.GroupItemAdd)
ag.DELETE("/groups/:id/items/:productId", admin.GroupItemRemove)
ag.PUT("/groups/:id/items/:productId/emoji", admin.GroupItemEmoji)
```

- [ ] **Step 6: Generate, build, vet**

Run: `make templ && go build ./... && go vet ./internal/web/`
Expected: clean.

- [ ] **Step 7: E2E — manage groups**

With `make watch` running, via Playwright (admin login): open `/admin/groups`; create "Fruits & Veg" with emoji 🥬; add subgroups "Fruits" and "Vegetables"; open "Fruits", add 2 products via the picker, set an emoji 🍎 on one; reorder a sibling with ▲/▼. Verify with psql:

```bash
docker exec -i pos_db psql -U pos_user -d pos_db -c "SELECT id,name,emoji,parent_id,sort_order FROM product_groups ORDER BY sort_order,id;"
docker exec -i pos_db psql -U pos_user -d pos_db -c "SELECT group_id,product_id,emoji FROM product_group_items;"
```
Expected: rows match what was entered (nested parent_id, emojis present, links created).

- [ ] **Step 8: Commit**

```bash
git add internal/web/admin_groups.go internal/web/web.go internal/web/server.go templates/pages/admin/groups.templ templates/layouts/admin.templ
git commit -m "feat(groups): admin Cashier Menu page (tree, CRUD, link products, emoji, reorder)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 4: Cashier read API + till group navigation

**Files:**
- Create: `internal/web/cashier_groups.go` (JSON handlers)
- Modify: `internal/web/web.go` (cashier API routes under the same group as `/api/products`)
- Modify: `templates/pages/cashier/pos.templ` (group-card view in the catalogue area)
- Modify: `static/js/app.js` (navigation state + loaders)

**Interfaces:**
- Consumes: `productgroups.Service` (Task 2); the existing till JSON envelope (find with `grep -n "func.*DefaultGrid\|response.OK\|/api/products" internal/features/products/api.go`).
- Produces: `GET /api/groups` → `{ "data": { "groups": [...] } }`; `GET /api/groups/:id` → `{ "data": { "group": {...}, "breadcrumb": [...], "children": [...], "products": [...] } }`. Product entries: `{ id, name, selling_price, unit_abbr, barcode, emoji }`.

- [ ] **Step 1: Write `internal/web/cashier_groups.go`**

```go
package web

import (
	"strconv"

	"karots-pos/internal/apperr"
	"karots-pos/internal/response"

	"github.com/labstack/echo/v4"
)

// GroupsTop returns the top-level menu groups for the till's default view.
func (s *Server) GroupsTop(c echo.Context) error {
	groups, err := s.groups.Children(c.Request().Context(), nil)
	if err != nil {
		return err
	}
	return response.OK(c, map[string]any{"groups": groups})
}

// GroupView returns one group's subgroups + linked products + breadcrumb.
func (s *Server) GroupView(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	g, err := s.groups.Get(ctx, id)
	if err != nil {
		return err
	}
	children, err := s.groups.Children(ctx, &id)
	if err != nil {
		return err
	}
	products, err := s.groups.Products(ctx, id)
	if err != nil {
		return err
	}
	crumb, err := s.groups.Breadcrumb(ctx, id)
	if err != nil {
		return err
	}
	return response.OK(c, map[string]any{
		"group": g, "breadcrumb": crumb, "children": children, "products": products,
	})
}
```

Note: adjust the receiver (`*Server` vs the actual type that owns `/api/products`) and `s.groups` access to match how cashier API handlers reach services in this codebase (grep first).

- [ ] **Step 2: Add routes** next to `/api/products` (same auth group so any till user can read):

```go
g.GET("/groups", <recv>.GroupsTop)
g.GET("/groups/:id", <recv>.GroupView)
```

- [ ] **Step 3: Add Alpine state & loaders** in `static/js/app.js`. Replace `loadDefaultGrid` usage. Add to the POS component data: `groupStack: []`, `groupChildren: []`, `inGroups: true`. Implement:

```js
async loadProducts() {
  // Search overrides groups: typing shows flat product results.
  if (this.search && this.search.trim()) {
    this.inGroups = false;
    const q = encodeURIComponent(this.search);
    const json = await apiFetch("GET", `/api/products?limit=100&search=${q}`);
    this.products = json.data || [];
    this.groupChildren = [];
    return;
  }
  // No search: show the group level we're on (or top level).
  this.inGroups = true;
  const top = this.groupStack[this.groupStack.length - 1];
  if (!top) return this.loadGroupsTop();
  return this.openGroup(top.id, true);
},
async loadGroupsTop() {
  this.groupStack = [];
  const json = await apiFetch("GET", "/api/groups");
  this.groupChildren = (json.data && json.data.groups) || [];
  this.products = [];
},
async openGroup(id, reload) {
  const json = await apiFetch("GET", `/api/groups/${id}`);
  const d = json.data || {};
  if (!reload) this.groupStack = (d.breadcrumb || []).map(g => ({ id: g.ID, name: g.Name, emoji: g.Emoji }));
  this.groupChildren = d.children || [];
  this.products = (d.products || []).map(p => ({
    id: p.product_id, name: p.name, selling_price: p.selling_price,
    unit_abbr: p.unit_abbr, barcode: p.barcode, emoji: p.emoji,
  }));
},
backGroup() {
  this.groupStack.pop();
  const top = this.groupStack[this.groupStack.length - 1];
  if (top) return this.openGroup(top.id, true);
  return this.loadGroupsTop();
},
```

(Confirm the JSON field casing returned by Go — structs serialize exported fields as-is unless json tags exist. If `Group` serialises as `ID/Name/Emoji`, map accordingly; if you prefer lowercase, add json tags to `Group`/`GroupProduct` in Task 2. **Decision: add json tags** to `GroupProduct` so product fields are `id,name,selling_price,unit_abbr,barcode,emoji`; keep `Group` fields mapped in JS as shown.)

- [ ] **Step 4: Render group cards** in `templates/pages/cashier/pos.templ`. In the catalogue area, above the existing product grid, add a group strip + Back, shown when `inGroups`:

```html
<div x-show="inGroups" class="mb-3 flex items-center gap-2">
  <button x-show="groupStack.length" type="button" x-on:click="backGroup()"
    class="px-3 py-1.5 rounded-lg border text-sm">← Back</button>
  <span class="text-sm text-slate-500" x-text="groupStack.map(g => (g.emoji?g.emoji+' ':'')+g.name).join(' / ')"></span>
</div>
<div x-show="inGroups && groupChildren.length" class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 gap-3 mb-3">
  <template x-for="g in groupChildren" :key="g.ID">
    <button type="button" x-on:click="openGroup(g.ID)"
      class="p-4 rounded-xl border bg-white hover:bg-slate-50 text-left">
      <div class="text-2xl" x-text="g.Emoji || '📁'"></div>
      <div class="font-medium" x-text="g.Name"></div>
    </button>
  </template>
</div>
```

The existing product-card grid stays as-is below (it already iterates `products`), so linked products and search results both render there. Add `x-text="p.emoji"` to the product card if you want the per-link icon shown.

- [ ] **Step 5: Initialise** — ensure the POS component calls `loadProducts()` on init (it already does for the default grid). With empty search this now loads top-level groups.

- [ ] **Step 6: Generate, build, vet**

Run: `make templ && go build ./... && go vet ./internal/web/`
Expected: clean. (`/api/products/default` still exists; removed in Task 5.)

- [ ] **Step 7: E2E — till navigation**

Via Playwright (cashier or admin → Open Cashier): default view shows top-level group cards (created in Task 3). Tap "Fruits & Veg" → shows "Fruits"/"Vegetables" subgroup cards + Back + breadcrumb. Tap "Fruits" → shows its linked products (with emoji). Tap a product → it adds to the cart. Type in search → flat product results, Back/cards hidden. Clear search → returns to the group level. Verify via `browser_evaluate` that `groupChildren`/`products`/`groupStack` are correct at each step.

- [ ] **Step 8: Commit**

```bash
git add internal/web/cashier_groups.go internal/web/web.go templates/pages/cashier/pos.templ static/js/app.js
git commit -m "feat(groups): till group-card navigation (/api/groups, Back, breadcrumb)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 5: Remove product pinning entirely

**Files:**
- Create: `migrations/0041_drop_is_pinned.sql`
- Modify: `internal/features/products/products.go` (struct field, CreateInput/UpdateInput/ImportRow fields, INSERT/UPDATE SQL, remove `DefaultGrid` repo method)
- Modify: `internal/features/products/service.go` (remove `DefaultGrid`, drop `IsPinned` mapping in writeProduct/import)
- Modify: `internal/features/products/api.go` (remove `DefaultGrid` handler + `GET /default` route)
- Modify: `templates/fragments/admin/products.templ` (remove "Pin to cashier" checkbox)
- Modify: `static/js/app.js` (remove `loadDefaultGrid` + `/api/products/default` call if any remain)

**Interfaces:**
- Consumes: nothing new.
- Produces: `products.is_pinned` and all references gone; build still green because Task 4 already replaced the till default path.

- [ ] **Step 1: Write the migration**

```sql
-- +goose Up
ALTER TABLE products DROP COLUMN is_pinned;
-- +goose Down
ALTER TABLE products ADD COLUMN is_pinned BOOLEAN NOT NULL DEFAULT false;
```

- [ ] **Step 2: Remove Go references** — exact sites (from grep at plan time):
  - `products.go`: delete the `IsPinned` field on `Product` (line ~33); delete `IsPinned` on `CreateInput`/`UpdateInput` (~68-69); delete `IsPinned` on `ImportRow` (~206); remove `is_pinned` from the INSERT column list + value (~219,225) and the UPDATE set + arg (~234,238); delete the whole `DefaultGrid` repo method (~136-153).
  - `service.go`: delete the `DefaultGrid` service method (~42-46); remove `IsPinned: in.IsPinned` in the create/import mapping (~356).
  - `api.go`: delete the `DefaultGrid` handler (~32-37) and the `g.GET("/default", api.DefaultGrid)` route (~125).

- [ ] **Step 3: Remove the form checkbox** in `templates/fragments/admin/products.templ` — delete the "Pin to cashier (show first on the till)" label/checkbox block (the `is_pinned` input).

- [ ] **Step 4: Clean app.js** — remove the `loadDefaultGrid` method and any remaining `/api/products/default` reference (Task 4 already rerouted `loadProducts`).

- [ ] **Step 5: Apply migration, generate, build, vet**

Run: `make migrate && make templ && go build ./... && go vet ./...`
Expected: clean; `grep -rn "is_pinned\|IsPinned\|DefaultGrid\|products/default" internal/ templates/ static/` returns nothing.

- [ ] **Step 6: E2E — pinning gone**

Open `/admin/products` → New Product: the "Pin to cashier" checkbox is absent; create/edit still works. Till default view still shows groups (unaffected). `curl -s -o /dev/null -w "%{http_code}" http://localhost:3000/api/products/default` → 404.

- [ ] **Step 7: Commit**

```bash
git add migrations/0041_drop_is_pinned.sql internal/features/products/ templates/fragments/admin/products.templ static/js/app.js
git commit -m "feat(groups): remove product pinning (column, form, default-grid path)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 6: Final end-to-end verification & polish

**Files:** none (verification), or small fixes uncovered.

- [ ] **Step 1: Full build/vet/test**

Run: `make templ && go build ./... && go vet ./... && go test ./...`
Expected: all green.

- [ ] **Step 2: Full E2E pass** (Playwright + psql), covering the spec's Testing section end to end:
  - Admin: create a 2-level tree with emojis, link products (mixed: a parent group with both a subgroup and a direct product), set per-link emojis, reorder, delete a subgroup (cascade — products survive).
  - Till: default = top-level cards; drill through mixed group (subgroup cards then product cards); Back up the full path; tap product → cart; search flat results + Back hidden; clear search restores groups; an unlinked product is reachable only via search.
  - Confirm pinning fully gone (Task 5 checks).

- [ ] **Step 3: make css (if any new classes)**

Run: `make css` (leaves `static/css/tailwind.css` unstaged). Restart not needed under `make watch`.

- [ ] **Step 4: Update memory** — add/refresh a memory note summarising the groups feature (tables, admin page, till nav, pinning removed) and update `MEMORY.md`.

- [ ] **Step 5: Final commit (docs/memory only if changed)**

```bash
git add docs/superpowers/plans/2026-06-28-cashier-group-cards.md
git commit -m "docs(groups): implementation plan

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage:** schema (T1), package (T2), admin management + permissions admin-only (T3), till read API + navigation + search-unchanged (T4), pinning removal (T5), mixed content + arbitrary depth (T2 queries + T4 rendering), edge cases (T6 E2E). All spec sections map to a task.

**Placeholders:** none — code shown for migrations, package, handlers (shape + the non-obvious helpers), JS, templ snippets. Where templ mirrors existing files, the new markup is given; boilerplate handlers reference the exact existing pattern to copy and list every method to implement.

**Type consistency:** `Group{ID,Name,Emoji,ParentID,SortOrder,IsActive,HasChildren,ItemCount}` and `GroupProduct{ProductID,Name,SellingPrice,UnitAbbr,Barcode,Emoji,SortOrder}` used consistently across T2/T3/T4; service method names (`Children/Get/Products/Tree/Create/Update/Delete/LinkProduct/UnlinkProduct/SetItemEmoji/Move/Breadcrumb`) match between definition (T2) and callers (T3/T4). JSON shape note in T4 Step 3 reconciles Go field casing (add json tags to `GroupProduct`).

**Open implementation detail (resolve at execution):** the exact server struct/receiver that owns cashier `/api/*` handlers and how services are injected — grep first (T3 Step 1, T4 Step 1) and follow the existing pattern rather than guessing.
