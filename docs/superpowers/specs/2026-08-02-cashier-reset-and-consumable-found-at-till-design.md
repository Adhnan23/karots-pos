# Cashier reset button + consumable found-at-till

**Date:** 2026-08-02
**Status:** Approved (design), ready for implementation plan

Two independent till improvements, both driven by the owner:

1. A one-press **Reset to menu** that jumps the cashier catalog back to the top
   level, clears the search box, and refocuses the barcode scanner — with an
   **F1** keyboard shortcut. Navigation only; the cart is never touched.
2. Extend **found-at-till** (sell what the count says is zero, because it
   physically exists) to a **service line's consumables** — the copy-job paper
   case. Today a real product can be sold when its count is short, but a
   consumable (paper for a Print & Copy job) cannot, so a copy job is blocked
   when paper reads zero even though the paper is on the shelf.

---

## Part A — "Reset to menu" button + F1 shortcut

### Problem
While serving a customer the cashier drills into groups / plugin steps / search
results. The only way back is the per-level `← Back` button (one level at a
time), and nothing clears the search box or returns focus to the scanner. They
want one action that lands them back at the top menu, ready to scan the next
item.

### Behaviour (owner-confirmed)
Navigation reset **only** — cart, customer, payments, and discount are
**untouched**. (Full "new sale" already exists as `newSale()`; this is
deliberately not that, so an accidental press never loses an in-progress sale.)

`reset()` does, from any state:
- `search = ''`
- `amountNode = null`, `detailHtml = ''` (exit a plugin amount/detail step)
- `menuMode = 'cards'`
- `loadGroupsTop()` — empties `groupStack`, `pluginLeaves`, `products`, loads the
  top-level cards
- `focusEl('scanInput')` — cursor back in the barcode field

### Implementation
- **`static/js/app.js`** — new `reset()` method on the POS Alpine component.
- **`templates/pages/cashier/pos.templ`** — a visible button in the catalog
  toolbar (beside "+ Quick item"), always shown so it works from any depth and
  doubles as "clear + focus scanner". Label: `⟲ Menu (F1)`.
- **`onKey()`** — add a `case "F1"` with `e.preventDefault()` (so the browser
  help panel never opens) → `reset()`. Existing keys unchanged: F2 search, F3
  scan, F4 hold, F9 discount, F10 pay, Esc close/blur, Enter new-sale-on-receipt.
  Keep the existing `palette-open` guard at the top of `onKey`.

---

## Part B — Found-at-till for service-line consumables

### Problem
`internal/features/sales/service.go` has two stock paths:
- **Real product line** (`!p.IsService`, ~`:383`): on a short count it honours
  `it.AllowOversell` → `FoundAtTill(...)` corrects the count **up** (never
  negative), then the guarded decrement succeeds.
- **Service line consumables** (`p.IsService`, the component loop ~`:351`): on a
  short count it **always** returns `Conflict("insufficient stock for a
  consumable used by " + p.Name)` — no oversell escape.

So a copy job whose paper reads zero is blocked, even though the paper exists.
The owner wants the same "the human beats an imperfect count" behaviour the real
products already have.

### Behaviour (owner-confirmed)
Confirm-gated (like product found-at-till), **not** flag-gated. When a job's
consumable reads short at checkout, the till shows a styled prompt ("A material
for this job reads zero — you have it? Proceed"). On OK the count is corrected
up and the sale completes. On cancel nothing changes.

### Server (`internal/features/sales/service.go`)
In the component-consume loop (~`:351`), when `DecrementGuarded(comp.ProductID,
cq)` returns `!ok`:
- If `it.AllowOversell` is **false** → keep today's `Conflict`, but carry a
  **distinct error code** (see below) so the client can detect it reliably
  without string-matching the message.
- If `it.AllowOversell` is **true** → read on-hand for `comp.ProductID`, compute
  `short = cq - onHand`, call `FoundAtTill(comp.ProductID, 0, short,
  <component cost>, cashierID)` (batchID 0 — consumables aren't lot-picked at the
  till; note "found at till — count corrected before sale"), then retry the
  guarded decrement, which must now succeed.

COGS is unchanged: `DepleteFEFO(comp.ProductID, cq)` still runs after a
successful decrement, so the line cost is still the true consumed cost.
`FoundAtTill` opens/tops the consumable's lot at its own cost (product cost when
no lot), matching how the product path already values a found lot.

**Error code:** introduce a stable code (e.g. `apperr` conflict with a
machine-readable code such as `consumable_short`) on the consumable-shortage
`Conflict`, surfaced in the JSON error envelope. The client keys off this code,
not the human message.

### Client (`static/js/app.js`)
The existing pre-checkout oversell pre-check (~`:1651`) can only see a line's own
`stock`; a service line carries MAX stock and its consumables are hidden inside
`components`, so a paper shortage is **only** knowable once the server tries.
This path is therefore **reactive**:

- In `checkout`, when the sale POST fails with the `consumable_short` code, show
  a styled prompt (reuse the `oversellPrompt` styling/pattern; a distinct
  variant or a shared prompt with tailored copy). On approve:
  - set `allow_oversell = true` on every cart **service line that carries
    components** (the shortage belongs to a job's material; setting it on the
    service lines with components is precise enough and avoids fragile
    name-matching),
  - re-run `checkout` **once** for this cause (guard so a repeated `consumable_short`
    can't infinite-loop — after one auto-retry, surface the error).
- On cancel: clear the prompt, no sale, cart intact.
- `newSale()` clears any such prompt state alongside the existing
  `oversellPrompt` reset.

---

## Safety / blast radius

- **Part A** is pure front-end (JS + templ) plus one keyboard case. No schema, no
  API, no server behaviour change. Cart-preserving by construction.
- **Part B** only widens the *consumable* branch to match the *product* branch
  that already ships and is E2E-verified. The `AllowOversell=false` path is
  byte-identical to today except for an added error code. No migration. Applies
  to any service-with-components sale (documents jobs and core recipes alike),
  which is the intended reach.
- All work + E2E on the disposable dev `pos_db`.

## Out of scope
- Warning about a short consumable *before* checkout (the documents quote step
  could, but the owner accepted the reactive prompt).
- Lot-picking consumables at the till (batchID stays 0 for the found top-up).
- Any change to product found-at-till, credit, or dedup behaviour.

## Verification
1. `make build` (regens templ) + `go vet ./...` + `go test ./...`.
2. **Part A**: from a deep group / a plugin amount step / search results, press
   F1 and click the button → lands at top menu, search cleared, scanner focused,
   **cart unchanged**. Confirm F1 does not open browser help.
3. **Part B server**: DB-guarded sale test — a service line with a zero-stock
   consumable and `AllowOversell:true` corrects the consumable up (positive
   `adjust` movement), completes, records correct COGS; with `AllowOversell:false`
   it returns the `consumable_short` conflict and moves no stock.
4. **Part B live E2E**: set a copy paper product to 0 on-hand, ring a copy job →
   checkout prompts → approve → sale completes, paper count corrected up to land
   at 0 (never negative), receipt correct; cancel → no sale.
5. Regression: a normal product oversell still prompts proactively and works; a
   job with paper in stock never prompts.
6. Restore dev DB to baseline afterwards.
