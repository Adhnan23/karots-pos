# Modernize the Categories management page

**Date:** 2026-07-22
**Status:** Approved (design)

## Problem

The admin **Categories** page (`/admin/categories`) is a plain bordered table.
Adding a child category means opening a modal and choosing its parent from a
dropdown — clumsy next to the product/intake **category picker**, which already
lets you type a new subcategory inline with a ＋ and see it slot into the tree.
The management page "looks outdated" and lacks that inline create.

## Goals

- Refresh the page to a clean nested tree that matches the rest of the app.
- Add an inline **＋ add subcategory** on every row (no modal, no parent
  dropdown) and a **＋ Add top-level category** at the bottom.
- Reuse the existing quick-create endpoint so a repeated name never duplicates.
- Keep the existing expand/collapse, Edit (rename + move), and Delete unchanged.

## Non-goals (YAGNI)

- No inline rename (Edit modal still renames).
- No drag-to-reparent (Edit modal still moves a category).
- No new server routes — quick-create, the table fragment, the edit modal, and
  delete all already exist.
- No change to delete behaviour.

## Approach

Same page, same `categoryTree()` collapse model, same endpoints. The table
becomes a div-based nested list, and the existing Alpine component grows an
inline-create mode.

### Layout (refreshed tree rows)

Replace the `<table>` with a nested list inside the app's rounded card
(`bg-white rounded-2xl shadow-sm`). Each row:

- a chevron **▾/▸** that toggles expand for parents; leaves get a flat indent
  spacer of the same width;
- an indent proportional to depth (reuse the existing `indent(Depth)` scheme);
- the category name;
- a small muted **direct-child count** next to collapsed parents (e.g.
  "Stationery (4)");
- three right-aligned action controls that sit quiet and lift on row hover:
  **＋** (add subcategory), **✎** Edit (existing `/admin/categories/form/:id`
  modal), **🗑** Delete (existing `hx-delete` with confirm).

The container keeps the current HTMX wiring: `x-data="categoryTree()"`,
`hx-get="/admin/categories/table"`,
`hx-trigger="reload-categories from:body"`, `hx-swap="innerHTML"`. Because the
component lives on the container and only its children are swapped, the
`expanded` state survives a reload.

### Inline add-child

Clicking **＋** on a row switches that row into create mode: an inline text
input appears indented one level beneath it, with ✓/Enter to submit and Esc/✕
to cancel. Submit POSTs to the existing `POST /admin/categories/quick` with
`name` and `parent_id=<row id>` (the same `FindOrCreateByPath` the picker uses,
so a duplicate name selects the existing child instead of creating a second).
On success:

1. mark the parent expanded (`expanded[parentId] = true`) so the new child shows;
2. dispatch `reload-categories` on `body` — the container re-fetches
   `/admin/categories/table` and re-renders the tree with the child in place.

A **＋ Add top-level category** row at the bottom does the same with no
`parent_id`.

Only one create input is open at a time (opening another, or Esc, closes the
prior). Errors from the endpoint render inline beside the input.

### State (extends `categoryTree()` in static/js/app.js)

Add to the existing component:

- `creatingFor` — the id of the row whose inline input is open, or `"top"` for
  the top-level input, or `null`.
- `newName`, `createBusy`, `createError`.
- `startCreate(id)`, `cancelCreate()`, `submitCreate(parentId)` — the last
  mirrors `categoryPicker.submitCreate` but, instead of splicing options
  client-side, sets `expanded[parentId]=true` and dispatches `reload-categories`.

### Data (extends TreeNode)

`TreeNode` gains `ChildCount int`. `Tree()` already builds a `children`
map in Go, so this is `len(children[c.ID])` at walk time — **no SQL change**.
Orphans appended at the end get `ChildCount: 0`.

## Files touched

- `internal/features/categories/categories.go` — `TreeNode.ChildCount` + set it
  in `Tree()`'s `walk`.
- `templates/pages/admin/categories.templ` — `CategoriesPage` + `CategoryRows`
  rewritten as the div-based refreshed tree with inline-add and top-level-add.
- `static/js/app.js` — extend `categoryTree()` with inline-create state and
  `startCreate`/`cancelCreate`/`submitCreate`.

## Error handling

- Empty name → inline "Enter a category name." (client guard), no POST.
- Endpoint error (e.g. invalid parent) → its message rendered inline beside the
  input; the tree is not reloaded.
- Duplicate name under the same parent → `FindOrCreateByPath` returns the
  existing id; the reload simply shows the already-present child (no error, no
  duplicate).

## Testing

- **JS:** `node --check static/js/app.js` after editing the component.
- **Build:** `templ generate && go build ./...` clean.
- **Live click-through** against the dev server:
  - the page renders as the nested tree with chevrons, counts, and hover
    actions;
  - **＋** on a parent opens an inline input; submitting a name adds the child
    nested under that parent, and the parent is expanded to show it;
  - **＋ Add top-level category** adds a root;
  - submitting the same child name twice does not create a duplicate;
  - expand/collapse state persists across the post-create reload;
  - Edit (rename/move) and Delete still work.
- Restore any categories created during the live check so the dev catalog
  returns to baseline.
