# Unified session lifecycle — float open/close folded into the till dialog (Phase 2)

**Date:** 2026-07-07
**Branch:** `cashier-menu-plugin-actions`
**Supersedes:** the "Phase 2 — Unified session lifecycle" section of
`2026-07-07-cashier-menu-plugin-actions-design.md` (which proposed the atomic
`SessionStep` variant; we chose the simpler **chained** approach below).

## Goal

One open, one close, one logout. The recharge per-device float opening/closing —
today entered on a separate "Reload & Bills" tab — is captured **inside the core
till Open and Close dialogs**, so a shift is a single thing to open, close and log
out of. The Reload & Bills tab keeps Bill payments, Float transactions and the
reconciliation view; only the open/close balance-capture sections move out.

## Key facts that make this light

- **Recharge floats are already keyed by the core till session id.**
  `store.SaveOpening(sessionID, deviceID, amt)` / `SaveClosing(...)` and
  `HasOpenFloat(sessionID)` all hang off the till session — there is no separate
  "float session" entity to merge.
- **Reconciliation already auto-carries the opening from the last close.** So the
  opening inputs are an *override* of a sensible default; a missing opening-save is
  low-stakes.
- **The existing save endpoints already parse the right form fields.**
  `/cashier/recharge/open` and `/cashier/recharge/close` read `opening_<deviceID>` /
  `closing_<deviceID>` form values — they are reused unchanged.

## Chosen approach: Chained (not atomic)

The till dialog shows the float inputs; the browser saves floats via the existing
recharge endpoints right around the till call. Rejected the atomic
`plugin_data`-in-the-till-transaction variant: a float count is a re-enterable
recorded number, not a cash movement, so full transactional rollback buys little
and costs core-tx plumbing + a tx-aware SaveOpening.

## Architecture

### 1. Core: one generic hook (no plugin names in core)

`internal/plugin/hooks.go`:

```go
// DrawerSection contributes an extra panel to the till OPEN and CLOSE dialogs
// (e.g. a plugin's per-session sub-ledger the cashier counts alongside the
// drawer). Core renders an empty slot and, client-side, loads each section's
// form fragment and posts it to the section's save URL around the drawer call.
// Core never references the plugin's domain.
type DrawerSection struct {
    Key          string // stable id, e.g. "recharge"
    OpenFormURL  string // GET → HTML input rows for the Open-till dialog
    CloseFormURL string // GET → HTML input rows for the Close-register dialog
    SaveOpenURL  string // POST (form-encoded) target, after the till opens
    SaveCloseURL string // POST (form-encoded) target, before the till closes
}
```

Plus `AddDrawerSection(DrawerSection)`, `DrawerSections() []DrawerSection`, and
`DrawerSectionsJSON() string` (the client-facing list:
`[{"key","openFormUrl","closeFormUrl","saveOpenUrl","saveCloseUrl"}]`), mirroring
the existing `CashierMenuRoot*` helpers.

### 2. Core: the till Open/Close dialogs (`templates/pages/cashier/pos.templ` + `static/js/app.js`)

- Each dialog gains a slot container. `pos()` is passed `DrawerSectionsJSON()` at
  init.
- **On opening a dialog:** for each section, `fetch` its `OpenFormURL` /
  `CloseFormURL` and inject the returned HTML (plain inputs) into a per-section
  wrapper carrying the save URL. The fragment is inputs only — **no `hx-post`
  inside it** — so the Phase-1 "HTMX never processes an injected fragment" trap
  does not apply; the core Alpine submit reads the inputs directly.
- **`openRegister()`** (open flow — till first, so the session exists for the save):
  1. POST `/api/cash-register/open` (unchanged).
  2. On success, for each section serialize its inputs (the `opening_*` fields)
     and POST form-encoded to `SaveOpenURL`. Best-effort: a failure toasts but the
     till stays open (reconciliation still carries the last-close opening).
- **`submitClose()`** (close flow — floats first, while the session is still open):
  1. For each section, POST its `closing_*` inputs to `SaveCloseURL`. If any fails
     → toast and **abort** (do not close the till; it stays open and recoverable).
  2. POST `/api/cash-register/close` (unchanged); show the close result as today.

When no plugin registers a section (recharge disabled), the slots are empty and
the dialogs behave exactly as before.

### 3. Recharge fills the slot (`plugins/recharge/`)

- Two new GET fragment handlers returning **input rows only**:
  - opening rows — per device, number input `opening_<deviceID>` defaulting to the
    carried last-close balance (reuse `openingValue`).
  - closing rows — per device, opening shown read-only + count input
    `closing_<deviceID>` + expected (reuse the reconciliation data).
- `SaveOpenURL` = existing `POST /cashier/recharge/open` (SaveOpening).
  `SaveCloseURL` = existing `POST /cashier/recharge/close` (SaveClosing) — both
  unchanged. (SaveClosing already handles `logout=1`; the client no longer needs
  it, but leaving it is harmless.)
- Register one `DrawerSection` in `recharge.go`.

### 4. Recharge: slim the Reload & Bills tab (`recon.templ`)

`ReconBody` drops the opening/closing **input form (`#recon-floats`) and the
Save-opening / Save-closing buttons**. It keeps `txForm` (Float transactions),
`bankTxForm` (Bills) and a **read-only reconciliation summary** (the per-carrier
Opening / Reload in / Reload out / Expected / Counted / Bonus-loss stats, without
editable inputs). `carrierBlock` gains a read-only rendering (or a sibling
read-only variant) for this.

### 5. Recharge: drop the LogoutGuard → one logout

Remove the recharge `AddLogoutGuard` registration. The close dialog now closes
floats together with the till (save-first, abort-on-fail ⇒ no orphan open floats),
so the only remaining logout guard is the core "till still open" one. Logout has a
single place to finish.

## Error handling & edge cases

- **Open, float-save fails:** toast; till stays open; reconciliation falls back to
  the auto-carried last-close opening. Acceptable (inputs are an override).
- **Close, float-save fails:** abort the close; till stays open; cashier retries.
  No till-closed-with-open-floats state is reachable, which is why the guard can go.
- **Close, till-close fails after floats saved:** floats are already recorded;
  retrying close re-saves them (idempotent overwrite) then closes. Recoverable.
- **Recharge disabled:** no section registered → dialogs unchanged.

## Boundary & safety

- **Plugin → core only.** `DrawerSection` is generic (URLs + a key); core has no
  recharge/float references. Recharge keeps all its domain logic and validation.
- **No schema change.** Reuses the existing per-session float tables and the
  existing save endpoints.

## Testing

- Manual E2E: open till → enter cash count + per-device float in the one dialog →
  verify opening rows recorded; make a reload; close register → count cash +
  closing float in the one dialog → reconciliation (bonus/loss) correct → logout is
  clean (no bounce to the recon tab).
- Existing recharge unit tests (`cashier_menu_test.go` etc.) stay green.
- Regression: with recharge disabled, till open/close/logout unchanged.

## Files touched (estimate)

- **Core:** `internal/plugin/hooks.go` (add `DrawerSection` + helpers),
  `templates/pages/cashier/pos.templ` (dialog slots), `static/js/app.js`
  (`pos()`: load fragments, save around open/close), and where `pos()` is
  constructed to pass `DrawerSectionsJSON()`.
- **Recharge:** `plugins/recharge/recharge.go` (register section, drop logout
  guard), `plugins/recharge/cashier.go` (two GET fragment handlers + routes),
  `plugins/recharge/recon.templ` (slim `ReconBody`, read-only `carrierBlock`).

## Out of scope

- Admin recharge/documents pages (untouched).
- Documents plugin (registers no drawer section).
- Reordering/config of where sections appear (fixed: below the denomination count).
