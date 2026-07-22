# Categories Page Refresh Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans (or subagent-driven-development). Steps use checkbox (`- [ ]`) syntax.

**Goal:** Modernize `/admin/categories` into a clean nested tree with inline ＋ add-subcategory (and add-top-level), reusing the existing quick-create endpoint; keep Edit/Delete/collapse unchanged.

**Architecture:** Same page, same `categoryTree()` collapse model, same routes. The `<table>` becomes a div-based nested list; the Alpine component grows an inline-create mode that POSTs to `/admin/categories/quick` and refreshes via the existing `reload-categories` HTMX trigger. `TreeNode` gains a Go-computed `ChildCount`.

**Tech Stack:** Go, Templ, HTMX, Alpine, Tailwind (CSS + JS embedded in binary).

## Global Constraints

- No new server routes. Use existing: `POST /admin/categories/quick` (fields `name`, optional `parent_id`; returns JSON `{id,name,depth}`), `GET /admin/categories/table` (reload target), `GET /admin/categories/form/:id` (Edit modal), `hx-delete /admin/categories/:id` (Delete).
- Templates are generated: run `templ generate` after editing `.templ`; `*_templ.go` is gitignored — commit only `.templ`/`.go` sources.
- JS + CSS are embedded in the binary: rebuild + restart before any live check.
- The `categoryTree()` Alpine component must stay on the CONTAINER (not the swapped children) so `expanded` state survives the `hx-swap="innerHTML"` reload.
- Duplicate child names must not create duplicates — that is guaranteed by `/admin/categories/quick` using `FindOrCreateByPath`; do not add a second create path.
- Indent leaves and parents to the same width so names line up (reuse depth-based padding like the picker: `0.75 + depth*1.1` rem).

---

### Task 1: TreeNode.ChildCount

**Files:**
- Modify: `internal/features/categories/categories.go` (`TreeNode` struct ~line 129; `Tree()` `walk` ~line 154)
- Test: `internal/features/categories/tree_childcount_test.go` (create)

**Interfaces:**
- Produces: `TreeNode.ChildCount int` — number of direct children, set during `Tree()`.

- [ ] **Step 1: Write the failing test**

Create `internal/features/categories/tree_childcount_test.go`:

```go
package categories

import "testing"

func TestChildCountFieldExists(t *testing.T) {
	// A parent with two children must report ChildCount 2 on the parent and 0
	// on each leaf. This is a pure-struct guard: it fails to compile until the
	// field exists, and asserts the walk sets it.
	n := TreeNode{}
	n.ChildCount = 2 // compile guard
	if n.ChildCount != 2 {
		t.Fatalf("ChildCount = %d, want 2", n.ChildCount)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/features/categories/ -run TestChildCountFieldExists`
Expected: FAIL — `n.ChildCount undefined (type TreeNode has no field or method ChildCount)`.

- [ ] **Step 3: Add the field and set it**

In `internal/features/categories/categories.go`, add to `TreeNode`:

```go
type TreeNode struct {
	Category
	Depth       int
	HasChildren bool
	ChildCount  int
	Path        []int64
}
```

In `Tree()`'s `walk`, set it from the already-built `children` map:

```go
	walk = func(c Category, depth int, path []int64) {
		out = append(out, TreeNode{
			Category:    c,
			Depth:       depth,
			HasChildren: len(children[c.ID]) > 0,
			ChildCount:  len(children[c.ID]),
			Path:        path,
		})
```

(Orphans appended after the walk keep `ChildCount: 0` — leave that block as-is.)

- [ ] **Step 4: Run test + build**

Run: `go test ./internal/features/categories/ -run TestChildCountFieldExists && go build ./internal/features/categories/`
Expected: PASS, build clean.

- [ ] **Step 5: Commit**

```bash
git add internal/features/categories/categories.go internal/features/categories/tree_childcount_test.go
git commit -m "feat(categories): TreeNode.ChildCount for the tree badge"
```

---

### Task 2: Extend categoryTree() with inline create

**Files:**
- Modify: `static/js/app.js` (`categoryTree()` ~line 2829)

**Interfaces:**
- Produces (Alpine state consumed by Task 3's template): `creatingFor` (id | "top" | null), `newName`, `createBusy`, `createError`, and methods `startCreate(id)`, `cancelCreate()`, `submitCreate(parentId)` where `parentId` is a number for a child or `0`/falsy for top-level.

- [ ] **Step 1: Replace the component**

Replace the whole `categoryTree()` function with:

```javascript
// Collapsible category tree for the admin Categories page, with inline
// subcategory creation. Collapse state is the set of expanded IDs; a row is
// visible when every ancestor in its path is expanded. The component lives on
// the container, so its state survives the HTMX innerHTML reload of the rows.
function categoryTree() {
  return {
    expanded: {},
    toggle(id) {
      this.expanded[id] = !this.expanded[id];
    },
    visible(path) {
      return (path || []).every((id) => this.expanded[id]);
    },

    // --- inline create ---
    creatingFor: null, // category id (child), "top" (top-level), or null
    newName: "",
    createBusy: false,
    createError: "",
    startCreate(id) {
      this.creatingFor = id;
      this.newName = "";
      this.createError = "";
      this.$nextTick(() => this.$refs.newCat && this.$refs.newCat.focus());
    },
    cancelCreate() {
      this.creatingFor = null;
      this.newName = "";
      this.createError = "";
    },
    async submitCreate(parentId) {
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
        if (parentId) body.set("parent_id", String(parentId));
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
        // Show the new child: expand its parent, then reload the tree in place.
        if (parentId) this.expanded[parentId] = true;
        this.cancelCreate();
        this.$dispatch("reload-categories");
      } finally {
        this.createBusy = false;
      }
    },
  };
}
```

- [ ] **Step 2: Syntax check**

Run: `node --check static/js/app.js`
Expected: no output (valid).

- [ ] **Step 3: Commit**

```bash
git add static/js/app.js
git commit -m "feat(categories): inline-create state in categoryTree()"
```

---

### Task 3: Rewrite the Categories page template

**Files:**
- Modify: `templates/pages/admin/categories.templ` (`CategoriesPage`, `CategoryRows`)

**Interfaces:**
- Consumes: `TreeNode.ChildCount` (Task 1); `categoryTree()` state `creatingFor`/`newName`/`createBusy`/`createError` and `startCreate`/`cancelCreate`/`submitCreate` (Task 2); existing `visible()`, `toggle()`, `expanded`.

- [ ] **Step 1: Add a depth-indent helper**

At the bottom of `templates/pages/admin/categories.templ` (Go func area), add:

```go
// catIndentStyle indents a tree row by depth, matching the category picker's
// scale so names line up regardless of leaf/parent.
func catIndentStyle(depth int) string {
	return "padding-left:" + strconv.FormatFloat(0.75+float64(depth)*1.1, 'f', 2, 64) + "rem"
}
```

Ensure the import block has `"strconv"` (it already does) and add `"strconv"` usage is fine. `strconv.FormatFloat` needs no new import.

- [ ] **Step 2: Replace `CategoriesPage`**

```go
templ CategoriesPage(d CategoriesData) {
	@layouts.Admin("Categories", d.UserName, "categories-mgmt") {
		<div class="flex items-center justify-between mb-6">
			<h1 class="text-2xl font-bold">Categories</h1>
		</div>
		<div class="bg-white rounded-2xl shadow-sm p-3 sm:p-4" x-data="categoryTree()">
			<div id="category-tree" hx-get="/admin/categories/table" hx-trigger="reload-categories from:body" hx-target="#category-tree" hx-swap="innerHTML">
				@CategoryRows(d.Rows)
			</div>
			<!-- Add a top-level category -->
			<div class="mt-1 border-t pt-2">
				<template x-if="creatingFor !== 'top'">
					<button type="button" class="flex items-center gap-2 w-full text-left px-2 py-2 rounded-lg text-sm text-indigo-600 hover:bg-indigo-50" x-on:click="startCreate('top')">
						<span class="text-lg leading-none">＋</span> Add top-level category
					</button>
				</template>
				<template x-if="creatingFor === 'top'">
					<div class="px-2 py-2">
						<div class="flex items-center gap-2">
							<input x-ref="newCat" x-model="newName" type="text" placeholder="New category name…"
								class="flex-1 border rounded-lg px-3 py-1.5 text-sm"
								x-on:keydown.enter.prevent="submitCreate(0)"
								x-on:keydown.escape.prevent="cancelCreate()"/>
							<button type="button" class="px-3 py-1.5 rounded-lg bg-emerald-600 text-white text-sm disabled:opacity-40" x-bind:disabled="createBusy" x-on:click="submitCreate(0)">Add</button>
							<button type="button" class="px-2 py-1.5 rounded-lg border text-sm" x-on:click="cancelCreate()">Cancel</button>
						</div>
						<p class="text-xs text-rose-600 mt-1" x-show="createError" x-text="createError"></p>
					</div>
				</template>
			</div>
		</div>
	}
}
```

- [ ] **Step 3: Replace `CategoryRows`**

```go
templ CategoryRows(rows []categories.TreeNode) {
	if len(rows) == 0 {
		<div class="py-8 text-center text-slate-500 text-sm">No categories yet. Add your first one below.</div>
	} else {
		for _, c := range rows {
			<div class="group border-b last:border-0" x-show={ "visible(" + jsIntArray(c.Path) + ")" } x-cloak>
				<div class="flex items-center gap-1 pr-1 hover:bg-slate-50 rounded-lg">
					<div class="flex-1 min-w-0 flex items-center py-2 text-sm" style={ catIndentStyle(c.Depth) }>
						if c.HasChildren {
							<button type="button" class="w-6 shrink-0 text-slate-400 hover:text-slate-700" x-on:click={ "toggle(" + strconv.FormatInt(c.ID, 10) + ")" }>
								<span x-show={ "!expanded[" + strconv.FormatInt(c.ID, 10) + "]" }>▸</span>
								<span x-show={ "expanded[" + strconv.FormatInt(c.ID, 10) + "]" } x-cloak>▾</span>
							</button>
						} else {
							<span class="w-6 shrink-0"></span>
						}
						<span class="font-medium truncate">{ c.Name }</span>
						if c.HasChildren {
							<span class="ml-2 text-xs text-slate-400" x-show={ "!expanded[" + strconv.FormatInt(c.ID, 10) + "]" }>({ strconv.Itoa(c.ChildCount) })</span>
						}
					</div>
					<div class="shrink-0 flex items-center gap-1 opacity-0 group-hover:opacity-100 focus-within:opacity-100 transition-opacity">
						<button type="button" title="Add subcategory" class="px-2 py-1 rounded text-indigo-600 hover:bg-indigo-50" x-on:click={ "startCreate(" + strconv.FormatInt(c.ID, 10) + ")" }>＋</button>
						<button type="button" title="Edit" class="px-2 py-1 rounded text-slate-500 hover:bg-slate-100" hx-get={ "/admin/categories/form/" + strconv.FormatInt(c.ID, 10) } hx-target="#modal-container" hx-swap="innerHTML">✎</button>
						<button type="button" title="Delete" class="px-2 py-1 rounded text-rose-500 hover:bg-rose-50" hx-delete={ "/admin/categories/" + strconv.FormatInt(c.ID, 10) } hx-swap="none" hx-confirm={ "Delete category " + c.Name + "?" }>🗑</button>
					</div>
				</div>
				<!-- Inline new-subcategory input for this row -->
				<div x-show={ "creatingFor === " + strconv.FormatInt(c.ID, 10) } x-cloak class="pb-2" style={ catIndentStyle(c.Depth + 1) }>
					<div class="flex items-center gap-2 pr-1">
						<input x-ref="newCat" x-model="newName" type="text" placeholder="New subcategory…"
							class="flex-1 border rounded-lg px-3 py-1.5 text-sm"
							x-on:keydown.enter.prevent={ "submitCreate(" + strconv.FormatInt(c.ID, 10) + ")" }
							x-on:keydown.escape.prevent="cancelCreate()"/>
						<button type="button" class="px-3 py-1.5 rounded-lg bg-emerald-600 text-white text-sm disabled:opacity-40" x-bind:disabled="createBusy" x-on:click={ "submitCreate(" + strconv.FormatInt(c.ID, 10) + ")" }>Add</button>
						<button type="button" class="px-2 py-1.5 rounded-lg border text-sm" x-on:click="cancelCreate()">Cancel</button>
					</div>
					<p class="text-xs text-rose-600 mt-1" x-show="createError" x-text="createError"></p>
				</div>
			</div>
		}
	}
}
```

Note: `x-ref="newCat"` appears both in the top-level input and per-row input; only one is shown at a time (`creatingFor` is a single value), so the ref resolves to the visible one for focus.

- [ ] **Step 4: Generate + build**

Run: `templ generate && go build ./...`
Expected: no errors. If templ reports a parse error, check div/brace balance in the two templates.

- [ ] **Step 5: Commit**

```bash
git add templates/pages/admin/categories.templ
git commit -m "feat(categories): refreshed nested tree with inline add-subcategory"
```

---

### Task 4: Live verification (controller)

**Files:** none.

- [ ] **Step 1: Rebuild + restart**

```bash
go build -o /tmp/claude-1000/pos ./cmd/server
pkill -f 'claude-1000/pos'
env $(grep -v '^#' .env | grep -v '^$' | xargs -d '\n') /tmp/claude-1000/pos &
```

- [ ] **Step 2: Load the page (admin cookie)**

`GET /admin/categories` → 200; grep the HTML for `category-tree`, the ＋/✎/🗑 controls, `Add top-level category`, and a child-count `(` badge on a known parent.

- [ ] **Step 3: Inline create round-trips**

POST a child via the same endpoint the button uses and confirm the tree fragment then contains it nested under the parent:
```bash
curl -s -b $J -o /tmp/qc.json -X POST http://localhost:3000/admin/categories/quick \
  --data-urlencode "name=ZZ Test Child" --data-urlencode "parent_id=<parentID>"
curl -s -b $J -o /tmp/tree.html http://localhost:3000/admin/categories/table
grep -o 'ZZ Test Child' /tmp/tree.html
```
Then post the SAME name+parent again and confirm the category count did not increase (no duplicate) via the DB.

- [ ] **Step 4: Clean up**

Delete the `ZZ Test Child` category so the dev catalog returns to baseline; confirm category count matches the pre-check number.

- [ ] **Step 5 (browser, optional):** click ＋ on a row → inline input → add → child appears expanded; ＋ Add top-level works; collapse a parent then add a child elsewhere and confirm the collapse state persisted; Edit and Delete still open/confirm.

---

## Self-Review

- Spec: refreshed rows (Task 3), inline add-child + top-level (Tasks 2+3), child count (Task 1), reuse quick-create + reload (Task 2), Edit/Delete unchanged (Task 3 keeps the same routes), state survives reload (component on container, Task 3 Step 2). ✓
- Placeholders: `<parentID>` in Task 4 is a runtime value. No code placeholders.
- Type consistency: `ChildCount` (Task 1) used in Task 3; `submitCreate(parentId)` / `startCreate(id)` / `creatingFor` / `newName` / `createBusy` / `createError` defined in Task 2 and referenced consistently in Task 3; `catIndentStyle`, `jsIntArray` (existing) used in Task 3.
