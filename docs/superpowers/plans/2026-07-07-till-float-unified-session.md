# Unified Session Lifecycle (till + float) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Capture recharge per-device float opening/closing inside the core till Open/Close dialogs so a shift is one open, one close, one logout.

**Architecture:** A generic core `DrawerSection` hook (URLs only, no plugin names) lets a plugin contribute plain-input HTML fragments into the till Open/Close dialogs. The cashier Alpine scope loads each fragment when a dialog opens and, around the drawer call, POSTs the fragment's inputs to the plugin's existing save endpoints (open = till-first, close = floats-first-abort-on-fail). Recharge fills the slot with its device float rows, slims its Reload & Bills tab to Bills + Float-transactions + read-only reconciliation, and drops its logout guard.

**Tech Stack:** Go, Echo v4, templ (`make templ`), Alpine.js, Tailwind (`make css`, CSS embedded in binary), plain `fetch`.

## Global Constraints

- Plugin → core only. `DrawerSection` is generic (a key + four URLs); core never references recharge/float. Verbatim naming: hook type `DrawerSection`, fields `Key, OpenFormURL, CloseFormURL, SaveOpenURL, SaveCloseURL`; client JSON keys `key, openFormUrl, closeFormUrl, saveOpenUrl, saveCloseUrl`.
- Never commit `cmd/server/enabled_plugins.go`, `static/css/tailwind.css`, `.claude/settings.local.json`. `_templ.go` is generated — never stage it.
- Assets are embedded: after any `.templ`/`.css`/`.js` change run `make templ && make css`, rebuild the binary, restart, before trusting the browser.
- No schema change; reuse existing recharge float tables and the existing `/cashier/recharge/open` (SaveOpening) and `/cashier/recharge/close` (SaveClosing) endpoints unchanged.
- Commit messages end with `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`.
- Fish shell: `pkill -f scratchpad/posserver` exits 144 — run it on its own line; start the server with `nohup … & disown`.

---

### Task 1: Core `DrawerSection` hook + JSON helper

**Files:**
- Modify: `internal/plugin/hooks.go` (add type near `CashierMenuRoot` ~line 86; var in the block ~line 104–107; `Add`/getter ~line 130–145; JSON helper after `CashierMenuRootsJSON` ~line 163)

**Interfaces:**
- Produces: `type DrawerSection struct { Key, OpenFormURL, CloseFormURL, SaveOpenURL, SaveCloseURL string }`; `func (r *Registry) AddDrawerSection(s DrawerSection)`; `func DrawerSections() []DrawerSection`; `func DrawerSectionsJSON() string` → `[{"key","openFormUrl","closeFormUrl","saveOpenUrl","saveCloseUrl"}]`.

- [ ] **Step 1: Add the type** (after the `CashierMenuRoot` struct)

```go
// DrawerSection contributes an extra panel to the till OPEN and CLOSE dialogs
// (e.g. a plugin's per-session sub-ledger the cashier counts alongside the
// drawer). Core renders an empty slot; client-side it loads each section's form
// fragment and posts it to the section's save URL around the drawer call. The
// fragment is plain inputs (no hx-*); core never references the plugin's domain.
type DrawerSection struct {
	Key          string // stable id, e.g. "recharge"
	OpenFormURL  string // GET → HTML input rows for the Open-till dialog
	CloseFormURL string // GET → HTML input rows for the Close-register dialog
	SaveOpenURL  string // POST (form-encoded) target, after the till opens
	SaveCloseURL string // POST (form-encoded) target, before the till closes
}
```

- [ ] **Step 2: Add the backing slice** (in the `var (` block with the other hook slices)

```go
	drawerSections   []DrawerSection
```

- [ ] **Step 3: Add register + getter** (beside `AddCashierMenuRoot` / `CashierMenuRoots`)

```go
func (r *Registry) AddDrawerSection(s DrawerSection) { drawerSections = append(drawerSections, s) }
```
```go
func DrawerSections() []DrawerSection { return drawerSections }
```

- [ ] **Step 4: Add the JSON helper** (after `CashierMenuRootsJSON`)

```go
// DrawerSectionsJSON renders the drawer sections for the cashier Alpine scope:
// [{"key":"recharge","openFormUrl":"…","closeFormUrl":"…","saveOpenUrl":"…","saveCloseUrl":"…"}].
func DrawerSectionsJSON() string {
	type s struct {
		Key          string `json:"key"`
		OpenFormURL  string `json:"openFormUrl"`
		CloseFormURL string `json:"closeFormUrl"`
		SaveOpenURL  string `json:"saveOpenUrl"`
		SaveCloseURL string `json:"saveCloseUrl"`
	}
	out := make([]s, 0, len(drawerSections))
	for _, d := range drawerSections {
		out = append(out, s{d.Key, d.OpenFormURL, d.CloseFormURL, d.SaveOpenURL, d.SaveCloseURL})
	}
	b, _ := json.Marshal(out)
	return string(b)
}
```

- [ ] **Step 5: Build**

Run: `go build ./internal/plugin/...`
Expected: no output (success).

- [ ] **Step 6: Commit**

```bash
git add internal/plugin/hooks.go
git commit -m "feat(plugin): DrawerSection hook for till open/close dialog panels

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: Core wires sections into the till dialogs (`pos.templ` + `app.js`)

**Files:**
- Modify: `templates/pages/cashier/pos.templ` (x-data call site line 23; Open overlay ~line 58–71; Close modal ~line 538–553)
- Modify: `static/js/app.js` (`pos()` signature line 265; state block ~line 291; `init()` ~line 344; `startClose()` ~line 703; `openRegister()` ~line 687; `submitClose()` ~line 708)

**Interfaces:**
- Consumes: `plugin.DrawerSectionsJSON()` (Task 1).
- Produces: `pos(symbol, defaultType, askToPrint, pluginRoots, drawerSections)`; Alpine refs `openSections` / `closeSections`; methods `loadDrawerSections(which)`, `saveDrawerSections(which)`.

- [ ] **Step 1: Pass sections into `pos()`** — `pos.templ` line 23, append the new arg:

```go
x-data={ "pos('" + d.Symbol + "', '" + d.DefaultSaleType + "', " + jsBool(d.AskToPrint) + ", " + plugin.CashierMenuRootsJSON() + ", " + plugin.DrawerSectionsJSON() + ")" }
```

- [ ] **Step 2: Add the Open-dialog slot** — in the Open Register overlay, immediately AFTER the denomination total line (the `<span x-text="sym + ' ' + money(countTotal(openCounts))">` block, ~line 58–59) and BEFORE the locker/source picker, insert:

```html
<div x-ref="openSections" class="space-y-3 mt-3"></div>
```

- [ ] **Step 3: Add the Close-dialog slot** — in the Close Register modal, AFTER the denomination close total (`countTotal(closeCounts)` block ~line 538) and BEFORE the Cancel/Close buttons row (~line 552), insert:

```html
<div x-ref="closeSections" class="space-y-3 mt-3"></div>
```

- [ ] **Step 4: Extend the `pos()` signature and state** — `app.js`:

Change line 265 from `function pos(symbol, defaultType, askToPrint, pluginRoots) {` to:
```js
function pos(symbol, defaultType, askToPrint, pluginRoots, drawerSections) {
```
Add to the returned state object (near `pluginRoots: pluginRoots || [],` ~line 281):
```js
    // Plugin drawer sections (e.g. recharge float): input fragments loaded into the
    // till Open/Close dialogs, saved to each section's endpoint around the drawer call.
    drawerSections: drawerSections || [],
```

- [ ] **Step 5: Add load/save helpers** — `app.js`, add these methods inside the `pos()` return object (e.g. right after `loadLockers()` ~line 372):

```js
    // Load each plugin drawer section's input fragment into the Open ('open') or
    // Close ('close') dialog slot. Fragments are plain inputs (no hx-*), so no
    // Alpine/HTMX init is needed — saveDrawerSections reads their values directly.
    async loadDrawerSections(which) {
      const box = which === "open" ? this.$refs.openSections : this.$refs.closeSections;
      if (!box) return;
      box.innerHTML = "";
      for (const s of this.drawerSections) {
        const url = which === "open" ? s.openFormUrl : s.closeFormUrl;
        if (!url) continue;
        try {
          const res = await fetch(url, { credentials: "same-origin" });
          if (!res.ok) continue;
          const wrap = document.createElement("div");
          wrap.setAttribute("data-drawer-save", which === "open" ? s.saveOpenUrl : s.saveCloseUrl);
          wrap.innerHTML = await res.text();
          box.appendChild(wrap);
        } catch (_) {
          /* a missing section just doesn't render */
        }
      }
    },
    // POST each loaded section's inputs (form-encoded) to its save URL. Returns
    // false if any 'close' save failed (caller aborts the till close). 'open'
    // saves are best-effort (reconciliation auto-carries the last close).
    async saveDrawerSections(which) {
      const box = which === "open" ? this.$refs.openSections : this.$refs.closeSections;
      if (!box) return true;
      const wraps = box.querySelectorAll("[data-drawer-save]");
      for (const w of wraps) {
        const url = w.getAttribute("data-drawer-save");
        if (!url) continue;
        const params = new URLSearchParams();
        w.querySelectorAll("input[name], select[name], textarea[name]").forEach((el) => {
          if (String(el.value).trim() !== "") params.append(el.name, el.value);
        });
        try {
          const res = await fetch(url, {
            method: "POST",
            credentials: "same-origin",
            headers: { "Content-Type": "application/x-www-form-urlencoded" },
            body: params.toString(),
          });
          if (!res.ok && which === "close") {
            const j = await res.json().catch(() => ({}));
            toast((j.error && j.error.message) || "Could not save float counts", "error");
            return false;
          }
        } catch (_) {
          if (which === "close") {
            toast("Could not save float counts", "error");
            return false;
          }
        }
      }
      return true;
    },
```

- [ ] **Step 6: Load open sections when the drawer is closed** — in `init()` (~line 344), after `await this.loadLockers();` and before the `logoutMode` block, add:

```js
      if (!this.session) await this.loadDrawerSections("open");
```

- [ ] **Step 7: Save open sections after the till opens** — in `openRegister()` (~line 687), after `await this.loadLockers();` and before `window.dispatchEvent(new CustomEvent("register-opened"));`, add:

```js
      await this.saveDrawerSections("open");
```

- [ ] **Step 8: Load close sections when the close dialog opens** — change `startClose()` (~line 703) to:

```js
    startClose() {
      this.closeCounts = {};
      this.closeResult = null;
      this.showClose = true;
      this.loadDrawerSections("close");
    },
```

- [ ] **Step 9: Save close sections before closing the till** — in `submitClose()` (~line 708), make it the FIRST line inside the method (before the `apiFetch("POST", "/api/cash-register/close"…)`):

```js
    async submitClose() {
      if (!(await this.saveDrawerSections("close"))) return;
      const json = await apiFetch("POST", "/api/cash-register/close", {
```
(the rest of `submitClose` is unchanged.)

- [ ] **Step 10: Build assets + binary**

```bash
make templ && make css
go build -o /tmp/claude-1000/-home-karots-Projects-go-karots-pos/0059bd78-27b3-48e5-8c81-1868ebd3f592/scratchpad/posserver ./cmd/server
```
Expected: both succeed, no output from `go build`.

- [ ] **Step 11: Commit**

```bash
git add templates/pages/cashier/pos.templ static/js/app.js
git commit -m "feat(cashier): load+save plugin drawer sections in till open/close dialogs

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: Recharge fills the slot (fragments + routes + register) and drops the logout guard

**Files:**
- Create fragments in: `plugins/recharge/recon.templ` (two new templ funcs `openFloatFields`, `closeFloatFields`)
- Modify: `plugins/recharge/cashier.go` (two handlers `DrawerOpenFields`, `DrawerCloseFields`)
- Modify: `plugins/recharge/recharge.go` (register two routes + `AddDrawerSection`; delete the `AddLogoutGuard` block ~line 93)

**Interfaces:**
- Consumes: `plugin.DrawerSection` (Task 1); existing `store.Carriers`, `store.Devices`, `h.reconData`, `h.symbol`, `CarrierRecon`/`DeviceRecon`, helpers `openingValue`/`closingValue`.
- Produces: routes `GET /cashier/recharge/drawer/open` → `DrawerOpenFields`, `GET /cashier/recharge/drawer/close` → `DrawerCloseFields`. Save URLs reuse existing `POST /cashier/recharge/open` and `POST /cashier/recharge/close`.

- [ ] **Step 1: Add the two input-only templ fragments** — append to `plugins/recharge/recon.templ`:

```go
// openFloatFields renders per-device opening float inputs for the till Open
// dialog. Left blank the server carries the last close forward, so these are an
// override; the value is not prefilled to avoid implying a counted balance.
templ openFloatFields(d ReconData) {
	if len(d.Carriers) > 0 {
		<div class="border-t pt-3 space-y-3">
			<div class="text-sm font-semibold text-slate-600">Reload float — opening</div>
			<p class="text-xs text-slate-400">Leave a device blank to carry its last closing balance forward.</p>
			for _, cr := range d.Carriers {
				<div>
					<div class="text-xs font-medium text-slate-500 mb-1">{ cr.Name }</div>
					for _, dev := range d.Devices {
						if dev.CarrierID == cr.ID && dev.ForRecharge {
							<div class="flex items-center gap-2 mb-1">
								<span class="flex-1 text-sm">{ dev.Label }</span>
								<input
									type="number" min="0" step="0.01"
									name={ "opening_" + strconv.FormatInt(dev.ID, 10) }
									placeholder="0.00"
									class="w-28 border rounded-lg px-2 py-1.5 text-right"
								/>
							</div>
						}
					}
				</div>
			}
		</div>
	}
}

// closeFloatFields renders per-device closing float count inputs for the till
// Close dialog, showing each device's expected (live) balance to count against.
templ closeFloatFields(d ReconData) {
	if len(d.Rows) > 0 {
		<div class="border-t pt-3 space-y-3">
			<div class="text-sm font-semibold text-slate-600">Reload float — count closing</div>
			for _, cr := range d.Rows {
				<div>
					<div class="text-xs font-medium text-slate-500 mb-1">{ cr.Carrier }</div>
					for _, dev := range cr.Devices {
						<div class="flex items-center gap-2 mb-1">
							<span class="flex-1 text-sm">
								{ dev.Label }
								<span class="text-slate-400 text-xs">· expected { money.Format(d.Symbol, dev.Expected) }</span>
							</span>
							<input
								type="number" min="0" step="0.01"
								name={ "closing_" + strconv.FormatInt(dev.DeviceID, 10) }
								value={ closingValue(dev) }
								placeholder="0.00"
								class="w-28 border rounded-lg px-2 py-1.5 text-right"
							/>
						</div>
					}
				</div>
			}
		</div>
	}
}
```

Note: confirm `Device` has fields `ID int64`, `CarrierID int64`, `Label string`, `ForRecharge bool` (grep `type Device struct` in `plugins/recharge/`); if the purpose flag differs (e.g. `Purpose`/`ForMoney`), adjust the `dev.ForRecharge` guard to the actual field. If `Device` lacks a display `Label`, use its name field.

- [ ] **Step 2: Add the two handlers** — in `plugins/recharge/cashier.go` (near `MenuBill`):

```go
// DrawerOpenFields renders the opening-float inputs for the core till Open dialog
// (registered as a DrawerSection.OpenFormURL). No session exists yet, so it lists
// devices with blank opening overrides.
func (h *cashierUI) DrawerOpenFields(c echo.Context) error {
	d, err := h.reconData(c)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, openFloatFields(d))
}

// DrawerCloseFields renders the closing-float count inputs for the core till
// Close dialog (registered as a DrawerSection.CloseFormURL), showing each open
// device's expected balance. Requires the still-open till session.
func (h *cashierUI) DrawerCloseFields(c echo.Context) error {
	d, err := h.reconData(c)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, closeFloatFields(d))
}
```

- [ ] **Step 3: Register routes + the DrawerSection, and drop the logout guard** — in `plugins/recharge/recharge.go`:

Add beside the other cashier routes (after the `menu/float` line):
```go
	reg.Cashier().GET("/recharge/drawer/open", ch.DrawerOpenFields)
	reg.Cashier().GET("/recharge/drawer/close", ch.DrawerCloseFields)
```
Add beside `AddCashierMenuRoot`:
```go
	// Float opening/closing rides the core till Open/Close dialogs via this section;
	// the save URLs are the existing recon endpoints (open = SaveOpening, close =
	// SaveClosing). No separate float open/close step or logout guard is needed.
	reg.AddDrawerSection(plugin.DrawerSection{
		Key:          "recharge",
		OpenFormURL:  "/cashier/recharge/drawer/open",
		CloseFormURL: "/cashier/recharge/drawer/close",
		SaveOpenURL:  "/cashier/recharge/open",
		SaveCloseURL: "/cashier/recharge/close",
	})
```
DELETE the entire `reg.AddLogoutGuard(func(ctx context.Context, userID int64) …{ … })` block (~line 93) and its explanatory comment. If this removes the only use of an import (e.g. `context`), remove that import too; `go build` will flag it.

- [ ] **Step 4: Build**

```bash
make templ
go build -o /tmp/claude-1000/-home-karots-Projects-go-karots-pos/0059bd78-27b3-48e5-8c81-1868ebd3f592/scratchpad/posserver ./cmd/server
```
Expected: success. If `go build` complains about an unused import in `recharge.go`, remove it and rebuild.

- [ ] **Step 5: Commit**

```bash
git add plugins/recharge/recon.templ plugins/recharge/cashier.go plugins/recharge/recharge.go
git commit -m "feat(recharge): float open/close via till drawer section; drop logout guard

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 4: Slim the Reload & Bills tab to read-only reconciliation

**Files:**
- Modify: `plugins/recharge/recon.templ` (`ReconBody` lines 21–73; add a read-only `carrierSummary` templ)

**Interfaces:**
- Consumes: `CarrierRecon`, `money.Format`, existing `stat`/`bonusStat` templs.
- Produces: `templ carrierSummary(symbol string, cr CarrierRecon)` (stats only).

- [ ] **Step 1: Replace the editable float form with a read-only summary** — in `ReconBody`, replace the whole `if len(d.Rows) == 0 { … } else { <form id="recon-floats">…</form> <div class="flex flex-wrap gap-3">…Save opening…Save closing…</div> }` block (lines 38–70) with:

```go
			if len(d.Rows) == 0 {
				<div class="bg-white rounded-2xl shadow-sm p-6 text-slate-400 text-center">
					No carriers set up. Add carriers and devices in Admin → Reload & Bills.
				</div>
			} else {
				<div class="text-sm text-slate-500">
					Float opening &amp; closing are counted in the till Open / Close dialog. This is the current reconciliation.
				</div>
				for _, cr := range d.Rows {
					@carrierSummary(d.Symbol, cr)
				}
			}
```

- [ ] **Step 2: Add the read-only `carrierSummary` templ** — append to `recon.templ` (it reuses the existing `stat`/`bonusStat`):

```go
// carrierSummary is the read-only reconciliation view for one carrier on the
// slimmed Reload & Bills tab (opening/closing are now counted in the till
// dialog). It shows the computed stats without editable inputs.
templ carrierSummary(symbol string, cr CarrierRecon) {
	<div class="bg-white rounded-2xl shadow-sm p-6">
		<div class="flex items-center justify-between mb-3">
			<h3 class="text-base font-semibold">{ cr.Carrier }</h3>
			if cr.AllClosed {
				<span class="text-xs px-2 py-0.5 rounded-full bg-slate-100 text-slate-500">closed</span>
			}
		</div>
		<dl class="grid grid-cols-2 sm:grid-cols-3 gap-x-6 gap-y-1 text-sm">
			@stat("Opening", money.Format(symbol, cr.Opening))
			@stat("Reload in", money.Format(symbol, cr.FloatIn))
			@stat("Reload out", money.Format(symbol, cr.FloatOut))
			@stat("Expected", money.Format(symbol, cr.Expected))
			if cr.AllClosed {
				@stat("Counted close", money.Format(symbol, cr.Closing))
				@bonusStat(symbol, cr.BonusLoss)
			}
		</dl>
	</div>
}
```

- [ ] **Step 3: Remove now-unused `carrierBlock`** — if `carrierBlock` (and only it) is now unreferenced, delete the `templ carrierBlock(...)` block. Keep `openingValue`/`closingValue` (still used by the drawer fragments in Task 3). Run `grep -n "carrierBlock" plugins/recharge/` to confirm no other reference before deleting.

- [ ] **Step 4: Build**

```bash
make templ
go build -o /tmp/claude-1000/-home-karots-Projects-go-karots-pos/0059bd78-27b3-48e5-8c81-1868ebd3f592/scratchpad/posserver ./cmd/server
```
Expected: success. A `declared and not used` / unused-templ error means a leftover reference — fix per the message.

- [ ] **Step 5: Commit**

```bash
git add plugins/recharge/recon.templ
git commit -m "refactor(recharge): read-only reconciliation on Reload & Bills tab

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 5: Build, run, and manual E2E verification

**Files:** none (verification only). Recharge + documents must be enabled locally in `cmd/server/enabled_plugins.go` (do NOT commit it).

- [ ] **Step 1: Full rebuild + restart**

```bash
make templ && make css
go build -o /tmp/claude-1000/-home-karots-Projects-go-karots-pos/0059bd78-27b3-48e5-8c81-1868ebd3f592/scratchpad/posserver ./cmd/server
```
```bash
pkill -f "scratchpad/posserver"
```
```bash
set -a; source .env; set +a; nohup /tmp/claude-1000/-home-karots-Projects-go-karots-pos/0059bd78-27b3-48e5-8c81-1868ebd3f592/scratchpad/posserver > /tmp/claude-1000/-home-karots-Projects-go-karots-pos/0059bd78-27b3-48e5-8c81-1868ebd3f592/scratchpad/server.log 2>&1 & disown
```
Expected: log shows `POS server listening on :3000` and both plugins' migrations applied.

- [ ] **Step 2: Open flow** — log in as cashier (0771111111 / 1111). With the till closed, the Open Register overlay shows the denomination count AND a "Reload float — opening" section per carrier/device. Enter a cash count + a device opening (e.g. 5000) and Open Register. Verify: register opens, and `psql` shows the opening recorded:

```bash
docker exec pos_db psql -U pos_user -d pos_db -c "select session_id, device_id, opening, closing, closed_at from recharge_device_sessions order by id desc limit 5;"
```
Expected: a row for this session with the entered `opening`.

- [ ] **Step 3: Close flow** — do a reload sale if desired, then Close Register. Verify the close modal shows the denomination count AND a "Reload float — count closing" section listing each device's expected balance. Enter closing counts + cash count and Close & Reconcile. Expected: close succeeds; the reconciliation (bonus/loss) reflects the counts; no error toast.

- [ ] **Step 4: One logout** — with a fresh till open + float open, click Log out. Expected: the logout is guarded ONLY by the core "till still open" flow (it routes to the close/count dialog), and once closed (floats counted in the same dialog) logout completes — no separate bounce to the Reload & Bills tab.

- [ ] **Step 5: Slimmed tab** — open the Reload & Bills nav tab. Expected: Bill payment form + Float-transaction form are present and still work; the per-carrier reconciliation is shown read-only (no opening/closing inputs, no Save opening/closing buttons).

- [ ] **Step 6: Core regression (recharge disabled)** — sanity: the Open/Close dialogs render with empty section slots and behave exactly as before when no plugin registers a section (verified by the `if len(d.Carriers/Rows) …` guards; no code path renders a section for a plugin that registered none).

- [ ] **Step 7: Report result to the user** for live testing; do not commit `enabled_plugins.go` / `tailwind.css`.

---

## Self-Review

- **Spec coverage:** §1 hook → Task 1. §2 dialogs load/save + open-till-first/close-floats-first → Task 2. §3 recharge fragments + reuse save endpoints + register → Task 3. §4 slim tab read-only → Task 4. §5 drop logout guard → Task 3 Step 3. Testing → Task 5. All covered.
- **Placeholder scan:** the only conditional notes are explicit "confirm the actual field/schema name and adjust" guards (Task 3 Step 1, Task 5 Step 2) because the recharge `Device` struct / float table columns weren't read verbatim — each says exactly what to check and how to adapt. No TBD/TODO left as work.
- **Type consistency:** `DrawerSection` fields and JSON keys match between Task 1 and Task 3; `pos(...)` 5-arg signature matches between Task 2 Step 1 (templ) and Step 4 (js); `loadDrawerSections`/`saveDrawerSections` names consistent across Task 2 steps; save URLs (`/cashier/recharge/open`, `/close`) match the existing handlers named in the spec.
