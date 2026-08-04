# Till Open/Close side-by-side float Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Lay the reload-plugin float beside the cash count (not below it) in the till Open/Close dialogs when a plugin float is present, and make all denominations visible — so cashiers stop missing coins.

**Architecture:** Front-end only. `templates/pages/cashier/pos.templ` gets Alpine `:class` bindings keyed on the existing `drawerSections` array (already in the POS scope). No server, route, JS-logic, or plugin change. `app.js` `loadDrawerSections`/`saveDrawerSections` and their `x-ref` containers are untouched.

**Tech Stack:** Go templ, Alpine.js, Tailwind (utility classes), `make build` (regenerates templ + re-embeds static assets into the binary).

## Global Constraints

- Front-end only — do NOT edit `app.js`, any `.go` file, routes, or plugins.
- `templates/pages/cashier/pos_templ.go` is generated and **gitignored** — never `git add` it.
- Front-end changes are embedded via `go:embed`, so they require `make build` + a server restart to appear.
- No-plugin path must stay byte-identical to today: narrow `max-w-md`, single cash-only column, empty `x-ref` float div still present in the DOM.

---

### Task 1: Two-column layout + taller denom list in both till dialogs

**Files:**
- Modify: `templates/pages/cashier/pos.templ` — Open Register overlay (~L33-79) and Close Register modal (~L620-661)

**Interfaces:**
- Consumes: `drawerSections` (array, already in the Alpine `pos(...)` scope via `plugin.DrawerSectionsJSON()`); the existing `x-ref="openSections"` and `x-ref="closeSections"` float containers.
- Produces: no new JS/Go interface. Purely markup/class changes.

- [ ] **Step 1: Open overlay — conditional card width.**
  In the Open Register overlay, on the inner card `<div>` (currently
  `class="bg-white rounded-2xl shadow p-6 w-full max-w-md max-h-full overflow-auto"`),
  remove `max-w-md` from the static class and add an Alpine bind:

  ```html
  <div class="bg-white rounded-2xl shadow p-6 w-full max-h-full overflow-auto"
       :class="drawerSections.length ? 'max-w-3xl' : 'max-w-md'">
  ```

- [ ] **Step 2: Open overlay — wrap cash + float in a responsive grid.**
  Wrap the cash block (the denom-list `<div class="space-y-1.5 …">`, the "Opening cash"
  total row, and the `take cash from` locker `<template x-if="lockers.length">`) together
  with the float container `<div x-ref="openSections" …>` inside one grid. The
  `<h2>`/subtitle and reopen-options row stay ABOVE the grid; the `openRegister()` button
  stays BELOW it. Structure:

  ```html
  <div class="grid gap-5 mt-3" :class="drawerSections.length ? 'lg:grid-cols-2' : 'grid-cols-1'">
    <div><!-- LEFT: denom list + total + locker select --></div>
    <div x-ref="openSections" class="space-y-3"></div><!-- RIGHT: float -->
  </div>
  ```
  Move the existing denom list, total row, and locker `<template>` into the LEFT div
  unchanged except for the denom-list height in Step 3. Keep the `x-ref="openSections"`
  element exactly (same ref name) as the RIGHT div — drop its old `mt-4` since the grid
  now owns spacing.

- [ ] **Step 3: Open overlay — taller denom list.**
  On the Open denom-list wrapper, change `class="space-y-1.5 max-h-72 overflow-auto pr-1"`
  to `class="space-y-1.5 max-h-[55vh] overflow-auto pr-1"`.

- [ ] **Step 4: Close modal — conditional card width.**
  In the Close Register modal, on the inner card `<div>` (currently
  `class="bg-white rounded-2xl shadow-xl w-full max-w-md max-h-full overflow-auto p-6"`),
  remove `max-w-md` and add the same bind:

  ```html
  <div class="bg-white rounded-2xl shadow-xl w-full max-h-full overflow-auto p-6"
       :class="closeResult ? 'max-w-md' : (drawerSections.length ? 'max-w-3xl' : 'max-w-md')">
  ```
  (The reconciliation result view `closeResult` stays narrow — only the count step widens.)

- [ ] **Step 5: Close modal — wrap cash + float in a responsive grid.**
  Inside `<template x-if="!closeResult">`, wrap the denom-list `<div class="space-y-1.5 …">`,
  the "Counted" total row, and the `Bank cash into` locker `<template x-if="lockers.length">`
  together with `<div x-ref="closeSections" …>` in one grid. The logout warning, `<h3>`,
  and subtitle stay ABOVE; the Cancel / Close & Reconcile button row stays BELOW.

  ```html
  <div class="grid gap-5 mt-3" :class="drawerSections.length ? 'lg:grid-cols-2' : 'grid-cols-1'">
    <div><!-- LEFT: denom list + total + locker select --></div>
    <div x-ref="closeSections" class="space-y-3"></div><!-- RIGHT: float -->
  </div>
  ```
  Keep the `x-ref="closeSections"` ref name exactly; drop its old `mt-4`.

- [ ] **Step 6: Close modal — taller denom list.**
  On the Close denom-list wrapper, change `class="space-y-1.5 max-h-72 overflow-auto pr-1"`
  to `class="space-y-1.5 max-h-[55vh] overflow-auto pr-1"`.

- [ ] **Step 7: Build.**
  Run: `make build`
  Expected: templ regenerates and the Go binary rebuilds with no errors. (If `make build`
  is unavailable, `templ generate` then `go build ./...`.)

- [ ] **Step 8: Restart the dev server and verify live (recharge plugin enabled).**
  Restart: `set -a; . ./.env; set +a; ./bin/karots-pos` (background), browse http://localhost:3000.
  With Playwright, sign in as a cashier and:
  - **Open Register:** confirm two columns — cash on the left, reload float on the right;
    every denomination incl. the lowest-value coins visible without an inner scrollbar on a
    normal viewport; no page-level vertical scroll.
  - **Close Register:** confirm the same two-column layout (count left, float right).

- [ ] **Step 9: Verify the no-plugin path collapses correctly.**
  On the open dialog, via Playwright `browser_evaluate`, set the Alpine scope's
  `drawerSections = []` (e.g. on the root `x-data` element's `_x_dataStack[0]`) and confirm
  the card narrows to `max-w-md` and the layout becomes a single cash-only column (the empty
  `openSections` div renders nothing). This proves the compile-time no-plugin build without a
  rebuild. Reload afterwards to restore state.

- [ ] **Step 10: Regression — float still saves.**
  Complete a real Open (enter float counts on the right) and a Close (enter drawer counts +
  float close counts), and confirm the session opens and the reconciliation posts — proving
  `saveDrawerSections` still reads the (now relocated) `x-ref` inputs. The refs are unchanged,
  so this should pass; verify it does.

- [ ] **Step 11: Commit.**

  ```bash
  git add templates/pages/cashier/pos.templ
  git commit -m "feat(till): side-by-side cash + reload float in open/close, taller denom list"
  ```
  (Do NOT add `pos_templ.go` — it is gitignored.)

---

## Self-Review

**Spec coverage:**
- Conditional width (`max-w-3xl` vs `max-w-md`) → Steps 1, 4. ✓
- Two-column grid when float present, stacks on mobile → Steps 2, 5. ✓
- No-plugin path unchanged, empty ref div stays → Steps 2/5 (ref kept) + Step 9 (verified). ✓
- Taller denom list so coins visible → Steps 3, 6. ✓
- No `app.js`/server/plugin change → Global Constraints + Interfaces. ✓
- Verification (build, live E2E both dialogs, empty-state, regression) → Steps 7-10, mirrors spec §Verification. ✓

**Placeholder scan:** none — every step gives the exact class string / structure.

**Type consistency:** `x-ref` names `openSections` / `closeSections` preserved verbatim; `drawerSections` matches the Alpine scope property. No new symbols introduced.
