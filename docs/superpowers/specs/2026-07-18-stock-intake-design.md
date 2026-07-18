# Stock Intake — design

## Problem

Bringing new stock into the shop currently takes three separate admin pages per
item:

1. **Products → Add product** — creates the product, but has *no* quantity field.
2. **Inventory → Stock-take** — set the on-hand quantity (`stock.Adjust`).
3. **Inventory → Barcode Labels** — generate/print a barcode sticker.

For a single item that is fine. During a real stock-addition session (dozens of
items) the page-hopping is tedious. The owner wants one page that fuses
**add/restock → set quantity → print labels**, shown only while stock-taking is
enabled (same toggle as Stock-take), and the Settings toggle reworded to mention
it.

## Goals

- One page, `/admin/inventory/intake`, that per item lets you: create a new
  product *or* restock an existing one, set the quantity, and print barcode
  labels — without leaving the page.
- Fast repeat entry: after each save the form resets with focus back on the
  search box, and the item joins an "Added this session" list with a Reprint
  button.
- Gate it behind the existing stock-take toggle: 403 when off, and the nav card
  hidden when off (same as the Stock-take card).
- Reword the Settings toggle to mention both pages.

## Non-goals (YAGNI)

- **No multi-row batch grid.** The one-at-a-time form + session list already
  delivers the speed and the review; true bulk creation is already served by
  CSV/XLSX import and the Flutter capture app. A grid would duplicate that and
  need per-row pickers/barcode-gen for marginal gain.
- No new stock-movement type or receiving-document model — reuse `stock.Adjust`.
- No changes to the normal Products add/edit form.

## Interaction

Single page, one item at a time, driven by an Alpine component `intake()`. Two
modes flow off one search box:

**Top — Item search** (reuse `adminfragments.ProductPicker` → `/api/products`).

- **Pick an existing product → Restock mode.** Show the product name and its
  current on-hand quantity. Fields:
  - **Qty to add** (numeric, required, > 0).
  - If the product has **no barcode**, show the **Generate & save barcode**
    action (the component added in commit 8d7a8d0: `POST /api/products/:id/barcode`,
    `SetBarcodeIfEmpty`). If it already has one, no prompt.
  - **Print labels** checkbox (on by default); **Number of labels** defaults to
    "Qty to add" (editable); sticker-size picker.
  - Button: **"Add stock & print labels"**.

- **Type a name, pick nothing → Create mode.** Minimal fields:
  - **Name** (prefilled from the search text), **Category** (`CategoryPicker`),
    **Unit** (`OptionPicker`, defaults to pcs via `unitSelectedID`), **Cost**
    (optional), **Selling price**, **Barcode** (text + Generate button),
    **Opening qty**.
  - **Print labels** checkbox (on by default); **Number of labels** defaults to
    Opening qty; sticker-size picker.
  - Button: **"Create, stock & print labels"**.

**On save** the item is prepended to the **"Added this session"** list (client
state: name · qty · barcode · Reprint button), the form resets, and focus
returns to the search box.

## Server flow

New handlers in `internal/web/intake.go`, all under the `ag` group and all
calling `requireStockTake(c)` first:

- `IntakePage` (GET `/admin/inventory/intake`) — renders the page. Needs
  categories tree, units, and the currency symbol (for label preview), mirroring
  what `ProductForm` and the labels page load.
- `IntakeCreate` (POST `/admin/inventory/intake/create`):
  1. `products.Create(CreateInput{...})` from the minimal fields (barcode carried
     if the user generated one).
  2. If opening qty > 0 → `stock.Adjust(AdjustInput{ProductID, NewQuantity: qty,
     Note: "stock intake"})`.
  3. Audit-log the create (existing `logAudit` pattern).
  4. Return JSON/fragment with the saved item (id, name, barcode, qty,
     price) so the client can (a) print and (b) add it to the session list.
- `IntakeRestock` (POST `/admin/inventory/intake/restock`):
  1. Load the product; if it has no barcode and one was generated, it was already
     saved via the existing `/api/products/:id/barcode` call from the client.
  2. `stock.Adjust` to **current on-hand + qty added** (absolute set; the handler
     re-reads current on-hand at save time).
  3. Audit-log; return the item payload.

**Printing** reuses the existing `POST /admin/labels/send` (`sendLabel` /
`parseLabelReq` — takes name/code/price/show_price/qty/size from the form). The
client, after a successful save, fires a second request to `/admin/labels/send`
when the Print-labels box is ticked. This mirrors how the labels page works
today.

Save and print are therefore **two requests** (non-atomic): if the print fails
the item is still saved and the session-list **Reprint** covers it. This matches
the app's existing chained-operation trade-offs. One sticker per unit falls out
naturally because the label count defaults to the quantity.

## Gating & settings

- All intake routes call `requireStockTake` → 403 when the feature is off.
- Nav: add an Inventory link `{"/admin/inventory/intake", "Stock Intake",
  "intake", "Add or restock items, set quantity, and print labels in one place"}`
  in `templates/layouts/admin.templ`.
- `sectionHub` (`internal/web/admin.go`) currently strips only
  `/admin/stock/take` when stock-take is off. Generalise its filter to also strip
  `/admin/inventory/intake` (e.g. a small set of gated hrefs), so the Intake card
  hides with the toggle.
- Settings label (`templates/pages/admin/settings.templ:117`) reworded to:
  **"Enable stock-take & intake — when off, the Stock-take and Stock Intake pages
  are hidden and their links stop working (they can rewrite quantities &
  prices)."**

## Reused vs new

**Reused:** `ProductPicker`, `CategoryPicker`, `OptionPicker`, `unitSelectedID`
(pcs default), the generate-and-save-barcode component + `/api/products/:id/barcode`,
`products.Create`, `stock.Adjust`, `sendLabel` via `/admin/labels/send`,
`requireStockTake`, `logAudit`, the label size picker / preview partials.

**New:** `internal/web/intake.go` (3 handlers), `templates/pages/admin/intake.templ`
(page + minimal-fields form), `intake()` Alpine component in `static/js/app.js`,
3 route registrations, the nav link, the `sectionHub` filter generalisation, and
the settings label reword.

## Testing

- **Go handler tests** for `IntakeCreate` (creates product + seeds stock) and
  `IntakeRestock` (adds to existing on-hand), plus a `requireStockTake`-off case
  returning 403.
- **Manual E2E** (server + browser):
  - Create mode: new name → category/unit(pcs)/cost/price/qty → save →
    product exists, stock = opening qty, label prints; item in session list.
  - Restock mode: pick existing → qty to add → on-hand increases by that amount;
    barcode-less product shows Generate & save; label prints.
  - Toggle stock-take off → Intake card gone from Inventory hub, and hitting the
    URL directly returns 403.
  - Settings label shows the new wording.

## Known trade-offs

- Restock uses read-then-absolute-set for the new quantity (tiny race window if
  two admins restock the same item at once) — acceptable under the app's
  single-admin intake assumption, same as other flows.
- Save+print are non-atomic; mitigated by the session-list Reprint.
