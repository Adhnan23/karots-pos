# Till Open/Close — side-by-side cash + reload float

**Date:** 2026-08-04
**Status:** Approved
**Scope:** front-end only (`templates/pages/cashier/pos.templ`)

## Problem

Both the **Open Register** overlay and the **Close Register** modal are narrow
single columns (`max-w-md`). Two things fall below the fold:

- The **reload-plugin float** section (`openSections` / `closeSections`) is stacked
  *below* the cash count, pushing everything down.
- The **denomination count list** is boxed in an `max-h-72` inner scroll, so the
  low-value coins at the bottom get lost — cashiers miss them during count.

The result: when the recharge plugin is enabled, the cashier has to scroll a long
dialog and sometimes misses coins or the float inputs.

## Goal

When a plugin float is present, show **cash on the left, reload float on the right**
so both fit without vertical scrolling. When no plugin is present, keep the current
narrow, cash-only single column. Also make all denominations (notes + coins) visible
without an inner scrollbar.

## Approach

Front-end only. `drawerSections` is already in the POS Alpine scope and is the single
source of truth for "a plugin float is present" — no server or JS-logic change needed.
Both the Open overlay (~L34) and Close modal (~L621) change identically:

1. **Conditional width.** Replace the static `max-w-md` on the modal card with
   `:class="drawerSections.length ? 'max-w-3xl' : 'max-w-md'"`.

2. **Two-column grid when a float exists.** Wrap the cash block (denomination list +
   total + locker select) and the float block (`x-ref="openSections"` /
   `x-ref="closeSections"`) in
   `<div class="grid gap-5" :class="drawerSections.length ? 'lg:grid-cols-2' : 'grid-cols-1'">`.
   - Header (title/subtitle, reopen options, logout warning) stays full-width above.
   - Primary buttons (Open Register / Cancel + Close & Reconcile) stay full-width below.
   - On phones it stacks (`grid-cols-1` until `lg`), so mobile is unaffected.

3. **No-plugin path unchanged.** Empty `drawerSections` ⇒ `max-w-md`, single cash-only
   column. The empty `x-ref` float `<div>` stays in the DOM so `loadDrawerSections` /
   `saveDrawerSections` refs keep working; it just renders nothing.

4. **Taller denom list.** Raise the inner cap from `max-h-72` to `max-h-[55vh]` in both
   dialogs so all notes and coins are visible without an inner scrollbar, while the
   "Opening cash" / "Counted" total stays pinned below the list.

## Out of scope

- Any change to `app.js` (`loadDrawerSections` / `saveDrawerSections` untouched).
- Any server, route, or plugin change.
- Denomination ordering, the reconciliation result view, deposit/withdraw dialogs.

## Verification

1. `make build` (regenerates templ + re-embeds assets) + restart the dev server.
2. Live E2E (Playwright, dev DB) with the recharge plugin enabled:
   - **Open Register:** two columns — cash left, float right; every coin visible; no
     vertical scroll on a normal viewport.
   - **Close Register:** same two-column layout; float inputs beside the count.
3. Empty-state check: set `drawerSections = []` via browser eval on the open dialog and
   confirm it collapses to the narrow (`max-w-md`) single cash-only column.
4. Regression: opening float and closing reconciliation still post correctly (float
   counts saved), since the JS refs and save logic are unchanged.
