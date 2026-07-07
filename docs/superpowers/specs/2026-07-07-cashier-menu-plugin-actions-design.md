# Cashier menu plugin actions + unified session lifecycle — design

Date: 2026-07-07
Branch: `cashier-menu-plugin-actions`
Status: approved (ready to plan)

Fold plugin cashier actions into the existing cashier **menu** (the product-group
directory), removing the separate quick-action strip; then fold the recharge
**float session** open/close into the core till's open/close so a shift is one
thing to open, close and log out of.

Built in **two phases** on one branch. Phase 1 is self-contained and reviewable on
its own; Phase 2 builds on it. **Core stays plugin-agnostic throughout** — every
integration point is a generic hook (the project's standing rule: no core change
for a specific plugin).

---

## Background (current state)

- **Cashier menu** = a DB-backed `product_groups` tree. The POS (`templates/pages/
  cashier/pos.templ`, the `pos()` Alpine scope) renders a folder's children as
  cards; `openGroup(id)` drills in (fetches children), `backGroup()` pops, a
  breadcrumb shows `groupStack`. Product leaves are tapped → `addToCart(p)`.
- **Plugin quick actions** = a separate tabbed strip *below* the grid, gated by
  `plugin.QuickActionTabs()`. Each plugin registers one `QuickActionTab{Key,Label,
  Component}`; the Component is a self-contained Alpine panel that fetches its own
  data, captures options/amount, and dispatches the `pos-add-service` window event
  `{id,name,price}` which the POS turns into a cart line.
  - Recharge: `ReloadPanel()` (carriers + devices dropdowns + amount + overdraw
    guard). Registered `Key:"reload"`.
  - Documents: `JobPanel()` (service → metered size/colour/double/qty, priced
    server-side). Registered `Key:"photocopy"`.
- **Recharge "Reload & Bills" cashier tab** (`plugins/recharge/recon.templ` +
  `cashier.go`): float session open (`POST /cashier/recharge/open` → SaveOpening),
  close (`/close` → SaveClosing), the reconciliation table, plus the transaction
  forms — Reload, Bill payment / get-money (`/bank-tx` → BankTx), Float deposit/
  withdraw (`/tx` → Tx).
- **Till open/close** = core cash register: opening drawer count, mid-session
  pay-in/withdraw, denomination-counted close / Z-report. Logout is guarded by
  `plugin.LogoutGuards()` (recharge blocks logout while a float session is open)
  then the core open-till guard.

---

## Phase 1 — Cashier menu plugin actions

### 1.1 The menu-node protocol (JSON)

Generalise menu navigation so any folder's children — core groups **or** a plugin
subtree — are the same shape, rendered by the same card grid:

```jsonc
{ "nodes": [
  // folder: drill in by fetching children_url
  { "kind":"folder", "name":"Dialog", "emoji":"📶",
    "children_url":"/cashier/recharge/menu/devices?carrier=3" },

  // leaf → shared inline amount step, then add a service line
  { "kind":"leaf", "action":"amount", "name":"Reload — Dialog · 077…",
    "add_url":"/cashier/recharge/menu/reload",   // POST {amount, meta} → validates + returns line
    "meta":{ "carrier_id":3, "device_id":9 } },

  // leaf → render a plugin form fragment inline (Back + form), form adds the line
  { "kind":"leaf", "action":"detail",
    "detail_url":"/cashier/documents/job?service=2" },

  // leaf → core product (today's behaviour, unchanged)
  { "kind":"leaf", "action":"product", "product":{ /* full product row */ } }
] }
```

Field notes:
- `emoji` optional (folder icon; falls back to 📁 as today).
- `action:"amount"` leaves carry an `add_url`. On **Add**, the client POSTs
  `{amount, meta}`; the plugin **validates** (e.g. recharge overdraw guard) and
  returns the resolved cart line `{id,name,price,meta}` (or a 4xx with a message
  shown inline). This keeps domain rules server-side and lets the guard run before
  the line is accepted.
- `action:"detail"` leaves render the plugin's own form fragment (its existing
  options UI) inside the menu; the fragment dispatches `pos-add-service` as today.

### 1.2 The hook

```go
// CashierMenuRoot adds a card at the ROOT of the cashier menu (alongside product
// groups). Tapping it navigates into ChildrenURL, which returns the node protocol.
type CashierMenuRoot struct { Key, Emoji, Label, ChildrenURL string }
func (r *Registry) AddCashierMenuRoot(m CashierMenuRoot)
func CashierMenuRoots() []CashierMenuRoot
```

`QuickActionTab` (type, `AddQuickActionTab`, `QuickActionTabs`, the strip markup in
`pos.templ`, and both plugins' registrations) is **removed**.

### 1.3 Core menu renderer changes (`pos.templ` `pos()` scope)

- Root view lists product-group cards **then** `plugin.CashierMenuRoots()` cards.
- Navigation stack generalises from "group id" to a node/URL: a folder card fetches
  its `children_url` (core groups keep working via their existing endpoint, now
  emitting the node shape at the plugin-root boundary only — core group nodes may
  stay in their current shape adapted by a thin client mapper; see plan).
- New leaf handling:
  - `product` → `addToCart` (unchanged).
  - `amount` → replace the card grid **in place** with the shared amount step
    (Back, big numeric input, Add). On Add → POST `add_url` → on success dispatch
    `pos-add-service` with the returned line; on 4xx show the message inline.
  - `detail` → fetch `detail_url`, render the returned fragment inline (Back above
    it); the fragment adds the line itself.
- One reusable **amount step** component (touch-friendly numeric input; mirrors the
  serial-input pattern's feel). Inline, not a modal.

### 1.4 Recharge subtree (`📶 Reload & Bills` root)

`ChildrenURL = /cashier/recharge/menu` returns three folder cards:
- **Reload** → `/menu/reload/carriers` → carrier folders → `/menu/reload/devices?
  carrier=X` → device **amount-leaves** (`add_url=/menu/reload` validates float,
  returns the reload line). Full card drill-down.
- **Bills** → `detail` leaf → the existing bill-pay / get-money form as an inline
  fragment (reuses `BankTx` logic).
- **Float transactions** → `detail` leaf → the existing deposit/withdraw form as an
  inline fragment (reuses `Tx` logic).

Remove `ReloadPanel` + `AddQuickActionTab`. The recharge endpoints already return
carriers/devices JSON; add the thin `/menu/*` wrappers emitting the node shape.

### 1.5 Documents subtree (`🖨 Documents` root)

`ChildrenURL = /cashier/documents/menu` returns service leaves as `detail` (each
`detail_url` = the job form for that service — the current `JobPanel` logic split
into a per-service inline fragment). Remove `JobPanel` + `AddQuickActionTab`.

### Phase 1 done = the strip is gone; Recharge (Reload full-card + Bills + Float
txns) and Documents are reachable as menu cards; cart lines are identical to today.

---

## Phase 2 — Unified session lifecycle (float open/close on the main page)

Goal: one open, one close, one logout — the recharge float session rides the core
till session instead of a separate tab.

### 2.1 Generic core hooks (no "float" in core)

Add open/close **step** slots to the cash-register flow, mirroring `LogoutGuard`:

```go
// SessionStep contributes an extra panel to the till OPEN and CLOSE dialogs plus
// server callbacks to persist it in the SAME transaction as the drawer open/close.
type SessionStep struct {
    OpenPanel   templ.Component // inputs shown in the Open-till dialog
    ClosePanel  templ.Component // inputs shown in the Close-register dialog
    // Persist hooks run inside the till open/close tx; a returned error rolls the
    // whole open/close back (so drawer + float are always consistent).
    OnOpen  func(ctx, tx, sessionID int64, form url.Values) error
    OnClose func(ctx, tx, sessionID int64, form url.Values) error
}
```

Core renders registered `OpenPanel`s inside its open-till dialog and `ClosePanel`s
inside its close dialog, and calls `OnOpen`/`OnClose` within the existing
open/close transaction. Core never references recharge.

### 2.2 Recharge fills the slots

- **OpenPanel:** per-device opening float inputs (carrying last close forward as the
  default, as the recharge tab does today). `OnOpen` records the float session
  opening tied to the till `sessionID`.
- **ClosePanel:** per-device closing float count + the reconciliation summary.
  `OnClose` closes the float session and books any variance.
- A **"Close float"**-style affordance is unnecessary as a separate button — it's a
  section inside the existing Close Register dialog (user's "next to close
  register … it will have those details").

### 2.3 Retire the float tab + simplify logout

- The recharge cashier tab shrinks to **read-only** (today's transaction log /
  reconciliation view) or is removed if fully covered by the close dialog — decided
  in the Phase-2 plan.
- `LogoutGuard` for recharge float is **dropped**: closing the till closes the float
  in the same step, so logout has only the core open-till guard to honour.

### Phase 2 done = opening the till captures opening floats; closing the register
counts/closes them in one dialog; logout guards one session.

---

## Boundary & safety

- **Plugin → core only.** New hooks (`CashierMenuRoot`, `SessionStep`) are generic;
  core has no plugin-specific code. Recharge/documents keep their own domain logic
  and validation (overdraw guard, FEFO pricing).
- **Atomicity.** `amount` leaves validate server-side before a line is accepted;
  Phase-2 float open/close persist inside the till open/close transaction (a float
  failure rolls back the drawer step too).
- **No data migration** expected for Phase 1. Phase 2 reuses the existing recharge
  float-session tables; any schema tweak (e.g. linking a float session to a till
  session id) is an additive migration, decided in the Phase-2 plan.

## Out of scope / future

- Reordering/admin-configuring where plugin root cards sit (fixed: after product
  groups, in registration order).
- Command-palette entries for the new menu nodes.
- Moving *admin* recharge/documents pages — untouched.

## Files touched (estimate)

- **Phase 1 core:** `internal/plugin/hooks.go` (add `CashierMenuRoot`, remove
  `QuickActionTab`), `templates/pages/cashier/pos.templ` (menu renderer + amount
  step, strip removal), possibly a small cashier JSON endpoint adapter.
- **Phase 1 recharge:** `plugins/recharge/{recharge.go,cashier.go,pos.templ}` (menu
  endpoints, remove ReloadPanel).
- **Phase 1 documents:** `plugins/documents/{documents.go,pos.templ}` (menu
  endpoints, remove JobPanel).
- **Phase 2 core:** cash-register open/close flow + `SessionStep` hook.
- **Phase 2 recharge:** OpenPanel/ClosePanel + OnOpen/OnClose, tab slimming, drop
  LogoutGuard.
