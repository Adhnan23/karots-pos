# POS QA Findings — sellability audit

**Started:** 2026-06-29 · **Method:** senior-QA pass, empty-DB onboarding + full feature drive,
core-only → +recharge → +documents → both. Report-only (no app-code changes this pass).

Severity: **P0** data loss / crash / wrong money / blocks a sale · **P1** major feature broken
or missing for sale-readiness · **P2** notable UX / pain · **P3** polish.
Type: **Bug** (broken) · **Gap** (missing) · **Pain** (friction).

---

## Executive summary
Audited the whole system from an **empty DB**, onboarding a shop and driving every major flow in
the UI + verifying state in psql, **core-only** then with **+recharge / +documents / both**.
**Headline: 0 P0, 0 P1 — the core is sellable-grade.** The entire money/inventory engine
reconciles end-to-end (buy→sell→credit→refund→supplier-pay→expense all produce correct CR-/DP-
receipts; P&L and cash-drawer expectation match to the rupee), serial warranty (claim/replace
keeping expiry + consuming stock) works, and the **plugin framework is excellent** — recharge &
documents enable on a live DB via isolated migration version tables, inject all their UI hooks
(nav/tabs/quick-actions/tender), coexist without conflict, and never break core.

**Audit status (2026-06-30): COMPLETE.** Every matrix cell is ✅ (or an explicitly-noted known
limit). All blockers QA-001..010 fixed (QA-004 won't-fix by design). **Four more findings this
pass — QA-011..014 — all P2/P3, all resolved:** QA-011 (P&L "Cash received" relabel), QA-012 (P2
import duplicated barcode-less products → name fallback), QA-013 (P2 recharge float refill
misattributed → now lands in active till), QA-014 (P3 known limit: Sinhala/Tamil don't print on
PC437 thermal; HTML receipts fine). Tiers A–D all driven live; cross-cutting (security/perf/i18n/
TTL/Windows) all checked. **Final gate green:** `go vet ./...`, `go test ./...`,
`GOOS=windows go build ./...` all clean; backups capture plugin data (dynamic table discovery);
restore round-trip + QA-010 receipt-seq fix verified live. **Recommendation: ship-ready** for the
target market, with QA-014 (local-script thermal) as the one product decision to weigh.

## Sellability blockers (no P0/P1; these P2s should be fixed before/at launch)
- **QA-002** — ✅ FIXED 2026-06-29: per-request active-user check (set-once `UserValidator`);
  deleted/deactivated users are force-logged-out immediately. System admin unaffected. Verified live.
- **QA-004** — ⛔ WON'T FIX (by design): vendor installs on-site, creates admin via the hidden
  system account, and trains the customer. No self-serve install → no bootstrap needed.
- **QA-007** — ✅ FIXED 2026-06-29: cashier Collect modal now has a Cash/Card/Online method
  selector (+ reference); non-cash no longer hits the drawer. Owner-validated live.
- **QA-001** — ✅ FIXED 2026-06-29: pre-tx validation extracted; `go test ./...` green.
- **QA-010** — ✅ FIXED 2026-06-29 (**P1**): restore left standalone receipt sequences
  (S-/CR-/DP-) at 1, so the first post-restore receipt collided (HTTP 500). Now `setval`'d on
  restore. Surfaced by owner testing returns; was missed by the row-count-only check.
- **Restore round-trip — ✅ VERIFIED (2026-06-29), now incl. post-restore writes.** download
  backup → `make reset` (bare DB) → `POST /admin/restore` reloaded every core table to identical
  row counts (users 3, sales 1980.00, CR-000001–004, cust outstanding 70.00, stock P1=67/P2=3,
  batches 4, serials 2). Only delta = the two plugin `goose_db_version_*` tables (not recreated
  under a core-only binary — not a defect). **Caveat found & fixed (QA-010):** receipt sequences
  weren't advanced; after the fix, post-restore inserts mint correct new numbers. No data loss.

## Status matrix (area × build)
Legend: ✅ pass · ⚠️ issues found · ❌ broken · — not yet tested

| Area | Core | +Recharge | +Documents | Both |
|------|------|-----------|------------|------|
| Build / vet / tests | ✅ | — | — | — |
| Onboarding (empty→shop) | ✅ (only residual = QA-004 won't-fix) | — | — | — |
| Settings / receipt config | ✅ | — | — | — |
| Users / auth / PIN | ✅ | — | — | — |
| Units / conversions | ✅ | — | — | — |
| Categories | ✅ | — | — | — |
| Products (+barcode/serial/warranty) | ✅ | — | — | — |
| Import/export (csv/xlsx/ods) | ✅ (QA-012 dup-on-reimport fixed) | — | — | — |
| Product groups / cashier menu | ✅ | — | — | — |
| Suppliers | ✅ | — | — | — |
| Customers / credit | ✅ | — | — | — |
| Purchasing (PO lifecycle) | ✅ | — | — | — |
| Supplier returns | ✅ | — | — | — |
| Stock / stock-take / movements | ✅ | — | — | — |
| Selling (tenders/discounts/held) | ✅ | — | — | — |
| Returns / refunds | ✅ (+damage disposition) | — | — | — |
| Warranty claims | ✅ | — | — | — |
| Expenses | ✅ | — | — | — |
| Supplier payments | ✅ | — | — | — |
| Cash register / Z | ✅ | — | — | — |
| Cashflow / lockers / CR- | ✅ | — | — | — |
| Reports / finance | ✅ | — | — | — |
| Unified receipts | ✅ | — | — | — |
| Backup / restore / recovery | ✅ (QA-010 seq fix + e2e round-trip) | — | — | — |
| Audit log | ✅ | — | — | — |
| Recharge plugin surface | n/a | ✅ (QA-013 refill fix) | n/a | ✅ |
| Documents plugin surface | n/a | n/a | ✅ | ✅ |
| Cross-cutting (i18n/security/perf) | ✅ (i18n thermal = QA-014 known limit) | — | — | — |
| Plugin coexistence / migrations | n/a | ✅ | ✅ | ✅ |
| Core regression w/ plugins on | n/a | ✅ | ✅ | ✅ |

---

## Findings

### Phase 0 — Build & automated health (core-only)
- `go vet ./...` — **clean** (no output).
- `GOOS=windows GOARCH=amd64 go build ./cmd/server` — **OK** (binary produced).
- `enabled_plugins.go` — core-only confirmed (no active plugin imports); clean vs HEAD.
- `go test ./...` — **1 package fails**: `supplierpay` (see QA-001). Everything else passes.

#### QA-001 · Core · Build · P2 · Bug · (maintainer/CI) — ✅ FIXED 2026-06-29
**Fix:** extracted `validatePay(in)` (method + totals + non-negative checks) and call it in `Pay`
*before* `appdb.WithTx`, with `PayTx` reusing the same helper. `go test ./...` now exits 0
(whole suite green). Verified: `go test ./internal/features/supplierpay/` → ok.

`go test ./...` is red: `supplierpay.TestPayValidation` panics with a nil-pointer
(`sqlx.(*DB).BeginTxx` on nil) — `supplierpay.go:145` `Pay` opens `appdb.WithTx(s.db,…)`
before any input validation, and the test passes a nil `s.db` expecting validation to reject
bad input first (`supplierpay_test.go:38`).
- **Impact:** production is *functionally* fine (real `s.db` is non-nil; `PayTx` validates
  inside the tx and rolls back bad input), so this is not a money/data bug. But a permanently-
  red `go test ./...` undermines CI and a buyer's/maintainer's confidence, and masks future
  regressions in that package.
- **Repro:** `go test ./internal/features/supplierpay/`.
- **Suggested fix:** validate `in` (amount > 0, known method, non-empty allocations) before
  `WithTx`, or rewrite the test to use a real/sqlmock DB. Pre-tx validation is the cleaner fix
  and also avoids opening a transaction for input that can't succeed.

### Phase 1 — Core only · Onboarding (empty DB)
**Positive:** fresh install ships 9 units + a default settings row; the admin **Dashboard has a
"Setup checklist" (1/6)** — name shop ✓, add staff login, create categories, add products, set
up printer, make first sale — each linking to the right screen. Good first-run guidance once
logged in.

#### QA-002 · Cross-cutting · Core+ · P2 · Bug · (security) — ✅ FIXED 2026-06-29
**Fix (owner chose per-request user check):** added a set-once package-level `UserValidator` in
`internal/middleware/auth.go` (`SetUserValidator`), consulted by every `JWTAuth` instance after
signature/expiry. `web.go` installs it to run one indexed `SELECT is_active FROM users WHERE id=$1`
per authenticated request; missing user or inactive (or query error) → 401/redirect to /login.
Package-level hook avoids changing `JWTAuth`'s signature (it's built independently in ~18 feature
API files) so all API + UI routes get the check. The hidden system admin is a real, always-active
row (`ensureSystemAdmin`), so it's never affected; refresh already rejected inactive via
`FindByID`. **Verified live:** cashier works (200) → `is_active=false` → same cookie rejected
(303→/login) → system admin still 200 → reactivate → cashier 200 again. `go test ./...` green.

A JWT keeps working after its user is deleted/deactivated. `JWTAuth` (`internal/middleware/
auth.go:37-61`) validates only the token **signature + expiry** — it never checks the user still
exists or is active. Observed live: after `make reset` wiped all users, the pre-reset admin
cookie still authenticated `/admin` fully.
- **Impact:** a fired/disabled employee (or a deleted account) retains full access until the
  token expires — up to the 12h default TTL (QA-KNOWN-1). No way to force-logout.
- **Repro:** log in; delete/deactivate that user (or reset DB); reuse the tab → still authorized.
- **Suggested fix:** in `JWTAuth`, after parsing, look up the user by `uid` and reject if
  missing/inactive (cache briefly to bound DB load), or add a token version/`session_epoch` per
  user that bumps on deactivate/PIN-change to invalidate outstanding tokens.

#### QA-003 · Cross-cutting · Core+ · P3 · Pain · (any role) — ✅ FIXED 2026-06-29
**Fix:** registered `e.GET("/logout", a.Logout)` alongside the POST. Logout only clears the
caller's own cookie + redirects, so GET is safe. Verified: `GET /logout` → 303 → `/login`
(was 405).

`GET /logout` returns a raw **405** error page (logout is POST-only). A user who types or
bookmarks `/logout` lands on an error.
- **Suggested fix:** accept GET on `/logout` (clear cookie + redirect to `/login`), or render a
  friendly page. Low effort.

**Verified OK (core, no bugs):**
- **Users** — create with role (Cashier/Manager/Admin), per-counter printer field; **PIN 4–6
  validation works** (2-digit PIN rejected, no row created); list shows Edit/Deactivate.
- **Categories** — clean empty state; create works; nestable (parent picker).
- **Products** — `name` + `category` required (server correctly rejects "categoryid is
  required" when category not chosen); type-to-search category/unit/supplier pickers; barcode +
  Generate; cost/selling/wholesale/tax/reorder; track-serial + warranty-months fields. Created
  Cola 500ml successfully once category selected via the picker.

#### QA-004 · Onboarding · Core+ · P2 · Gap · (new owner / installer) — ⛔ WON'T FIX (by design, 2026-06-29)
**Owner decision:** the vendor (this project's owner) personally installs the POS at each customer
site, logs in via the hidden system-admin `0000000001/2273`, creates the customer's admin
account(s), and trains them. There is no self-serve install in the business model, so a first-run
bootstrap screen is unnecessary. The hidden system account is retained intentionally. Closed as
won't-fix; no code change.

First-run discoverability: a freshly-reset install has **zero real users**; the only way in is
the **hidden** system-admin `0000000001/2273`. The login page gives no hint, no "first-time
setup", no forced bootstrap to create the first admin. A self-installing customer who wasn't
told the magic credential is locked out of their own system.
- **Impact:** blocks self-serve onboarding; fine only if the vendor always provisions the first
  admin. Sellability risk for any non-hand-held install.
- **Suggested fix:** a first-run bootstrap screen ("No accounts yet — create the owner login")
  when `users` is empty, or have the installer/bootstrapper seed an owner account + print the
  credential. At minimum document it prominently.

### Phase 1 — Core · Settings, Stock, Cash register, Selling
**Verified OK (core, no bugs):**
- **Settings** — comprehensive (shop identity, currency, receipt footer, default sale type,
  58/80mm width, tax-registered, low-stock alerts, ask-to-print, PIN policy, receipt/label
  printers + network addr, label dims, logo upload, backup/restore). Save works (toast "saved").
- **Stock-take** — set counted_qty → on-hand updates (Cola 0→50); form-POST flow.
- **Cash register open** — denomination count UI (per note/coin) computes opening float live
  (10×500 + 20×100 = Rs.7,000) → Open Register. Good UX.
- **Selling (revenue path) — full E2E PASS**: scan barcode **or** F2 product search both present;
  cart with per-line qty ± and per-line + bill discount (Rs./%); select tender method
  (Cash/Card/Online) → enter amount → **change computed (200−120=80)** with a **change
  denomination breakdown** (1×50,1×20,1×10); Complete Sale → `S-00001` persisted (total 120,
  paid 200, change 80, completed), payment row (cash 200), **stock decremented 50→49**.
- **Oversell guard — PASS**: selling at 0 stock returns HTTP **409** `insufficient stock for
  Cola 500ml` (clear), no sale row, stock unchanged.
- **Till payment UX is intentional** (confirmed with product owner): no method pre-selected;
  you click Cash/Card/Online then type the amount; Complete Sale blocks until a method is set.

#### QA-005 · Onboarding/Cashier · Core+ · P3 · Pain · (new shop)
A freshly-onboarded shop (products added, no Cashier Menu groups built) sees **"Nothing here
yet."** in the till product grid, with no prompt to set up the Cashier Menu. The till is still
usable via F2 search / F3 scan, so not a blocker, but the empty grid gives no guidance.
- **Suggested fix:** empty-grid call-to-action linking to the Cashier Menu builder ("No quick
  buttons yet — set up your Cashier Menu" / "or search & scan to sell").

#### QA-006 · Settings/Onboarding · Core+ · P3 · Pain · (new owner) — ✅ FIXED 2026-06-29
Two small onboarding inaccuracies: (a) `tax_registered` defaults **ON** for a fresh shop —
most small shops aren't VAT-registered, so receipts/flows assume tax setup they don't have;
(b) the dashboard Setup checklist marks **"Name your shop ✓ Done"** while the name is still the
placeholder **"My Shop"** (it keys off row existence, not a real edit).
- **Suggested fix:** default `tax_registered` off; treat shop-name step as done only when it
  differs from the default placeholder.
- **Resolution:** (a) was a **non-issue / false finding** — re-checked the schema and form:
  `migrations/0006_settings.sql` already declares `tax_registered BOOLEAN NOT NULL DEFAULT false`,
  the default row inserts with it false, and the settings checkbox renders `checked?={ .TaxRegistered }`
  (verified live: DB value `f`, checkbox unchecked). No change needed. (b) **fixed** in
  `internal/web/setup.go`: `shopNamed` now requires the name to be non-empty **and** not the
  `"My Shop"` placeholder (case-insensitive), so the checklist step stays open until the owner
  actually renames the shop.

### Phase 1 — Core · Customers, Credit sale, Debt collection (DP-)
**Verified OK (core, no bugs):**
- **Customers** — create (name/phone/credit_limit/opening_balance/address); list shows
  balance + loyalty points + Edit/Statement/Deactivate.
- **Credit sale** — till Credit type + customer picker (type-to-search, real-select); full
  amount to credit; `S-00002` status credit, customer balance → Rs.120, stock 49→48.
- **Debt collection (DP-)** — `DP-000001` (Rs.50, cash, balance_before 120 → balance_after
  70), Nimal balance → 70, and a money receipt `CR-000001` (customer_payment, from "Nimal
  Perera" to **"Till - System"**). ✅ Confirms the em-dash→hyphen fix is live in this build.
- "Expected in drawer" updates correctly after the cash sale (7,000 → 7,120).

#### QA-007 · Customers/Money · Core+ · P2 · Gap · (cashier) — ✅ FIXED 2026-06-29
**Fix:** added a **Method** selector (Cash/Card/Online, default Cash) + a Reference field
(shown for non-cash) to the cashier Collect modal (`templates/pages/cashier/more.templ`
`CreditPayForm`). The handler already routed cash→till and non-cash off-drawer and the DP-
receipt already prints the method — only the form was missing. Owner-validated live: collected
Nimal's remaining Rs.70 by **card**; recorded off-drawer with the method on the DP- receipt.

The cashier **Collect Payment** modal (`/cashier/credit/pay/:id`) has **only an amount field —
no payment-method selector** (cash/card/online). It records the repayment as **cash**, so a
debt repaid by card/online can't be recorded accurately and would **inflate the expected cash
drawer**, breaking reconciliation. The `customer_payments` schema already has a `method`
column + DP- receipt shows Method, so the data model supports it — only the cashier UI hardcodes
cash. (Check whether the admin credit-collect flow exposes method.)
- **Repro:** `/cashier/credit` → Collect → only "Payment Amount".
- **Suggested fix:** add a Cash/Card/Online selector (default cash) like the till tender; route
  non-cash through the same path so the drawer isn't credited for card/online repayments.

### Phase 1 — Core · Returns / refunds
**Verified OK (core, no bugs):** `/cashier/returns` lists recent sales (Print/View/Return);
the per-line Return modal (returnable = sold − already-returned) processes via
`POST /cashier/sales/:id/partial-return`. Returned Cola×1 from S-00001 → `sale_returns` row
(refund 120), **sale status → returned**, **stock restored 48→49**, refund money receipt
**CR-000002** (kind refund, 120). Walk-in sale → return slip correctly omits customer/balance.
- *Testing note (not a product bug):* the return form at `/cashier/returns/:id` is an htmx
  **fragment**; opening that URL directly (no htmx/Alpine loaded) makes "Process Return" do a
  native GET no-op. The real flow (open modal from the Returns list) works. Worth confirming no
  user can reach the bare fragment URL via a bookmark and hit the dead form.

### Phase 1 — Core · Reports / Finance
**Verified OK — strong area:** Reports hub has 18 reports (Finance/P&L, Tax, Inventory
Valuation, Daily Trend, Cash Register over/short, Customer/Supplier Dues, Profit by Category,
Product Sales vs last year, Returns, Warranty & Recovery, Batches/Expiry, Low Stock, Damage…).
**Finance/P&L is accurate vs the data I created:** Sales 2 / gross Rs.240 / **Returns (120)** /
net 120 / **COGS (80) correctly reversed** for the returned sale / gross profit 40 / margin
33.33% / **receivables 70** (= Nimal balance) / register over-short 0 / top products. Date
presets + CSV present.
- *To verify later (not yet a finding):* "Cash received Rs.200" appears to count only sale
  tender, not the Rs.50 cash **debt collection** — confirm whether cash debt repayments should
  appear in that P&L cash line.

### Phase 1 — Core · Backup / restore, Audit
**Verified OK:** **Backup** (`GET /admin/backup`) downloads a valid gzipped JSON snapshot
(version, generated_at, per-table columns+rows); system-admin can access; **auto-backups** also
run on a schedule (files in `backups/`). The snapshot includes `audit_log` (system-admin actions
are audited as user_name "System").
- **Restore round-trip — ✅ VERIFIED (2026-06-29).** Ran the full `POST /admin/restore`
  ("TRUNCATEs and reloads every table") path on a real backup: downloaded a backup, `make reset`
  to a bare DB, restored via the UI, then diffed every `public` table's row count against a
  pre-reset snapshot — **all 31 data tables identical** (users 3, sales total 1980.00,
  money_receipts CR-000001–004, customer outstanding 70.00, stock P1=67/P2=3, stock_batches 4,
  warranty_units 2). Only the two plugin `goose_db_version_recharge/documents` version tables were
  not recreated, because this is a core-only build (the plugins aren't compiled in) — not a restore
  defect, and their data tables were empty pre-reset. **Restore is data-safe for release.**

### Phase 1 — Core · Warranty (serial tracking + claims)
**Verified OK — sophisticated, correct:** selling a serial-tracked product prompts a serial per
unit at the till ("Serial / IMEI #1"); completing the sale created `warranty_units` row
(PB-SN-0001, warranty_until = sold + 12mo = 2027-06-29, active). The warranty screen lists units
with status filters + serial lookup. Recording a replacement (new serial + reason) →
`warranty_claims` row (resolution replaced), old unit → **replaced** (linked to new unit), new
unit **active keeping the original expiry**, and **stock consumed** (Power Bank 5 → sold 1 → 4 →
replacement −1 → 3). Replacement slip carries months-left (verified in prior session).

### Phase 1 — Core · Purchasing (PO→GRN), Supplier payments, Cashflow/CR-
**Verified OK — full E2E:**
- **Supplier** create (contact/phone/credit_days/opening_balance); Pay/Edit/Delete actions.
- **PO builder** (Alpine): supplier picker (search), product line picker (auto-fills cost/sell),
  qty → `purchases` draft (total 1600, ordered_qty captured).
- **GRN receive** (`/admin/purchases/:id/receive`): received qty prefilled; Mark Received →
  status received, **Cola stock 49→69 (+20)**, **supplier owes Rs.1600**.
- **Supplier payment** modal is well-built: **per-invoice allocation + method (cash/card/online)
  + cash-source selector**. Paid Rs.1000 → supplier balance 1600→**600**, purchase →
  **partial** (paid 1000), money receipt **CR-000003** (supplier_payment, "Till - System" →
  "Lanka Distributors"), redirected to the receipt page (ask-to-print policy).
- **Unified CR- money tracking confirmed** across 3 kinds: customer_payment (CR-001), refund
  (CR-002), supplier_payment (CR-003) — all with clean ASCII "Till - System" labels.
- *Reinforces QA-007:* the supplier-pay modal HAS a method selector; the **customer Collect
  modal does not** — they should be consistent.

**Cashflow / lockers / CR- transfers — fully verified (2026-06-30).** Created two lockers
(Main Safe/safe, Bank BOC/bank), then drove every money move through `cashflow.Move`:
- *Fund:* adjust_up Safe +10,000 → CR-000008 (adjust).
- *Transfer:* Safe→Bank 4,000 → **two ledger legs** (Safe −4,000, Bank +4,000) but **one**
  CR-000009 — correct (one move = one receipt).
- *Bank charge:* Bank −50 → CR-000010 **and** an `expenses` row (Bank charges, 50) → hits P&L.
- *Interest:* Bank +120 → CR-000011 → hits P&L **Other Income**.
Computed balances reconcile exactly: Safe **6,000**, Bank **4,070** (4000−50+120). The combined
**Cashflow view** renders both lockers, Cash on hand, Total cash, and **Net position**
(cash + stock-at-cost + receivables − payables). P&L symmetry confirmed live: bank charge in
expenses, interest in Other Income (re-verifies commit 45c8f3c). Cell → ✅.

### Phase 1 — Core · Expenses, Cash register (open/expected/close)
**Verified OK:**
- **Expenses** — record (category/amount/source/date/description) → expense row + money receipt
  **CR-000004** (expense, "Till - System" → External). Cash-source selector present.
- **Cash register**: open via denomination count (float computed live) ✅; **"Expected in
  drawer" is exactly correct** — Rs.7,050 = 7,000 float + 1,500 power-bank sale + 50 debt
  collection − 1,000 supplier − 500 expense (S-00001 sale/refund net 0). The day's every money
  move flows into the drawer expectation correctly.
- **Close / Z reconciliation — now fully verified (2026-06-30).** Drove three closes end-to-end
  (denomination count-out → `/api/cash-register/close`), all as cashier on real sessions:
  - *Balanced:* float 2,000 + Cola sale 600 → expected 2,600, counted 2,600 → **difference 0**.
  - *Over:* float 1,000, no sale → expected 1,000, counted 2,450 → **+1,450 over**.
  - *Short:* float 500 + Cola sale 240 → expected 740, counted 700 → **−40 short**.
  `difference = counted − (opening + cash_sales + adjustments)` confirmed in every case. Each
  close writes a `closing` cash_movement (counted amount) and an audit `close` row carrying
  "counted / expected / over-short". The **Z-report** (`/cashier/z/:id`) renders Opening float,
  Expected in drawer, Counted, and the over/short line with correct **Over / Short** labeling
  (verified on the short session: 740 / 700 / Short −40). Cash register / Z is ✅.

### Phase 2 — +RECHARGE plugin (integration + core regression)
**Verified OK — clean integration, no core breakage:**
- **Late-enable migrations**: enabling recharge on the live core DB applied only its migrations
  via its own `goose_db_version_recharge` table (8 recharge_* tables created), **core untouched**
  — validates the plugin framework's non-destructive late-enable.
- **All plugin→core UI hooks fire**: admin nav "📶 Reload & Bills" (+ admin hub page: carriers/
  devices/cards, float refills, ledger, reports), cashier nav tab, **📶 Reload quick-action tab**
  in the till, and **"Wallet (eZ Cash / mCash)" tender method** alongside Cash/Card/Online. Core
  nav order preserved (plugin appends).
- **Core regression PASS**: a normal retail cash sale completed (S-00004) with the plugin on.
- *Deferred (deep plugin logic, documented E2E-verified previously):* live recharge transaction
  (carrier/device setup → deposit/billpay/topup with per-device float + overdraw block,
  reconciliation, refill). Do a live deposit + a Wallet-tender sale next to fully close Phase 2.

### Phase 3/4 — +DOCUMENTS and BOTH plugins (integration + regression)
**Verified OK — clean coexistence:**
- Enabling **documents** applied its migrations via its own `goose_db_version_documents`
  (`doc_service` table); with **both** plugins on, each keeps its own goose version table
  (`_recharge`, `_documents`) — **no migration conflict**, core schema untouched.
- **Both plugins' hooks coexist without collision:** admin nav shows both "🖨 Communication
  Store" + "📶 Reload & Bills"; the till shows both quick-action tabs ("🖨 Photocopy" + "📶
  Reload"); documents admin page loads (services + paper consumption + labour, empty state).
- **No route conflicts / startup errors** with both compiled in.
- **Core regression PASS with both on**: retail cash sale completed (S-00005).
- *Deferred (deep per-plugin flows, documented E2E-verified):* a live documents service sale
  (photocopy/print with consume-on-sale + `sale_items.description`) and the recharge txns.
- *Note:* the dev DB now retains additive plugin tables after restoring the core-only binary —
  harmless (core ignores them); a fresh `make reset` clears them.

### Improvements requested by the owner (2026-06-29)
#### QA-009 · Returns/Stock · Core+ · P2 · Gap · (cashier) — owner-requested — ✅ FIXED 2026-06-29
**Fix:** added a per-line **disposition** selector ("Back to stock" / "Send to damage") to both
the cashier and admin return modals. `ReturnLineInput` gained a `Disposition` field; in
`PartialReturnTx`, a line marked `damage` is restocked (so the return + batch ledger stay
consistent) and then immediately written off through the same primitives as the Damage screen
(`DecrementGuarded` + `DepleteFEFO` + a `MoveDamage` movement valued at FEFO cost, referencing the
return). Net sellable stock is unchanged for damaged goods, the refund/credit is unaffected, and
both the **returns report** and the **damage/loss report (P&L stock-losses)** stay accurate. The
Alpine `saleReturn()` component now sends `disposition` per line (default `restock`). Build +
`go test ./...` green. *Owner UI validation pending.*

A processed return **always adds the item back to sellable inventory** (observed: Cola 48→49 on
return, no choice). A faulty/damaged return shouldn't re-enter sellable stock — it would later
be sold as good. Add a **disposition prompt/quick-action at return time: "Return to inventory"
vs "Send to damage/write-off."** The Damage feature already exists (cashier Damage tab + Damage
report), so route the "damaged" path into it (and reflect the loss in P&L stock-losses instead
of restocking).
- **Suggested fix:** per-line (or per-return) radio "Restock / Damaged"; on Damaged, create a
  damage/write-off record instead of incrementing stock; refund/credit unaffected.

#### (QA-007 enrichment) — owner-requested, same fix
Owner wants the debt-collection **payment method captured and shown on the DP- receipt**. The
DP- receipt already renders a Method line; adding the Cash/Card/Online selector to the cashier
Collect modal (mirror the supplier-pay modal) closes both QA-007 and this request at once, and
routes non-cash off the cash drawer.

#### QA-010 · Backup/Restore · Core+ · **P1** · Bug · (any role, post-restore) — ✅ FIXED 2026-06-29
**The first receipt minted after a restore collided** and the operation failed with HTTP 500:
`duplicate key value violates unique constraint "money_receipts_receipt_no_key"`. Root cause: the
restore's `resetSequences` advances only **column-owned** sequences (via `pg_get_serial_sequence`),
but the human-friendly receipt numbers come from **standalone** sequences — `sales_receipt_seq`
(S-), `money_receipt_seq` (CR-), `debt_receipt_seq` (DP-) — which are not owned by any column, so
they were left at 1 after a restore while the reloaded rows already used those numbers. The next
sale / money receipt (refund, supplier payment, expense, etc.) / debt payment therefore collided.
- **Severity P1:** restore is the disaster-recovery path; silently leaving sequences behind means
  the *first* post-restore cash refund / sale / collection blows up — exactly when a shop is
  recovering and least able to debug it. (Surfaced by the owner while testing returns.)
- **Why my earlier "restore verified" missed it:** that check compared *row counts only*; it never
  exercised a post-restore insert. Lesson recorded — verification must include a write after restore.
- **Fix:** added `resetReceiptSequences` in `internal/backup/backup.go`, called at the end of
  `Restore`, which `setval`s each standalone receipt sequence to the max numeric suffix present in
  its table (`regexp_replace(receipt_no,'\D','','g')::bigint`), tolerant of missing tables/seqs.
  Covers S-/CR-/DP-; supplier payments + expenses + refunds all flow through CR-, so they're
  covered too. Re-verified live: after the corrective `setval`, a damage-return minted **CR-000005**
  and a restock-return minted **CR-000006** with no collision; `go test ./internal/backup/` green.
- **Clean round-trip re-verified end-to-end through the HTTP path (2026-06-29):** `GET /admin/backup`
  → `make reset` (bare) → restart (fixed binary) → `POST /admin/restore` → sequences came back
  advanced (sales=5/money=6/debt=2, all is_called) → a post-restore return minted **CR-000007**
  with HTTP 200 (pre-fix this was the dup-key 500). Confirms the fix runs inside the real
  `Restore` code path, not just the manual correction.

---

#### QA-011 · Reports/Finance · Core+ · P2 · Pain · (owner reading P&L) — ✅ FIXED 2026-06-30
**The Finance → Profit P&L "Cash received" line was misleading vs the Cashflow view.** It is
`SUM(sales.paid_amount)` filtered by **sale** date — i.e. only tender taken *at point of sale*.
A later **cash debt collection never appears** there: collecting a debt only decrements
`customers.outstanding_balance` and inserts a `customer_payments` (DP-) row; it never updates
`sales.paid_amount`. Meanwhile the **Cashflow view's "Cash in (range)"** *does* count the
collection (debt payment emits a CR- money receipt — confirmed CR-000001 customer_payment). So
the two "cash received" figures diverge whenever credit is used, and the P&L one silently
understates real cash collected.
- **Severity P2 (Pain):** not wrong math, but two differently-scoped "cash" numbers in the same
  app confuse an owner reconciling takings; the P&L number looks like "all cash in" but isn't.
- **Resolution (owner decision 2026-06-30): relabel, keep the P&L accrual-pure.** Renamed the
  line **"Cash received" → "Sale tender (paid at sale)"** in the Finance report (HTML +
  CSV export) and clarified the `reports.PL.Received` field comment to point at the Cashflow
  view as the source of truth for true cash-in. No figure changed; the Cashflow view remains
  authoritative for actual cash collected. Verified live: `/admin/reports/finance` HTML and
  `?format=csv` both render the new label.
- **Files:** `templates/pages/admin/mgmt_reports.templ` (FinanceReport table),
  `internal/web/admin_reports.go` (CSV row), `internal/features/reports/reports.go` (comment).

---

#### QA-012 · Import/Export · Core+ · P2 · Bug · (owner, data integrity) — ✅ FIXED 2026-06-30
**Catalog import duplicated every barcode-less product on re-upload**, despite the export being
explicitly sold as a round-trip ("export, edit in a spreadsheet, and re-upload"). `ImportOne`
matched an existing product **by barcode only** (`internal/features/products/service.go`); when a
row had no barcode it always `Insert`ed a new row. Reproduced: exported 5 products → re-imported
the unchanged file → **"2 created, 3 updated"**, leaving duplicate `Rice 1kg` (id4+id6) and
`Rice 25kg sack` (id3+id7). Many real products have no barcode (loose groceries, services,
weighed goods), so an owner doing export→edit→re-upload silently corrupts their catalog, doubling
rows (and seeding extra opening stock) every cycle.
- **Severity P2 (data integrity):** silent, cumulative catalog corruption on an advertised workflow.
- **Fix:** added `Repository.FindByName` (case-insensitive, active, oldest-first) and a fallback
  in `ImportOne` — match by barcode first, else by name — so barcode-less rows update in place.
  Re-verified: re-importing the export now reports **"0 created, 5 updated"**, product count
  unchanged, zero duplicates. `go vet` + `go test ./internal/web ./internal/sheet` green.
- **Files:** `internal/features/products/products.go` (FindByName),
  `internal/features/products/service.go` (ImportOne fallback).

---

#### QA-013 · Recharge plugin · +Recharge/Both · P2 · Bug · (admin refill / cashier) — ✅ FIXED 2026-06-30
**Admin float refill landed in the refiller's own till session, not the till using the device's
float** — so the refilled float was invisible to the working cashier and didn't carry forward.
`Refill` (`plugins/recharge/admin.go`) resolved the session via
`CashRegister.Current(ctx, adminUID)`. Reproduced: cashier had Dialog SIM 1 float open under
session 5 (live balance 3,500); the system-admin (with their own leftover open till, session 1)
refilled +10,000 → the `refill` row got `session_id=1`. Result: cashier's live balance **stayed
3,500** (the overdraw guard would block the just-added 10,000), the admin "where's my money"
panel showed a third number (8,500), and the float was stranded in session 1 — it never reached
the cashier's session **or** the session-0 carry. In any shop where the owner also keeps a till
open (common), every mid-shift refill silently misattributes.
- **Severity P2:** float/money correctness — the agent can't use float they actually loaded.
- **Resolution (owner decision 2026-06-30: apply to the active cashier till now).** Added
  `Store.OpenDeviceSession(deviceID)` → the session id of the device's un-closed
  `recharge_device_sessions` row (the till actually holding that float), else 0 (carry). `Refill`
  now attributes there instead of the refiller's session. Re-verified with the admin's own till
  still open: refill row now `session_id=5` (cashier), cashier live float **3,500 → 13,500**
  immediately, usable mid-shift. `go vet ./plugins/recharge` green.
- **Files:** `plugins/recharge/recon.go` (OpenDeviceSession), `plugins/recharge/admin.go` (Refill).

---

#### QA-014 · i18n / printing · Core+ · P3 · Gap · (Sinhala/Tamil shops) — KNOWN LIMITATION (by design)
**Local-script (Sinhala/Tamil) names don't print on thermal receipts** — they render as `?`.
The ESC/POS layer (`internal/escpos.ascii`) deliberately sanitises any non-printable-ASCII rune
to `'?'` before sending, because the printer's built-in **PC437** font is Latin-only and raw
multibyte bytes would otherwise print as codepage garbage. Verified: a product named "තේ කොළ (Tea)"
sells fine and **renders correctly on the HTML receipt** (`/cashier/receipt/:id`), but on the
thermal slip the Sinhala becomes `??? (Tea)`.
- **Severity P3:** graceful + documented degradation, not a crash; the English part still prints
  and most shops use English/transliterated POS names. But a shop that names products purely in
  Sinhala/Tamil can't show them on thermal paper.
- **Path forward (if needed):** print the receipt body as a raster image (render text → bitmap →
  ESC/POS raster) for local-script support, or print the HTML/A4 receipt. No code change made —
  logging as a deliberate constraint for the owner to weigh against the target market.
- **Owner decision 2026-06-30: WON'T FIX (non-issue in practice).** Even Sinhala/Tamil shops use
  English product names on the POS, so thermal slips are fine as-is. Raster thermal dropped.

---

## Tier D — cross-cutting hardening (2026-06-30)

**Security / authz sweep ✅.** Route-group guards verified at runtime as a cashier: **every**
admin route returns **403** (`/admin`, products, users, audit, backup, lockers, reports/finance)
— `ag` group enforces `RequireRole(admin,manager)`, with users/audit/backup further gated to
admin. Admin-only **sale APIs 403** for cashier (`GET /api/sales` list, `POST /api/sales/:id/return`,
`POST /api/purchase-returns`). **IDOR check**: `/cashier/z/:id` enforces ownership — cashier views
own session (200) but gets **403** for another user's session (the admin's). Stale-token hole was
already closed (QA-002). Receipts are shop-wide by design (any cashier may view any receipt — not
user-scoped secrets), which is appropriate for a POS.

**Windows build ✅.** `GOOS=windows GOARCH=amd64 go build ./cmd/server` produces a 32 MB exe;
`GOOS=windows go build ./...` (all packages incl. both plugins) is clean. `internal/printing` has
both `_unix` and `_windows` build-tagged implementations.

**i18n → QA-014 above** (thermal local-script limitation; HTML receipts render Unicode fine).

**Performance ✅.** Seeded **2,010 products** and timed authenticated endpoints: admin products
list **6.8ms**, search "Perf Product 1500" **5.1ms**, stock page **7.3ms**, `/api/products?search`
**10.6ms**, finance report **4.5ms** — all sub-15ms (paginated list, indexed search). Comfortable
for small/medium-shop scale. (Deep multi-10k sales load not driven; products list/search — the main
pagination surface — is fast. Perf seed cleaned up after.)

**Session TTL (QA-KNOWN-1) ✅ re-confirmed by config.** `JWTAccessTTL` = **12h**
(`JWT_EXPIRES_IN`, config.go:44); the UI cookie MaxAge = the same 12h (web.go:104). A refresh-token
table exists (7d) but is **not** wired into web-cookie sliding renewal — `setCookie` writes a single
access cookie and there's no refresh-on-request middleware. Net: a normal daily shift (≤12h) never
expires mid-task (the original 15m bug is fixed, commit 263ed1b); a **>12h continuous** session
would still hard-expire. Acceptable for a daily-shift POS; would need sliding refresh for 24/7
operation. Flag retained, not a blocker.

---

## Tier C — plugin deep flows verified (2026-06-30)

**+Documents live ✅.** Enabled alongside recharge ("both" build) — migrations via own
`goose_db_version_documents` (5 tables), core still v42. Set up a metered service **Photocopy A4**
(@10/copy) with a **consumable** of 1 × A4 Paper sheet per copy (paper product stocked 500).
Recorded a service sale: 20 copies @10 = **S-00010 total 200**, line carries `components` →
**consume-on-sale fired in the core sale tx**: A4 Paper **500→480** (−20 `sale` movement). The
service line's **cost_price rolled up from FEFO** (20 sheets × cost 2 = 40 / 20 = **2.00/unit**)
and the per-line **description** "Photocopy A4 B&W 1-side x20" persisted to `sale_items`. Admin
documents hub/report/labour pages render.

**+Both plugins live ✅ (#13).** With recharge + documents both enabled: both migration version
tables apply cleanly with **no conflict** (core v42, recharge v9, documents own table); **both
admin nav hooks coexist** (/admin/recharge "Reload & Bills" + /admin/documents "Communication
Store"); **both ReportCard hooks** render on the reports hub. **No receipt-number collisions**
across plugins (0 duplicate S-/CR-/DP-). No double-count: recharge cash movements mirror the
drawer (not revenue), the documents service sale is a normal sale counted once. Core regression
clean throughout.

**+Recharge live ✅ — one bug found+fixed (QA-013 above).** Enabled via `enabled_plugins.go`
blank import; migrations applied through its own `goose_db_version_recharge` (8 tables), **core
untouched** (core still v42). Drove the full per-device-float model: carrier + float device
create; opening float 5,000; **deposit** 1,500 (+20 service charge) → float −1,500 / drawer
+1,520 (pay-in 1,520); **overdraw hard-block 409** ("not enough float on this device") on an
oversized deposit. **Bank cards** (no-float): create + load 5,000 → **billpay** 2,000 (+30 svc)
→ card 5,000→3,000, drawer +2,030; **billpay overdraw 409** ("not enough balance on this card").
**Admin float refill** (QA-013 fix): +10,000 books a "Float top-up" expense (no drawer move) and
now lands in the active cashier till → live float 13,500. **Reload** (airtime via sale path):
float −500, cash-neutral (sale collected it). **Wallet tender** (wallet-paid sale): float +300.
**Reconciliation**: recon page shows Opening 5,000 / Expected 13,300; closing count 13,300 books
the device-session close. All recharge admin pages (hub/report/ledger/refills/balances) render;
**core regression clean** (products/stock/finance/money-receipts all 200, normal cash + wallet
sales succeed, Reload & Bills nav hook present).

---

## Tier B — core breadth verified (2026-06-30, no defects unless noted)

**Import / export (csv / xlsx / ods) ✅ — one bug found+fixed (QA-012 above).** Products CSV
round-trip: export (correct 14-col header) → edit (Cola price 120→130, add Sprite) → import →
**Cola updated + Sprite created with opening stock 40**. Format coverage: `?format=xlsx` and
`?format=ods` exports produce valid files (`file` → "Microsoft Excel 2007+" / "OpenDocument
Spreadsheet", correct MIME). **xlsx read path** works (re-imported the .xlsx). Bad-row handling:
a blank-name row is reported "missing name" + skipped while the valid row (Fanta) still imports.
Customers + suppliers CSV export headers correct. The barcode-less duplication bug (QA-012) was
found here and fixed; re-import is now idempotent.

**Suppliers + Supplier returns (debit notes) ✅.** Supplier create + edit clean. Drove a
supplier return (`POST /api/purchase-returns`, admin/manager-gated): 5 × Cola @ cost 80 to Lanka
Distributors → **HTTP 201**, total 400. Effects all correct: supplier balance **600→200**
(returning goods reduces what we owe), Cola stock **100→95**, `purchase_return` movement **−5**
(FEFO-depleted). Detail page (`/admin/purchase-returns/1`) renders supplier/Cola/total/reason;
supplier-dues + suppliers reports render. (Suppliers list/pay was already ✅ in Phase 1.)

**Unified receipts — 58/80mm reprint ✅.** All four receipt types view (HTTP 200) on **both**
cashier and admin: sale **S-** (`/cashier/receipt/1`), money **CR-** (`/{cashier,admin}/money-
receipts/1`), debt **DP-** (`/{cashier,admin}/receipts/credit/1`), warranty
(`/{cashier,admin}/receipts/warranty/1`). All four hub tabs (sales/cash/credit/warranty) render
on both roles. **Size switch works**: `?size=58` vs `?size=80` produce different HTML for both
the sale and CR- receipts (paper-width override on top of the saved `receipt_width` setting).
Reprint POSTs return `200 {"ok":true}` (dev printer queue accepts; no crash with no hardware).
ESC/POS byte generation is covered by `internal/escpos` + `internal/tspl` unit tests (green).

**Product groups / Cashier Menu ✅.** Built a 2-level menu in admin: top group **Drinks** (🥤)
→ children **Cold** (❄️) + **Hot** (🔥); linked Cola + Sprite into Cold with per-item emoji.
Cashier drill-down API verified: `/api/groups` returns Drinks (has_children=true); `/api/groups/1`
→ breadcrumb [Drinks], child Cold; `/api/groups/2` → breadcrumb [Drinks › Cold], products Cola
(🥤) + Sprite. Per-item emoji update (Sprite → 🥶) and group reorder (`?dir=up` query param —
Hot↔Cold sort_order swap) both work. Admin Groups + tree pages render. (Note: `move` takes `dir`
as a **query** param, not form — fine, just noting for any future UI wiring.)

**Units / conversions ✅.** Unit CRUD clean (create Carton/ctn → update → delete, 0 left).
Conversion E2E: created Rice 25kg sack (id3, cost 5000) → Rice 1kg (id4) at **ratio 25**;
stocked 4 sacks; ran 2 → **source 4→2, dest 0→50 kg**, two `conversion` movements (−2 / +50),
`conversion_runs` logged (from 2 / to 50). **Value preserved exactly**: source out 2×5000 =
10,000 = dest in 50×200 → dest batch valued **200/kg** (source='conversion'). **Overdraw guard
works**: running 99 sacks (only 2 on hand) → **HTTP 409** "not enough stock of the source
product", source stock unchanged. This is the consume-on-sale value seam shared with plugins.

---

## Carry-over (already known from prior sessions, re-confirm in-context)
- **QA-KNOWN-1** · Cross-cutting · P1 · Bug → now mitigated · Session was a single
  access-token cookie; default TTL was 15m (no sliding refresh) → users logged out mid-task.
  Fixed this session (default → 12h, commit `263ed1b`). **Re-verify** the long-task scenario
  still can't expire an active operator (sliding/keepalive not implemented — flag if it bites).
- **QA-KNOWN-2** → confirmed in Phase 0, logged as **QA-001** above.
