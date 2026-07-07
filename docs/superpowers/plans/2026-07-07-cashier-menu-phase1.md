# Cashier Menu Plugin Actions — Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the cashier POS quick-action strip and fold plugin actions into the existing cashier menu as drill-down cards — Recharge (Reload full-card + Bills + Float txns) and Documents — behind one generic `CashierMenuRoot` hook + a menu-node JSON protocol.

**Architecture:** The cashier menu already navigates a tree by fetching each folder's children as JSON (`/api/groups`, `/api/groups/:id`). We add a generic hook letting a plugin contribute a root card whose `ChildrenURL` returns a small **node protocol** (`folder` / `leaf` with `action` = `product|amount|detail`). The same `pos()` Alpine renderer draws plugin nodes with the identical card grid + breadcrumb; `amount` leaves show a shared inline amount step, `detail` leaves render a plugin form fragment. `QuickActionTab` is deleted.

**Tech Stack:** Go 1.x, Echo v4, templ, Alpine.js (vanilla, no JS test harness), Tailwind (embedded CSS), Postgres. Plugins are compile-time (`internal/plugin`).

## Global Constraints

- **Plugin → core only.** No core code may reference a specific plugin. The new hook (`CashierMenuRoot`) is generic. (Spec: Boundary & safety.)
- **Never commit** `cmd/server/enabled_plugins.go`, `static/css/tailwind.css`, `.claude/settings.local.json`. `_templ.go` are generated (git-ignored) — never stage.
- **Assets are embedded:** after any `.templ`, `.js`, or CSS change run `make templ && make css`, rebuild the binary, and restart before trusting the browser.
- Run `make templ` after editing any `.templ`; `go build ./...` must stay green.
- Commit messages end with: `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`.
- Branch: `cashier-menu-plugin-actions` (already checked out).
- Enable both plugins locally for testing by adding blank imports to `enabled_plugins.go` (do NOT commit it): `_ "karots-pos/plugins/recharge"`, `_ "karots-pos/plugins/documents"`.
- Dev logins: admin `0771234567/1234`, cashier `0771111111/1111`. Server on `:3000`, ESC/POS emulator viewer `:8631`.

---

## Node protocol (the contract every task shares)

A plugin `ChildrenURL` / folder `children_url` returns:

```jsonc
{ "nodes": [
  { "kind":"folder", "name":"Dialog", "emoji":"📶",
    "children_url":"/cashier/recharge/menu/devices?carrier=3" },
  { "kind":"leaf", "action":"amount", "name":"Reload — Dialog · 077…",
    "add_url":"/cashier/recharge/menu/reload",
    "meta":{"carrier_id":3,"device_id":9} },
  { "kind":"leaf", "action":"detail", "name":"Pay a bill",
    "detail_url":"/cashier/recharge/menu/bill" }
] }
```

- **folder**: has `children_url`. Tapped → fetch it → render its `nodes`; push a breadcrumb frame `{name, emoji, url}`.
- **leaf `amount`**: has `add_url` + `meta`. Tapped → inline amount step. On Add → `POST add_url` with `{amount, meta}` → **200** returns `{line:{id,name,price,meta}}` (add it to cart) or **4xx** returns `{message}` (show inline, stay).
- **leaf `detail`**: has `detail_url`. Tapped → `GET detail_url` returns an HTML fragment rendered inline (with a Back above it); the fragment dispatches `pos-add-service` itself (existing mechanism).
- **leaf `product`** (core only, emitted by `/api/groups/:id`): unchanged `addToCart`.

`add_url` responses use the existing `response` helpers; a validation failure is `apperr.Validation(msg)` → the error handler returns JSON `{error:{message}}` for `/cashier/...` XHR (verify shape in Task 3 Step 1).

---

## Task 1: `CashierMenuRoot` hook; remove `QuickActionTab`

**Files:**
- Modify: `internal/plugin/hooks.go` (remove `QuickActionTab` block ~L76-85; its var ~L103; `AddQuickActionTab` ~L129; `QuickActionTabs` ~L142. Add `CashierMenuRoot`.)
- Modify: `templates/pages/cashier/pos.templ:143-161` (delete the `if len(plugin.QuickActionTabs()) > 0 { … }` strip block).
- Modify: `plugins/recharge/recharge.go:110` (remove `AddQuickActionTab` line — temporary; re-added as a menu root in Task 3).
- Modify: `plugins/documents/documents.go:47` (remove `AddQuickActionTab` line — re-added in Task 4).

**Interfaces:**
- Produces:
  ```go
  type CashierMenuRoot struct { Key, Emoji, Label, ChildrenURL string }
  func (r *Registry) AddCashierMenuRoot(m CashierMenuRoot)
  func CashierMenuRoots() []CashierMenuRoot
  ```

- [ ] **Step 1: Add the hook type + registry funcs, remove QuickActionTab**

In `internal/plugin/hooks.go`, delete the `QuickActionTab` type, its slice var, `AddQuickActionTab`, and `QuickActionTabs`. Add near the other hook types:

```go
// CashierMenuRoot adds a card at the ROOT of the cashier menu (alongside the
// product-group cards). Tapping it navigates into ChildrenURL, a GET that returns
// the menu-node JSON protocol ({"nodes":[…]}). This replaces the old quick-action
// strip: a plugin's actions live in the same drill-down menu as products.
type CashierMenuRoot struct {
	Key         string
	Emoji       string
	Label       string
	ChildrenURL string
}
```

Add to the `var (…)` block: `cashierMenuRoots []CashierMenuRoot`. Add:

```go
func (r *Registry) AddCashierMenuRoot(m CashierMenuRoot) { cashierMenuRoots = append(cashierMenuRoots, m) }
func CashierMenuRoots() []CashierMenuRoot                { return cashierMenuRoots }
```

- [ ] **Step 2: Delete the strip block in `pos.templ`**

Remove the entire `if len(plugin.QuickActionTabs()) > 0 { … }` block (lines ~143-161). Leave the surrounding `</section>` intact.

- [ ] **Step 3: Remove the two plugin registrations (temporary)**

Delete `reg.AddQuickActionTab(...)` at `plugins/recharge/recharge.go:110` and `plugins/documents/documents.go:47`. (Their `ReloadPanel`/`JobPanel` templates stay for now; wired anew in Tasks 3–4.)

- [ ] **Step 4: Verify it compiles with no QuickActionTab references left**

Run: `make templ && go build ./...`
Expected: builds clean.
Run: `grep -rn "QuickActionTab" internal/ plugins/ templates/ | grep -v _templ.go`
Expected: no matches.

- [ ] **Step 5: Commit**

```bash
git add internal/plugin/hooks.go templates/pages/cashier/pos.templ plugins/recharge/recharge.go plugins/documents/documents.go
git commit -m "feat(cashier-menu): add CashierMenuRoot hook; remove QuickActionTab strip

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: Core menu renderer — plugin root cards, node nav, inline amount + detail steps

**Files:**
- Modify: `static/js/app.js` (the `pos()` scope, ~L265-477 nav + a new amount/detail sub-view; `addServiceLine` ~L769 reused).
- Modify: `templates/pages/cashier/pos.templ` (root card grid: render `plugin.CashierMenuRoots()` cards; add the inline amount-step + detail-fragment views; import already has `karots-pos/internal/plugin`).

**Interfaces:**
- Consumes: `plugin.CashierMenuRoots()` (Task 1).
- Produces (Alpine `pos()` scope additions consumed by pos.templ):
  - state: `menuMode` (`"cards"|"amount"|"detail"`), `amountNode` (obj), `amountValue` (string), `amountError` (string), `detailHtml` (string), `pluginRoots` (array injected from templ).
  - methods: `openNode(node)`, `openPluginRoot(root)`, `confirmAmount()`, `cancelStep()`.

- [ ] **Step 1: Inject plugin roots into the scope**

In `pos.templ`, where `pos(...)` is instantiated (the `x-data`), pass the roots as JSON. Find the `x-data="pos(...)"` on the POS root element and append an argument. In `pos.templ` add a templ expression building the JSON:

```templ
{{ rootsJSON := plugin.CashierMenuRootsJSON() }}
```
and change the scope init to `x-data={ "pos('" + d.Symbol + "', '" + d.DefaultType + "', " + askToPrintJS + ", " + rootsJSON + ")" }`.

Add a helper in the cashier pages package (e.g. `templates/pages/cashier/pos.templ` bottom, Go block) — OR simpler, add `func CashierMenuRootsJSON() string` in `internal/plugin/hooks.go`:

```go
// CashierMenuRootsJSON renders the menu roots as a JSON array for the cashier
// Alpine scope: [{"emoji":"📶","label":"Reload & Bills","url":"/cashier/recharge/menu"}].
func CashierMenuRootsJSON() string {
	type r struct { Emoji, Label, URL string }
	out := make([]r, 0, len(cashierMenuRoots))
	for _, m := range cashierMenuRoots {
		out = append(out, r{m.Emoji, m.Label, m.ChildrenURL})
	}
	b, _ := json.Marshal(out)
	return string(b)
}
```
(Add `encoding/json` import to hooks.go.)

Update `pos(symbol, defaultType, askToPrint)` signature in app.js to `pos(symbol, defaultType, askToPrint, pluginRoots)` and store `pluginRoots: pluginRoots || [],`. Add new state:
```js
menuMode: "cards",   // "cards" | "amount" | "detail"
amountNode: null, amountValue: "", amountError: "", detailHtml: "",
```

- [ ] **Step 2: Render plugin root cards + the two step views in `pos.templ`**

After the product-group cards `<template x-for="g in groupChildren">…</template>` block (pos.templ ~L118) but still inside the `x-show="inGroups && …"` card region, add a plugin-roots row shown only at the menu root (empty breadcrumb) and in card mode:

```templ
<template x-if="menuMode==='cards' && !groupStack.length && !search.trim()">
	<div class="grid grid-cols-2 sm:grid-cols-3 xl:grid-cols-4 gap-3 mb-3">
		<template x-for="r in pluginRoots" x-bind:key="'pr'+r.url">
			<button type="button" x-on:click="openPluginRoot(r)" class="text-left p-3 rounded-xl bg-white shadow-sm hover:ring-2 hover:ring-indigo-400">
				<div class="text-2xl leading-none" x-text="r.emoji || '📁'"></div>
				<div class="font-medium text-sm mt-1" x-text="r.label"></div>
			</button>
		</template>
	</div>
</template>
```

Then wrap the existing folder/product card grids so they only show in `menuMode==='cards'` (add `menuMode==='cards' &&` to their `x-show`/`x-if`). After them, add the amount + detail views:

```templ
<!-- Inline amount step (plugin 'amount' leaves) -->
<template x-if="menuMode==='amount'">
	<div class="p-2">
		<button type="button" x-on:click="cancelStep()" class="px-3 py-1.5 rounded-lg border text-sm hover:bg-slate-50 mb-3">← Back</button>
		<div class="text-sm text-slate-500 mb-1" x-text="amountNode && amountNode.name"></div>
		<div class="text-lg font-semibold mb-2">Enter amount</div>
		<input type="number" min="0" step="0.01" x-model="amountValue" x-ref="amtInput"
			class="w-full max-w-xs border rounded-lg px-3 py-3 text-right text-2xl" placeholder="0.00"/>
		<div x-show="amountError" x-text="amountError" class="text-rose-600 text-sm mt-1"></div>
		<button type="button" x-on:click="confirmAmount()" x-bind:disabled="busy || !(Number(amountValue)>0)"
			class="mt-3 px-6 py-2.5 rounded-lg bg-indigo-600 text-white font-semibold disabled:opacity-40">Add</button>
	</div>
</template>
<!-- Inline detail fragment (plugin 'detail' leaves) -->
<template x-if="menuMode==='detail'">
	<div class="p-2">
		<button type="button" x-on:click="cancelStep()" class="px-3 py-1.5 rounded-lg border text-sm hover:bg-slate-50 mb-3">← Back</button>
		<div x-html="detailHtml"></div>
	</div>
</template>
```

- [ ] **Step 3: Add the nav methods in app.js**

Insert into the `pos()` scope (near `openGroup`):

```js
openPluginRoot(r) {
  this.inGroups = true;
  this.groupStack = [{ name: r.label, emoji: r.emoji, url: r.url }];
  return this.fetchNodes(r.url);
},
async fetchNodes(url) {
  this.menuMode = "cards";
  const json = await apiFetch("GET", url);
  const nodes = (json && json.nodes) || (json.data && json.data.nodes) || [];
  // Map nodes onto the existing card grids: folders -> groupChildren, leaves kept on the node.
  this.groupChildren = nodes.filter(n => n.kind === "folder")
    .map(n => ({ name: n.name, emoji: n.emoji, _node: n }));
  this.products = []; // plugin folders have no product leaves
  this._leaves = nodes.filter(n => n.kind === "leaf");
  this.pluginLeaves = this._leaves; // rendered as cards (see Step 4)
},
openNode(node) {
  if (node.kind === "folder") {
    this.groupStack.push({ name: node.name, emoji: node.emoji, url: node.children_url });
    return this.fetchNodes(node.children_url);
  }
  if (node.action === "amount") {
    this.amountNode = node; this.amountValue = ""; this.amountError = "";
    this.menuMode = "amount";
    this.$nextTick(() => this.$refs.amtInput && this.$refs.amtInput.focus());
    return;
  }
  if (node.action === "detail") {
    return this.openDetail(node.detail_url);
  }
  if (node.action === "product") return this.addToCart(node.product);
},
async openDetail(url) {
  const res = await fetch(url, { credentials: "same-origin" });
  this.detailHtml = await res.text();
  this.menuMode = "detail";
  this.$nextTick(() => window.Alpine && window.Alpine.initTree && window.Alpine.initTree(this.$el));
},
async confirmAmount() {
  if (this.busy) return; this.busy = true; this.amountError = "";
  try {
    const res = await fetch(this.amountNode.add_url, {
      method: "POST", credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ amount: this.amountValue, meta: this.amountNode.meta || {} }),
    });
    const json = await res.json().catch(() => ({}));
    if (!res.ok) { this.amountError = (json.error && json.error.message) || json.message || "Could not add"; return; }
    this.addServiceLine(json.line || json.data);
    this.cancelStep();
  } finally { this.busy = false; }
},
cancelStep() {
  this.menuMode = "cards";
  this.amountNode = null; this.detailHtml = "";
},
```

Update `backGroup()` so a plugin frame (has `.url`) re-fetches nodes:

```js
backGroup() {
  this.menuMode = "cards";
  this.groupStack.pop();
  const top = this.groupStack[this.groupStack.length - 1];
  if (top && top.url) return this.fetchNodes(top.url);
  if (top) return this.openGroup(top.id, true);
  return this.loadGroupsTop();
},
```

Add `pluginLeaves: [], _leaves: [],` to the scope state.

- [ ] **Step 4: Render plugin folder + leaf cards through `openNode`**

In `pos.templ`, the existing folder-card `x-for="g in groupChildren"` button currently calls `openGroup(g.id)`. Change its click to branch: `x-on:click="g._node ? openNode(g._node) : openGroup(g.id)"`. After the folder grid, add a leaf-card grid:

```templ
<template x-if="menuMode==='cards' && pluginLeaves.length">
	<div class="grid grid-cols-2 sm:grid-cols-3 xl:grid-cols-4 gap-3 mb-3">
		<template x-for="n in pluginLeaves" x-bind:key="'ln'+n.name">
			<button type="button" x-on:click="openNode(n)" class="text-left p-3 rounded-xl bg-white shadow-sm hover:ring-2 hover:ring-indigo-400">
				<div class="text-2xl leading-none" x-text="n.emoji || '▸'"></div>
				<div class="font-medium text-sm mt-1" x-text="n.name"></div>
			</button>
		</template>
	</div>
</template>
```

Clear `pluginLeaves` when returning to core menu: in `loadGroupsTop` and `openGroup`, add `this.pluginLeaves = [];` and `this.menuMode = "cards";`.

- [ ] **Step 5: Build, deploy, and verify the core menu still works (no regressions)**

Run:
```bash
make templ && make css && go build -o "$SCRATCH/posserver" ./cmd/server
```
(`$SCRATCH` = the session scratchpad dir.) Restart the server. In Playwright as cashier: open `/cashier`, confirm product-group cards + drill-down + Back + add-to-cart still work exactly as before, and no quick-action strip is present. (Plugin roots array is empty until Tasks 3–4, so no plugin cards yet.)

- [ ] **Step 6: Commit**

```bash
git add static/js/app.js templates/pages/cashier/pos.templ internal/plugin/hooks.go
git commit -m "feat(cashier-menu): render plugin menu roots + node nav + inline amount/detail steps

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 3: Recharge subtree (Reload full-card + Bills + Float txns)

**Files:**
- Modify: `plugins/recharge/recharge.go` (register `CashierMenuRoot`; add `/cashier/recharge/menu*` routes).
- Modify: `plugins/recharge/cashier.go` (menu handlers: `MenuRoot`, `MenuReloadCarriers`, `MenuReloadDevices`, `MenuReloadAdd`, `MenuBill`, `MenuFloatTx`).
- Modify: `plugins/recharge/pos.templ` (Bills + Float-tx inline fragments; remove `ReloadPanel`).
- Test: `plugins/recharge/cashier_menu_test.go` (node builders + reload validation).

**Interfaces:**
- Consumes: `plugin.CashierMenuRoot` (Task 1); node protocol (top of plan).
- Produces: `GET /cashier/recharge/menu` → root nodes; `GET /cashier/recharge/menu/reload/carriers`, `…/reload/devices?carrier=ID`; `POST /cashier/recharge/menu/reload` `{amount, meta:{carrier_id,device_id}}` → `{line:{id,name,price,meta}}` or 4xx; `GET /cashier/recharge/menu/bill`, `…/menu/float` (HTML fragments).

- [ ] **Step 1: Confirm the `/cashier` XHR error JSON shape**

Run a quick probe against the running server (Task 2 build) to see how `apperr.Validation` renders for a `/cashier/...` POST without `Accept: json`:
```bash
curl -s -b cookies.txt -X POST http://127.0.0.1:3000/cashier/recharge/menu/reload -H "Content-Type: application/json" -d '{"amount":"-1","meta":{}}' -w "\n%{http_code}\n"
```
Confirm the body is `{"error":{"message":"…"}}` (adjust `confirmAmount()` parsing in Task 2 Step 3 if the shape differs). Note the exact shape here before writing handlers.

- [ ] **Step 2: Write failing tests for the reload node builders + amount validation**

Create `plugins/recharge/cashier_menu_test.go`. Test the pure helpers you will extract: `reloadDeviceNode(carrierID, d)` returns a leaf with `action=="amount"`, correct `add_url`, and `meta.device_id`; and `parseReloadMeta(body)` rejects a non-positive amount. (Keep the DB-touching handlers out of the unit test.)

```go
package recharge

import "testing"

func TestReloadDeviceNodeShape(t *testing.T) {
	n := reloadDeviceNode(3, deviceRow{ID: 9, Label: "SIM 077", Number: "0771234567"})
	if n.Kind != "leaf" || n.Action != "amount" { t.Fatalf("kind/action: %+v", n) }
	if n.AddURL != "/cashier/recharge/menu/reload" { t.Fatalf("add_url: %s", n.AddURL) }
	if n.Meta["carrier_id"] != int64(3) || n.Meta["device_id"] != int64(9) { t.Fatalf("meta: %v", n.Meta) }
}

func TestParseReloadAmountRejectsNonPositive(t *testing.T) {
	if _, err := parseAmount("0"); err == nil { t.Fatal("expected error for 0") }
	if _, err := parseAmount("-5"); err == nil { t.Fatal("expected error for -5") }
	if v, err := parseAmount("100"); err != nil || !v.Equal(mustDec("100")) { t.Fatalf("100: %v %v", v, err) }
}
```
(Define `deviceRow` as the existing device struct alias or the real type; `mustDec` via `decimal.RequireFromString`.)

- [ ] **Step 3: Run the tests — expect failure**

Run: `go test ./plugins/recharge/ -run 'Reload|ReloadAmount' -v`
Expected: FAIL — `reloadDeviceNode`, `parseAmount` undefined.

- [ ] **Step 4: Define the node types + builders**

Add to `plugins/recharge/cashier.go` (or a new `menu.go` in the package):

```go
// menuNode is one entry in the cashier menu-node protocol (see the core plan).
type menuNode struct {
	Kind        string           `json:"kind"`                   // "folder" | "leaf"
	Name        string           `json:"name"`
	Emoji       string           `json:"emoji,omitempty"`
	ChildrenURL string           `json:"children_url,omitempty"` // folder
	Action      string           `json:"action,omitempty"`       // leaf: "amount" | "detail"
	AddURL      string           `json:"add_url,omitempty"`      // amount leaf
	DetailURL   string           `json:"detail_url,omitempty"`   // detail leaf
	Meta        map[string]any   `json:"meta,omitempty"`
}

func reloadDeviceNode(carrierID int64, d Device) menuNode {
	label := d.Label
	if d.Number != "" { label += " · " + d.Number }
	return menuNode{
		Kind: "leaf", Name: "Reload — " + label, Action: "amount",
		AddURL: "/cashier/recharge/menu/reload",
		Meta:   map[string]any{"carrier_id": carrierID, "device_id": d.ID},
	}
}

func parseAmount(s string) (decimal.Decimal, error) {
	v, err := money.Parse(s)
	if err != nil || !v.IsPositive() { return decimal.Zero, apperr.Validation("enter an amount greater than zero") }
	return v, nil
}
```
Match `Device`'s real field names (`ID`, `Label`, `Number`) from `plugins/recharge/store.go`; adjust the test's `deviceRow` to the real `Device` type.

- [ ] **Step 5: Run the tests — expect pass**

Run: `go test ./plugins/recharge/ -run 'Reload|ReloadAmount' -v`
Expected: PASS.

- [ ] **Step 6: Implement the menu handlers**

In `plugins/recharge/cashier.go` add (uses existing `h.p.store.Carriers`, `.Devices`, `h.p.core.CashRegister.Current`, and the existing reload/overdraw logic):

```go
// MenuRoot returns the three recharge branches for the cashier menu.
func (h *cashierUI) MenuRoot(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{"nodes": []menuNode{
		{Kind: "folder", Name: "Reload", Emoji: "📲", ChildrenURL: "/cashier/recharge/menu/reload/carriers"},
		{Kind: "leaf", Name: "Bills", Emoji: "🧾", Action: "detail", DetailURL: "/cashier/recharge/menu/bill"},
		{Kind: "leaf", Name: "Float transactions", Emoji: "💱", Action: "detail", DetailURL: "/cashier/recharge/menu/float"},
	}})
}

// MenuReloadCarriers → carrier folders (reload-capable carriers only).
func (h *cashierUI) MenuReloadCarriers(c echo.Context) error {
	cs, err := h.p.store.Carriers(c.Request().Context())
	if err != nil { return err }
	nodes := make([]menuNode, 0, len(cs))
	for _, cr := range cs {
		nodes = append(nodes, menuNode{Kind: "folder", Name: cr.Name, Emoji: "📶",
			ChildrenURL: "/cashier/recharge/menu/reload/devices?carrier=" + strconv.FormatInt(cr.ID, 10)})
	}
	return c.JSON(http.StatusOK, map[string]any{"nodes": nodes})
}

// MenuReloadDevices → device amount-leaves for a carrier (reload-purpose devices).
func (h *cashierUI) MenuReloadDevices(c echo.Context) error {
	cid, _ := strconv.ParseInt(c.QueryParam("carrier"), 10, 64)
	ds, err := h.p.store.DevicesFor(c.Request().Context(), cid, "recharge") // existing purpose filter
	if err != nil { return err }
	nodes := make([]menuNode, 0, len(ds))
	for _, d := range ds { nodes = append(nodes, reloadDeviceNode(cid, d)) }
	return c.JSON(http.StatusOK, map[string]any{"nodes": nodes})
}

// MenuReloadAdd validates the amount against the device float (overdraw guard) and
// returns the cart line for the reload service product.
func (h *cashierUI) MenuReloadAdd(c echo.Context) error {
	var in struct {
		Amount string `json:"amount"`
		Meta   struct{ CarrierID, DeviceID int64 } `json:"meta"`
	}
	if err := c.Bind(&in); err != nil { return apperr.BadRequest("invalid request") }
	amt, err := parseAmount(in.Amount)
	if err != nil { return err }
	// Reuse the existing reload guard + service-product resolution (see the current
	// Reload handler): resolve the carrier's hidden service product + overdraw check.
	line, err := h.reloadLine(c.Request().Context(), in.Meta.CarrierID, in.Meta.DeviceID, amt)
	if err != nil { return err }
	return c.JSON(http.StatusOK, map[string]any{"line": line})
}
```

`reloadLine(ctx, carrierID, deviceID, amt)` factors out the existing Reload handler's logic to return `{id, name, price, meta}` (id = carrier service product id, name = "Reload — <carrier>", price = amt, meta carries device/carrier for checkout attribution). Keep the current overdraw guard.

`MenuBill` and `MenuFloat` render the existing forms as fragments:
```go
func (h *cashierUI) MenuBill(c echo.Context) error  { return response.RenderFragment(c, BillMenuForm(/* banks, symbol */)) }
func (h *cashierUI) MenuFloat(c echo.Context) error { return response.RenderFragment(c, FloatMenuForm(/* devices, symbol */)) }
```

- [ ] **Step 7: Add the Bills + Float inline fragments in `plugins/recharge/pos.templ`; remove `ReloadPanel`**

Create `BillMenuForm` and `FloatMenuForm` templ components — reuse the existing bank-tx / tx form markup (the current recon.templ `bankTxForm` / tx form), posting to the existing `/cashier/recharge/bank-tx` and `/cashier/recharge/tx` endpoints, and on success dispatching `pos-add-service` where appropriate OR closing the step (bill-pay/float are money moves, not cart lines — they post directly and toast; the menu step just hosts the form). Delete the old `ReloadPanel()` templ.

- [ ] **Step 8: Register the routes + the menu root**

In `plugins/recharge/recharge.go` Setup:
```go
reg.AddCashierMenuRoot(plugin.CashierMenuRoot{Key: "recharge", Emoji: "📶", Label: "Reload & Bills", ChildrenURL: "/cashier/recharge/menu"})
reg.Cashier().GET("/recharge/menu", ch.MenuRoot)
reg.Cashier().GET("/recharge/menu/reload/carriers", ch.MenuReloadCarriers)
reg.Cashier().GET("/recharge/menu/reload/devices", ch.MenuReloadDevices)
reg.Cashier().POST("/recharge/menu/reload", ch.MenuReloadAdd)
reg.Cashier().GET("/recharge/menu/bill", ch.MenuBill)
reg.Cashier().GET("/recharge/menu/float", ch.MenuFloat)
```

- [ ] **Step 9: Build, deploy, and E2E-verify Reload**

`make templ && make css && go build …`, restart. In Playwright as cashier (float session open): `/cashier` → tap **📶 Reload & Bills** → **Reload** → a **carrier** → a **device** → the **Enter amount** step appears → type 100 → **Add** → a "Reload — …" line is in the cart at Rs. 100. Then verify overdraw: enter an amount above the device float → inline error, no line. Tap **Bills** and **Float transactions** → their forms render inline with a Back.

- [ ] **Step 10: Commit**

```bash
git add plugins/recharge/recharge.go plugins/recharge/cashier.go plugins/recharge/pos.templ plugins/recharge/cashier_menu_test.go
git commit -m "feat(recharge): reload/bills/float as cashier menu nodes; drop ReloadPanel

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 4: Documents subtree (service detail leaves)

**Files:**
- Modify: `plugins/documents/documents.go` (register `CashierMenuRoot`; add `/cashier/documents/menu` + `/cashier/documents/job` routes).
- Modify: `plugins/documents/cashier.go` (`MenuRoot`, `JobFragment` handlers).
- Modify: `plugins/documents/pos.templ` (per-service job fragment from the existing `JobPanel`; remove `JobPanel`).

**Interfaces:**
- Consumes: `plugin.CashierMenuRoot`; node protocol.
- Produces: `GET /cashier/documents/menu` → service `detail` leaves (`detail_url=/cashier/documents/job?service=ID`); `GET /cashier/documents/job?service=ID` → the job form fragment.

- [ ] **Step 1: Implement `MenuRoot` returning service detail leaves**

In `plugins/documents/cashier.go`:
```go
func (h *cashierUI) MenuRoot(c echo.Context) error {
	svcs, err := h.p.store.Services(c.Request().Context())
	if err != nil { return err }
	nodes := make([]menuNode, 0, len(svcs))
	for _, s := range svcs {
		nodes = append(nodes, menuNode{Kind: "leaf", Name: s.Name, Emoji: "🖨", Action: "detail",
			DetailURL: "/cashier/documents/job?service=" + strconv.FormatInt(s.ID, 10)})
	}
	return c.JSON(http.StatusOK, map[string]any{"nodes": nodes})
}
```
(Reuse/define `menuNode` in the documents package, same JSON tags.)

- [ ] **Step 2: Split `JobPanel` into a per-service `JobFragment`**

`JobFragment(service)` renders the existing metered options form (size/colour/double/qty, server-priced via `/cashier/documents/prices`) for one service; it dispatches `pos-add-service` on Add exactly as the current `JobPanel` does. `JobHandler` (`GET /cashier/documents/job?service=ID`) renders it as a fragment. Delete `JobPanel`.

- [ ] **Step 3: Register the root + routes**

In `documents.go` Setup:
```go
reg.AddCashierMenuRoot(plugin.CashierMenuRoot{Key: "documents", Emoji: "🖨", Label: "Documents", ChildrenURL: "/cashier/documents/menu"})
reg.Cashier().GET("/documents/menu", ch.MenuRoot)
reg.Cashier().GET("/documents/job", ch.JobFragment)
```

- [ ] **Step 4: Build, deploy, and E2E-verify Documents**

Restart. In Playwright: `/cashier` → **🖨 Documents** → a service leaf → the job form renders inline with Back → set size/colour/qty → Add → a document line is in the cart, priced correctly.

- [ ] **Step 5: Commit**

```bash
git add plugins/documents/documents.go plugins/documents/cashier.go plugins/documents/pos.templ
git commit -m "feat(documents): services as cashier menu detail leaves; drop JobPanel

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 5: Both-plugins integration pass + cleanup

**Files:**
- Verify only; possibly `templates/pages/cashier/pos.templ` polish.

- [ ] **Step 1: Enable both plugins, full E2E**

With `recharge` + `documents` both enabled in `enabled_plugins.go` (uncommitted), rebuild + restart. At `/cashier` root confirm BOTH plugin cards (📶 Reload & Bills, 🖨 Documents) sit after the product groups. Walk each full flow (Reload add + overdraw block; a Documents job add; Bills + Float forms render). Confirm typing a product search still overrides the menu and Back from a plugin subtree returns to the root cleanly.

- [ ] **Step 2: Regression sweep**

Confirm: product-group drill-down unchanged; add-to-cart, discounts, tender, complete-sale unchanged; no console errors (`browser_console_messages`). `grep -rn "QuickActionTab\|ReloadPanel\|JobPanel" internal/ plugins/ templates/ | grep -v _templ.go` → no matches.

- [ ] **Step 3: Green build + vet**

Run: `make templ && go build ./... && go vet ./internal/... ./plugins/...`
Expected: clean.

- [ ] **Step 4: Commit any polish + push the branch**

```bash
git add -A ':!cmd/server/enabled_plugins.go' ':!static/css/tailwind.css'
git commit -m "test(cashier-menu): both-plugins integration verified; cleanup

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>" || echo "nothing to commit"
```

---

## Self-review notes (author)

- **Spec coverage:** node protocol (Task 2 + plan header), `CashierMenuRoot` hook (Task 1), remove strip (Task 1), recharge Reload full-card + Bills + Float detail (Task 3), documents detail (Task 4), root placement after groups (Task 2 Step 2 / Task 5 Step 1). Phase-2 (session lifecycle) intentionally **not** in this plan — separate plan after Phase-1 review.
- **Verification reality:** this codebase has no JS test harness; UI is verified by build + Playwright (matching current practice). Pure-Go node builders + reload amount validation get real unit tests (Task 3). 
- **Open item to confirm in Task 3 Step 1:** exact `/cashier` XHR error JSON shape — parsing in `confirmAmount()` may need adjusting once observed.
- **Assumption to verify during Task 3:** the existing recharge store exposes a device-by-carrier+purpose query (`DevicesFor(ctx, carrierID, "recharge")`); if the real method name differs, use it — the current cashier reload popup already fetches carrier devices, so the query exists.
