# 📋 Handoff — status

**Everything compiles** (`go build ./...` clean, `go vet ./...` clean,
`templ generate` clean). Phases 2–8 are wired, routed, and **smoke-tested live
over HTTP** (incl. through the final 15M static binary). DB is at migration **13**
and seeded. Go module is **`karots-pos`**.

## ✅ Phase 8 — owner-requested add-ons (migrations 0012–0013, all live-tested)

Seven features the owner picked after the Phase-7 review:

| Feature | Where | Verified |
|---|---|---|
| **Split-tender payments** (cash/card/online + ref) | `pos()` sends `payments[]`; `pos.templ` payment rows | sale 400 → cash 200 + card 200 (ref stored) |
| **Cashier receipt reprint** | `/cashier/receipts` (`sales.ListFilter.Receipt`) | found S-00015 + Reprint link |
| **Hold / park sale** | feature `heldsales`, `/api/held-sales`, Held(n) modal | hold→list→resume→delete (cart JSON round-trips) |
| **Z-report (day-end)** | `sales.PeriodSummary`, `cashierpages.ZReport`, `/cashier/z/:id` + `/admin/cash-register/:id/z` | 200, sections render, by-method totals |
| **Audit log** | feature `audit`, `Server.logAudit`, `/admin/audit` | product update → `Admin / update / product` row |
| **Customer dues report** | `customers.Owing`, `/admin/reports/customer-dues` | owing customer listed + total receivable |
| **Backup / restore** | `internal/backup` (pure Go) + `internal/web/admin_backup.go`, Settings card | full round-trip: backup→mutate→restore reverts, sequences intact |

**Notes:** held-sale carts are JSONB round-tripped with `json.RawMessage` (JSON
passes through, never base64). Audit recording is best-effort
(`audit.Service.Record` logs+swallows errors — never fails a mutation); cash
withdraw/close are audited inside `cashregister` via `.WithAudit(...)`.

**Backup/restore is pure Go over the existing DB connection** (`internal/backup`),
NOT pg_dump/psql and NOT docker exec — so it works identically whether Postgres
is a local container or a remote VPS, with nothing to install. It dumps DATA only
(schema is owned by the embedded migrations that run on startup) as a gzipped
JSON archive: every column is read with `::text` and re-inserted as a text
parameter, so Postgres' own I/O functions handle numeric/timestamptz/jsonb/enum
exactly, driver-independently. Restore runs in one tx: `SET LOCAL
session_replication_role='replica'` (disable FK — needs a privileged role; true
for the standard docker Postgres user), `TRUNCATE … RESTART IDENTITY CASCADE`,
reload, then `setval` each serial sequence. Verified: backup→add sale/customer→
restore reverts counts and a subsequent insert gets a fresh non-colliding id.

## ✅ Phase 7 — Management reports (printable / PDF, all live-tested)

A **Reports** hub (`/admin/reports`, sidebar "All Reports (print)") of management
reports, each **filterable** and **printable / saveable as PDF**. Dependency-free:
print-optimized HTML pages + browser "Save as PDF" (`window.print()`), matching
the receipt/label print pattern — no CGO, no PDF lib, still one static binary.

Each report uses `layouts.Report(title, shopName)`: a `.no-print` toolbar with a
"🖨 Print / Save PDF" button + Back, a header (shop name, title, generated-at), an
in-page filter form (hidden in print), the table, and a totals `<tfoot>`. Print
CSS (`.report-sheet`/`.no-print` in `app.css`) renders a clean full-width sheet.

| Report | Route | Filters | Verified |
|---|---|---|---|
| Hub | `/admin/reports` | — | ✅ 200, cards for every report |
| Sales | `/admin/reports/sales` | from/to + status | ✅ 13 sales listed, totals foot |
| Finance / P&L | `/admin/reports/finance` | from/to | ✅ revenue→net + top products |
| Cash register | `/admin/reports/cash-register` | from/to | ✅ 8 sessions, over/short total −7750 |
| Purchases | `/admin/reports/purchases` | from/to (in-memory) | ✅ 200 + totals (total/paid/due) |
| Suppliers | `/admin/reports/suppliers` | snapshot | ✅ payables + total |
| Inventory valuation | `/admin/reports/inventory` | snapshot | ✅ cost value 50,840.00 = DB |
| Batches / expiry | `/admin/reports/batches` | days (expiring within) | ✅ 200, value total |
| Low stock / Expiring | existing routes | — | ✅ linked from hub |

Plumbing added: `stock.AllBatches`/repo `AllLiveBatches`;
`cashregister.SessionsInRange`; `internal/web/admin_reports.go` (handlers +
`rangeStrings`/`shopName` helpers, reuses `reports.ParseRange`); templates
`templates/pages/admin/mgmt_reports.templ` + `templates/layouts/report.templ`.

## ✅ Phase 6 — cash management (denominations + drawer ledger, all live-tested)

Migration **0011**: `denominations` (seeded LKR coins 1–10 + notes 20–5000),
`cash_movements` (enum `cash_movement_type`, signed amounts + reason + JSONB
breakdown), and `cash_register.opening_breakdown/closing_breakdown` JSONB.

| Area | What | Verified live |
|---|---|---|
| **Denomination CRUD (admin)** | `/admin/denominations` + `denominations` feature (`/api/denominations`, `?all=1` for inactive). Add/edit/retire notes & coins | ✅ add 2000, retire/reactivate 1; cashier list shows active only |
| **Open by counting** | POS open overlay = per-denomination count grid, live total → opening float; `POST /api/cash-register/open {breakdown}` (server totals authoritatively) | ✅ counts {5000:1,1000:2,100:5} → opening 7500 |
| **Reopen options** | overlay offers **Continue last (Rs X)** (prefills from last close's breakdown via `summary.last_breakdown`) and **Count fresh** | ✅ summary returns last_closing 7320 + base64 breakdown |
| **Mid-shift withdrawal** | Withdraw modal (amount + reason) → `POST /api/cash-register/withdraw` (negative movement) | ✅ −500 banked, lowers expected |
| **Close by counting** | Close modal count grid → `POST /close {breakdown}`; shows cash sales / expected / counted / over-short | ✅ counted 7320 = expected 7320, diff 0 |
| **Live drawer + all-cash tracking** | `GET /api/cash-register/summary` = opening + cash sales + pay-ins/credit − withdrawals; every event in `cash_movements`. Credit collected on `/cashier/credit` posts a `credit_payment` movement into the open drawer | ✅ pay_in 20 reflected; expected live |
| **Admin drawer audit** | `/admin/cash-register` session list (opening/expected/counted/over-short) + `/admin/cash-register/:id` detail (movements + opening/closing denomination counts) | ✅ list+detail 200, shows withdrawal/credit/banked + counts |
| **Finance integration** | `reports.PL` gains `CashWithdrawn` + `RegisterDiff`; finance page shows "Cash Withdrawn (drawer)" and "Register Over/Short" cards | ✅ cards render |

**Fix (correctness):** `sales.CashCollectedSince` now nets out `change_given`
(change is always cash), so an overpaid cash sale no longer overstates the
expected drawer. Verified: sale 300 tendered 500 → drawer +300, not +500.

**Wiring:** `denominations.RegisterAPI` + extended `cashregister` in `main.go`;
web `Server` holds `denominations` + `cashRegister` services (the latter records
credit cash); admin handlers in `internal/web/admin_cash.go`; templates
`templates/pages/admin/{denominations,cash}.templ`; POS counting/withdraw/close
UX in `static/js/app.js` (`pos()`) + `templates/pages/cashier/pos.templ`.

## ✅ Phase 5 — phone login + cashier operations (all live-tested)

| Area | What | Verified live |
|---|---|---|
| **Phone + PIN login** | login is now **phone number + PIN** (was a user-picker + PIN). Phone is unique per user; no staff list on the login screen. `auth.LoginInput{Phone,PIN}`, `Repository.FindByPhone`, migration **0010** (backfills seeded users, `UNIQUE` + `NOT NULL` on `users.phone`) | ✅ admin `0771234567/1234`→/admin, cashier `0771111111/1111`→/cashier, wrong PIN→401 |
| **Auto role routing** | no admin/cashier toggle — server redirects to `auth.HomePath(role)` after the PIN verifies | ✅ each role lands on its own surface |
| **Required phone on user create** | `CreateUserInput.Phone` now `required`; admin Users form marks it required | ✅ no-phone create→422; with phone→200 and that user can log in |
| **Cashier: Returns** | `/cashier/returns` lists recent sales with Receipt + per-line Return modal; posts to `POST /cashier/sales/:id/partial-return` (cashier-reachable; the `/api` one is admin-only) | ✅ sold 3, returned 2 → stock +2, status `partially_returned` |
| **Cashier: Damage** | `/cashier/damage` write-off form → `POST /cashier/damage` (`stock.Damage`) | ✅ stock 168→166 |
| **Cashier: Credit** | `/cashier/credit` lists customers with outstanding>0; collect modal → `POST /cashier/credit/:id/payment` (`customers.RecordPayment`) | ✅ outstanding 60→50 |
| **Cashier shell nav** | topbar tabs Sell/Returns/Damage/Credit; **Admin link only for admin/manager** | ✅ cashier sees no Admin link; admin does |

**Layering:** all new cashier handlers live in `internal/web/cashier.go` (call
services directly, so they sidestep the admin/manager role gate on the JSON API);
templates in `templates/pages/cashier/more.templ`; routes under the `/cashier`
group (jwt only) in `web.go`. The `saleReturn()` Alpine component gained an
optional `{endpoint, reload}` arg so admin and cashier reuse the same JS. The
`Cashier` layout now takes `(title, userName, role, active)`.

## ✅ Phase 4 — final polish & self-contained binary (all live-tested)

| Area | What | Verified |
|---|---|---|
| **Nested category filter (products)** | indented category dropdown; selecting a parent matches products in it + all descendants (recursive CTE) | ✅ product in child cat shows when filtering by parent |
| **Category mgmt tree view** | categories list/picker rendered depth-indented (`categories.Service.Tree`) | ✅ |
| **Batch viewing UI** | "Batches" button per product on Inventory → modal of live lots (expiry, qty, cost, source) | ✅ shows FEFO lots |
| **List pagination** | products (20/pg) & sales (25/pg) with Prev/Next that preserve filters (fetch n+1 to detect next page) | ✅ page 2 loads |
| **Delete buttons** | suppliers + expenses delete in the UI (APIs already existed) | ✅ |
| **Settings** | relabeled "Shop name (your language)"; added **Logo URL** field | ✅ both render |
| **Receipt** | optional **shop logo**, local-language name, **58mm** toggle (`?size=58`) alongside 80mm | ✅ logo + narrow class |
| **Rename** | Go module `pos` → **`karots-pos`** (go.mod + all imports) | ✅ build clean |
| **Self-contained static binary** | `static/` (CSS/JS + vendored htmx/Alpine/Tailwind/JsBarcode) embedded via `go:embed`; served from embedded FS. `CGO_ENABLED=0` → 14 MB static ELF | ✅ runs from /tmp with **no static/ dir**; all assets 200 |

**Deploy = binary + `.env` + Postgres.** `make build` → `bin/karots-pos`.
Migrations + templates + assets are all embedded. Dockerfile/Makefile/README/
.gitignore updated for the new name and the no-static-dir runtime.

**Offline:** the four JS/CSS libs are vendored under `static/vendor/` (Tailwind is
its in-browser JIT build), so the UI needs no internet. Optional future tweak:
swap Tailwind's JIT for a prebuilt `app.css` to shrink the payload.

## ✅ Phase 3 — batch tracking & advanced inventory (migration 0009, all live-tested)

| Area | Backend/API | Admin UI | Verified live |
|---|---|---|---|
| **FEFO batch/lot tracking** | `stock_batches` + `DepleteFEFO` (weighted COGS) | — | ✅ sale of 7 drained earliest-expiry batch first; COGS = weighted 214.29 |
| **Expiry tracking** | `has_expiry` flag, batch `expiry_date` | — | ✅ GRN sets expiry+flag; FEFO order earliest-first |
| **Expiring report + dashboard badge** | `stock.Expiring(days)` | `/admin/reports/expiring`, clickable dashboard card | ✅ lists 2026-06-10 batch within 30d |
| **Low-stock / reorder report** | `products.List{LowStock}` | `/admin/reports/low-stock` (suggested order qty) | ✅ renders, dashboard card links |
| **Per-line partial sale returns** | `POST /api/sales/:id/partial-return`, `sale_returns(_items)` | per-line qty modal on `/admin/sales` | ✅ 3/7 → partially_returned, 4 more → returned, over-return → 409, restock to return batch, proportional refund/credit |
| **Purchase returns (debit notes)** | `POST /api/purchase-returns` | `/admin/purchase-returns` + entry (Alpine `pret()`) | ✅ stock 180→170, payable 3000→2000, FEFO deplete, movement logged |
| **Purchase detail view** | `purchases.Get` | `/admin/purchases/:id` (header + lines + expiry) | ✅ 200, View link on list |
| **Product conversions (bag→loose)** | `product_conversions`, `conversions.Run` FEFO | `/admin/conversions` (create + Run modals) | ✅ 2 of P2 → 20 of P3, dest batch cost 21.00, run logged |
| **Categories management + nesting** | `/api/categories` (existed) | `/admin/categories` (tree, parent select) | ✅ create/edit/delete, ZZChild→ZZParent nesting |
| **Product filter by category** | `products.List{CategoryID}` | category `<select>` on products page | ✅ `?category_id=1` |
| **Units management + edit** | added `units.Update` + `PUT /api/units/:id` | `/admin/units` (CRUD modals) | ✅ edit sk→sack |
| **Barcode label printing** | — | `/admin/labels` + `/admin/labels/print` (JsBarcode, print CSS) | ✅ N labels w/ barcode SVG + price, auto-render |

**Batch model note:** `stock.quantity` remains the atomic oversell guard + cached
aggregate; `stock_batches` is the FEFO ledger (expiry + cost). Both are kept in
sync inside the same tx by every mutation (GRN, sale, damage, adjust, returns,
conversions). Migration 0009 seeds an `opening` batch per product so
`SUM(batches) == stock.quantity` from day one. Sale COGS is the weighted-average
cost of the batches actually consumed.

**⚠️ Regression fixed during this work:** adding the `has_expiry` column made every
`SELECT p.*` products query 500 (sqlx errors on unmapped columns) until a
matching `HasExpiry` field was added to the `products.Product` struct. Lesson:
when you `ALTER TABLE ... ADD COLUMN` on a table read via `SELECT *`, add the Go
struct field in the same change.

## ✅ Phase 2 — backends + APIs + UI, all live-tested

| Area | Backend/API | Admin UI | Verified live |
|---|---|---|---|
| Suppliers CRUD + pay | `/api/suppliers` | `/admin/suppliers` (+table/form/pay) | ✅ create → toast+reload, payable tracked |
| Purchases / GRN | `POST /api/purchases` | `/admin/purchases`, `/admin/purchases/new` (Alpine `grn()`) | ✅ stock 135→185, payable 3000, status partial |
| Expenses | `/api/expenses` | `/admin/expenses` (date filter + range total) | ✅ create → toast + page refresh |
| Finance / Profit | `GET /api/reports/finance` | `/admin/finance` (date range, P&L cards, top products, payments) | ✅ revenue/COGS/gross/net/receivables/payables |
| Sale return | `POST /api/sales/:id/return` | Return button on `/admin/sales` rows | ✅ completed→returned, restock + credit reversal |
| Damage write-off | `POST /api/stock/damage` | "Record Damage" modal on `/admin/stock` | ✅ stock 185→180, guarded, audited |
| Customer edit | `PUT /api/customers/:id` | Edit modal on `/admin/customers` | ✅ name + limit updated |
| Customer repayment | `POST /api/customers/:id/payment` | Pay modal (shown when outstanding>0) | ✅ outstanding 260→210 |
| Admin users | auth.Service | `/admin/users` (+table/form/deactivate) | ✅ create (admin-only routes) |
| Cost snapshot on sales | migration `0008_sale_item_cost.sql` | — | ✅ COGS accurate going forward |
| **Thermal receipt (80mm/58mm)** | — | `GET /cashier/receipt/:id` (`?print=1` auto-prints) | ✅ renders; Print Bill button in POS; receipt link on Sales rows |
| **Sales filtering** | `sales.ListFilter` (from/to/status) | filter form → `/admin/sales/table` fragment | ✅ all=4 = returned 2 + completed 1 + credit 1 |
| **Inventory movement filter** | `stock.Movements(…, type, …)` | type `<select>` → `/admin/stock/table` | ✅ `?type=damage` returns only damage rows |

## Layout / structure (unchanged rules)
- **Import-cycle rule** still holds: feature packages never import `templates/*`.
  All new UI handlers live in `internal/web/admin_more.go` (+ `cashier.go` for the
  receipt). Templates import feature **model** types only.
- New web wiring: `internal/web/web.go` registers all the routes above and the
  `Server` struct now also holds `suppliers/purchases/expenses/reports` services.
- New templates: `templates/pages/admin/{suppliers,purchases,expenses,finance,users}.templ`,
  `components.templ` (modalShell/modalButtons), plus additions to
  `stock.templ` (DamageForm + move-type filter), `customers.templ`
  (CustomerEditForm/CustomerPaymentForm + row buttons), `sales.templ`
  (SalesRows fragment + filter form + Return button), and
  `templates/pages/cashier/receipt.templ`.
- New JS: `static/js/app.js` gains `grn()` (GRN entry) and `pos().printReceipt()`.
- Receipt print styling: `static/css/app.css` `@media print` + `.receipt` (80mm).

## Notes / gotchas (for the next session)
- **Money** is `shopspring/decimal` everywhere; parse form strings with `money.Parse`.
- HTMX errors surface as toasts via the `HX-Trigger: show-toast` header (hyphenated
  on purpose). Success+close+reload via `response.ToastAnd(...)`. Expenses/finance
  use a plain `HX-Refresh: true` full-page reload (no list fragment).
- **zsh shell traps** (this shell is zsh, not bash):
  - Never name a loop var `path` or `status` — they're tied to `$PATH`/special and
    clobber the environment (then `curl` "command not found"). Use `p`, `hs`, etc.
  - zsh does **not** word-split unquoted variables, so `CMD="curl -s"; $CMD` fails.
    Inline the full command instead.
  - **Do not** pipe the dev server through `head` (`go run … | head -40`): once
    head closes, the server dies on SIGPIPE. Redirect to a file:
    `go run ./cmd/server > /tmp/pos_server.log 2>&1`.
  - `curl` is at `/usr/bin/curl`; DB is `docker exec pos_db psql -U pos_user -d pos_db`.
- Dev users: **Admin / 1234**, **Cashier / 1111**. Change before any real deploy.
- Run locally: `docker compose up -d postgres`, then
  `set -a && . ./.env && set +a && go run ./cmd/server`.

## ✅ DONE — full UI revamp (Phases 1–4 all shipped)

### ✅ Phase 1 (done) — admin nav declutter + global affordances
- **Collapsible grouped admin sidebar.** The flat wall of ~20 links is now
  Dashboard (standalone) + six collapsible groups: **Sell · Inventory ·
  Purchasing · Cash · Reports · Setup**. Open/closed state per group is saved to
  `localStorage` (`adminNavOpen`) so it survives the full page reloads; the group
  that owns the current page is always force-opened. See `adminGroups()` /
  `navGroupBlock` in `templates/layouts/admin.templ` and `adminNav()` in
  `static/js/app.js`.
- **Command palette (⌘K / Ctrl+K).** Global "jump to any page" overlay in *both*
  shells — type to filter, ↑/↓ to move, Enter to open, Esc to close. Also opened
  by the "Search / jump…" button (admin sidebar) / 🔍 button (cashier topbar),
  which dispatch an `open-palette` window event. Built from `CommandPalette` +
  `paletteJSON()` in `templates/layouts/palette.templ` (shared `navItem`/
  `navGroup`/`paletteEntry` types live there) and `cmdPalette()` in `app.js`.
  Destination lists: `adminPalette()` (admin.templ), `cashierPalette(role)`
  (cashier.templ — Admin entry only for admin/manager).
- **Global touch + keyboard CSS** in `static/css/app.css`: strong always-visible
  `:focus-visible` ring everywhere, and `@media (pointer: coarse)` bumps buttons/
  selects/inputs/textarea to a ≥44px tap target (and 16px font to stop mobile
  zoom) — touch devices only, desktop layout untouched.

### ✅ Phase 2 (done) — cashier touch + keyboard polish
- **Cashier keyboard shortcuts.** A `keydown.window="onKey($event)"` handler on
  the POS root (`pos()` in `static/js/app.js`): **F2** focus search · **F3** focus
  scan · **F9** focus discount · **F4** hold sale · **F10** complete sale · **Esc**
  close the open modal (or blur the focused field) · **Enter** on the receipt
  screen starts a New Sale. Yields to the command palette while it's open (palette
  toggles a `palette-open` body class). Inputs carry `x-ref` (`searchInput`,
  `scanInput`, `discountInput`) and the placeholders show their F-key.
- **Touch qty steppers.** Each cart line now has −/＋ buttons around the qty input
  (`incQty`/`decQty`) plus larger remove/✕ hit areas — finger-usable without a
  keyboard.
- **Shortcut legend.** A muted keycap legend (desktop only) under the cart actions
  listing F2/F3/F9/F4/F10/Esc/⌘K; `kbd` styled in `static/css/app.css`.
- **Global Esc-to-close on admin modals.** `ModalHost` in
  `templates/layouts/base.templ` now clears the modal container on
  `keydown.escape.window`, so every HTMX form modal closes with Esc.

### ✅ Phase 3 (done) — admin data-entry touch + keyboard polish
- **GRN & purchase-return entry screens** (`grn()` / `pret()` in `app.js`,
  `purchases.templ` / `purchasereturns.templ`): `keydown.window="onKey($event)"`
  on the root — **F4** add line · **F10** submit (Receive Goods / Return to
  Supplier); the buttons show those hints. Each qty cell now has −/＋ steppers
  (`incLine`/`decLine`), qty column widened to `w-32`. Yields to the palette via
  the `palette-open` body class.
- **Modal autofocus.** A `DOMContentLoaded` listener in `app.js` hooks
  `htmx:afterSwap` on `#modal-container` and focuses the first non-hidden
  input/select/textarea — so every HTMX form modal is ready to type into (and Esc
  closes it, from Phase 2). Enter-to-submit already works via each form's submit
  button.
- The cash-register denomination-counting inputs (open/close/withdraw on the
  cashier page) are covered by the Phase-1 `@media (pointer: coarse)` 44px tap
  targets; no per-field change needed.

### ✅ Phase 4 (done) — nav animation + global jump-anywhere
- **Smooth nav collapse animation.** The admin nav groups now animate open/closed
  via the pure-CSS grid `0fr→1fr` trick (`transition-[grid-template-rows]`, inner
  `overflow-hidden`) — no Alpine plugin added. The group that owns the current
  page is server-rendered already-open (`grid-template-rows:1fr` inline) so there
  is no flash before Alpine hydrates; `groupHasActive()` in `palette.templ` drives
  that. See `navGroupBlock` in `templates/layouts/admin.templ`.
- **"/" opens the palette from anywhere.** `cmdPalette.onSlash()` (in `app.js`,
  wired via `keydown.window` on `CommandPalette`) opens the jump-to-page overlay
  on "/" unless the user is typing in an input/select/textarea/contenteditable —
  so keyboard navigation works on every screen, including plain list pages with
  no other shortcuts. ⌘K / Ctrl+K and the sidebar/topbar buttons still work too.

### ✅ Phase 5 (done) — light / dark theme toggle
- **Toggle** in the admin sidebar footer ("🌙 Dark mode" / "☀️ Light mode") and
  the cashier topbar (icon button). Both use the `themeToggle()` Alpine component
  in `app.js`, which flips a `dark` class on `<html>` and stores the choice in
  `localStorage.theme`.
- **No flash:** an inline script in `Base` (`templates/layouts/base.templ`,
  `@templ.Raw`) applies the saved theme — or the OS `prefers-color-scheme` on
  first visit — before paint.
- **Implementation:** rather than adding `dark:` variants across every template,
  dark mode is a set of `html.dark` overrides in `static/css/app.css` that remap
  the dominant surface/text/border utilities (`bg-white`, `bg-slate-50/100/200`,
  `text-slate-400…800`, `border*`, `hover:bg-slate-*`, inputs, `kbd`). The page
  body rule is specificity-bumped (`html.dark body.bg-slate-100`) to outrank the
  `.bg-slate-100` remap. Print sheets (`.receipt-/.label-/.report-`) are left
  light on purpose so bills/labels still print correctly.

### ✅ Phase 6 (done) — UX polish: built CSS, styled confirms, loading feedback
- **Built stylesheet replaces the Tailwind Play CDN.** `static/css/tailwind.css`
  (~20 KB minified, vs the ~450 KB runtime CDN) is compiled by `make css`
  (`npx -y tailwindcss@3 -c tailwind.config.js -i static/css/tailwind.input.css
  -o static/css/tailwind.css --minify`). `build`/`run`/`dev` now depend on `css`.
  Node/npx is a **build-time** requirement only — the runtime binary is still
  self-contained, and the no-styling-flash from the CDN is gone. The vendored
  `static/vendor/tailwind.js` was deleted; the `error.templ` page no longer pulls
  from `cdn.tailwindcss.com` (worked-offline fix). `tailwind.config.js` scans
  `.templ` + `templates/**/*.go` (for Go-helper class strings) + `static/js`, and
  **safelists** `text-{amber,emerald,indigo,rose,slate}-600` (built dynamically by
  `statCard` in `dashboard.templ` via string concat — Tailwind can't see those).
  ⚠️ If you add a new dynamically-concatenated class, add it to the safelist or it
  won't be generated. `tailwind.css` is committed (like the generated `_templ.go`)
  so a plain `go build` finds the embedded file.
- **Styled confirm dialog replaces native `confirm()`.** A global `htmx:confirm`
  hook (app.js) routes every `hx-confirm` (20 of them) to a themed, dark-aware,
  touch-friendly modal — `ConfirmHost` in `base.templ` + `confirmHost()` in
  app.js (dispatched via an `app-confirm` event; Yes → `issueRequest(true)`).
- **Global loading feedback.** A thin top progress bar (`#app-loading-bar` in
  `base.templ`) driven by `loadingStart/Stop` — hooked to `htmx:beforeRequest/
  afterRequest` *and* wrapped around `apiFetch()` so every request (HTMX or the
  cashier's fetch calls) shows progress. Plus `.htmx-request` dims the triggering
  element, and a `prefers-reduced-motion` block neutralises transitions.

Decision: **section-hub landing pages were intentionally skipped** — the
collapsible grouped sidebar + command palette already solve "find the page"
without an extra layer of hub pages to maintain. Revisit only if asked.

Original goals — all three fully delivered across Phases 1–4:

1. **Touch-screen friendly.** The whole app (cashier terminal *and* admin) should
   be comfortable on a touch monitor/tablet: large tap targets (≥44px), bigger
   number inputs / qty steppers, on-screen friendly controls, no reliance on
   hover, generous spacing, swipe/scroll where it helps. The cashier flow
   especially (cart, payments, denomination counting, hold/resume) should be
   fully usable with fingers only.
2. **Keyboard-only friendly.** Equally usable with no mouse: logical tab order,
   visible focus rings, Enter-to-submit, shortcut keys for the common cashier
   actions (search focus, add/remove line, pay, hold, complete sale), and an
   Esc-to-close on every modal. The two modes (touch + keyboard) must coexist.
3. **Admin nav is overwhelming.** The sidebar currently lists *every* section
   flat (Dashboard, Sales, Customers, Products, Stock, Conversions, Labels,
   Categories, Units, Purchases, Purchase Returns, Suppliers, Cash Register,
   Denominations, Reports×4, Expenses, Users, Audit, Settings) — it reads as a
   wall of links and is visually paralyzing. Rework the information architecture:
   group/collapse into a smaller set of top-level areas (e.g. Sell · Inventory ·
   Purchasing · Cash · Reports · Setup) with collapsible sub-menus or landing
   "section hub" pages, a cleaner visual hierarchy, and maybe a command/search
   palette so power users jump straight to a page.

Scope note: this is a presentation-layer rework (`templates/layouts/*`,
`templates/pages/*`, `static/css/app.css`, `static/js/app.js`) — the
handlers/services/routes should mostly stay as-is. Likely also the moment to swap
the Tailwind Play CDN for a built stylesheet (see below) so the new design ships
lean.

## ⬜ Optional future polish (nothing blocking)
- Purchase **detail view** (`/admin/purchases/:id`) — API `GET /api/purchases/:id`
  exists; no admin drill-down page yet (list only).
- Supplier/expense **delete** buttons in the UI (APIs exist).
- Partial / line-level **returns** (currently whole-sale return only).
- Receipt: optional 58mm toggle (CSS var) and shop logo.
- Pagination on the sales/movements lists (currently capped at 100–200 rows).
- ~~Replace Tailwind Play CDN with a built stylesheet for production.~~ ✅ done (Phase 6).
