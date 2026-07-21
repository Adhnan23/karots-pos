# Inline category creation from the category picker

**Date:** 2026-07-21
**Status:** approved, not yet implemented

## Problem

Every place that captures a product asks for a category through
`adminfragments.CategoryPicker`, which can only choose from categories that
already exist. Filing a product under a category that has not been created yet
means abandoning the half-filled form, navigating to Admin → Categories,
creating it, coming back, and starting again. That happens constantly while
capturing new stock, which is exactly when the catalogue is still growing.

## Decisions

| Question | Decision |
|---|---|
| How is nesting expressed? | **Tap ➕ on the row you want to nest under.** The parent comes from where you tapped, so no path is typed and no parent name is spelled. |
| Where does it appear? | **Creation contexts only** — Stock Intake, New Service, Product create/edit. Filter pickers are untouched. |
| Duplicate names? | **Selects the existing category** rather than erroring or duplicating. |
| New endpoint or reuse? | New thin endpoint; resolution delegates to the existing `categories.FindOrCreateByPath`. |

## Design

### Picker — `templates/fragments/admin/products.templ`

`CategoryPicker` gains an `allowCreate bool` parameter, threaded through
`categoryPickerData` into the Alpine config. Call sites:

| File | Context | allowCreate |
|---|---|---|
| `templates/pages/admin/intake.templ:72` | Stock Intake, new item | **true** |
| `templates/pages/admin/services.templ:192` | New Service | **true** |
| `templates/fragments/admin/products.templ:173` | Product create/edit | **true** |
| `templates/pages/admin/products.templ:61` | Products list filter | false |
| `templates/pages/admin/stocktake.templ:68` | Stock Take filter | false |

When `allowCreate` is on:

- Each option row gains a ➕ button on the right that opens the create panel
  with that row as parent. The row's existing select-on-click behaviour is
  unchanged — the ➕ must stop propagation so tapping it does not also pick the
  category.
- A single **➕ New top-level category** entry sits at the end of the list and
  opens the panel with no parent.
- The panel replaces the option list in place: a heading (`New under "Batteries"`
  or `New top-level category`), a text input, Create and Cancel. Escape cancels.
  Cancelling returns to the list with the previous selection intact.

On success the returned option is spliced into `options` directly after its
parent (so indentation stays coherent), selected, and the panel closes.

### Endpoint — `POST /admin/categories/quick`

Form fields `name` (required) and `parent_id` (optional). Returns
`{"id":…, "name":…, "depth":…}`.

Handler responsibilities:

- Trim `name`; reject blank with a 422 validation error.
- When `parent_id` is present, load it to confirm it exists and to compute the
  new category's depth for the picker's indentation.
- Delegate creation to `categories.FindOrCreateByPath`, building the path from
  the parent's ancestry plus the new name. That function already walks the path
  and creates only missing levels, so an existing child is returned rather than
  duplicated.
- Audit-log the creation like `CategoryCreate` does.

Mounted on the existing admin group, so it inherits admin-only authorisation.

### Why reuse FindOrCreateByPath

It is already the code path the CSV product import uses
(`internal/web/import_products.go:215`), so nested creation and the
find-or-create semantics are proven against real imports. A separate create path
would risk diverging on the duplicate rule.

## Testing

**Unit (no database):**
- Name validation: blank, whitespace-only, and leading/trailing spaces trimmed.
- The path builder: root parent, nested parent, and a name containing `>` — it
  must be treated as a literal name, not as extra path levels, since the parent
  is supplied structurally.

**Live:**
- From Stock Intake, create a category under an existing one; confirm it is
  selected, the item saves into it, and it appears at the right depth in
  Admin → Categories.
- Create a top-level category the same way.
- Create the same name twice under one parent; confirm the second attempt
  selects the existing category and no duplicate row appears.
- Confirm the two filter pickers show no ➕ affordance.
- Confirm tapping ➕ does not also select that row.

## Known limitation

A category created here appears immediately in the picker that created it, but
any other picker already rendered on the same page keeps its stale option list
until the page reloads. Accepted: the creation contexts each show one category
picker at a time.

## Out of scope

- Renaming, moving or deleting categories from the picker.
- Creating categories from the till.
- Creating units or suppliers inline (same shape of problem, separate change).
