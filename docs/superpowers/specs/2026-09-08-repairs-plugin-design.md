# Repairs plugin — design

Date: 2026-09-08
Status: approved design, pending spec review

## Purpose

A repair/service shop (mobile phones, electronics, etc.) takes a customer's
device in, works on it over hours or days, and hands it back later — often after
taking a deposit. Today the shop fakes this with the documents/photocopy plugin:
parts sold as products, labour typed as a custom-amount line. That has no job to
track (who dropped what, when it's promised, what's still owed, is it under
warranty).

This plugin adds a first-class **repair job/ticket** with its own lifecycle, and
— like the documents/photocopy plugin — settles the money as a **real core sale**
(parts + labour on the sales receipt), reusing the POS's sale/stock machinery
rather than reinventing it.

It is a separate, self-contained plugin (`plugins/repairs/`), inert when not
built — same shape as the alternatives / clearance / documents plugins.

## Decisions (from brainstorming)

- **Job tracking is the point**, not just billing — drop-off now, collect later,
  with a promised return date and a status.
- **Money settles as a real sale, like photocopy**: at collection the parts +
  labour ring up on a normal core sale and appear on the sales receipt (revenue
  and stock booked by core). Both a **cashier-menu** flow and an admin flow.
- **Deposits are optional** ("sometimes they pay, sometimes not") and must be
  **tracked against the job** — never a loose untracked amount in the drawer.
- **Parts leave stock at pickup**: parts are listed on the job as they are used,
  and physically leave stock when the collection sale rings. A cancelled job
  consumed nothing.
- **Admin can run the whole thing himself** (small shops: the admin is the
  cashier) — create, add parts, set labour, take deposit, and collect payment,
  without handing off to a separate cashier.
- **Core report code is not modified.** Repairs appear in core Sales/P&L because
  they *are* sales; anything repair-specific comes from plugin hooks.
- **A dedicated Repairs report section** is provided by the plugin (its own page
  via a `ReportCard`).
- **Repair warranty**: each job carries a warranty period; the shop tells the
  customer how long the repair is covered, and it prints on the receipt.
- **Repair type + device model** are free-text with a **datalist of the distinct
  values already used** (like the expense-type / bill-type combos) — no master
  tables to maintain; the list grows itself.
- **Scannable ticket**: each job has a ticket code that prints as a barcode to
  stick on the device. Scanning it in the till or admin scan box opens the repair
  instead of a product lookup.
- **Parts carry per-item discounts, and jobs carry custom charge lines** (labour
  and any named charge) — reusing the discount pattern already in the POS.
- **Customer is a real record**: at drop-off you **pick an existing customer or
  register one on the spot** (the till's customer picker, deduped by phone), so
  there is contact info if the repair goes wrong. A no-phone walk-in can stay a
  name-only snapshot.
- **Deadline can be extended** (with the customer's agreement): the promised date
  is editable, and each change is recorded with a reason in the audit trail.

## Money model (settle as a core sale)

Mirrors the documents plugin: each billable thing is a core product, and
collection creates a normal `Core.Sales` sale so revenue, COGS, stock depletion,
the receipt, and every core report work with zero core changes.

- **Line items on the collection sale:**
  - **Parts** — the real product for each part used (qty × charge, minus any
    per-item discount). Consumes stock through the normal sale at checkout
    (= pickup). Adding a part to a job *lists* it; stock drops when the sale rings.
  - **Charges** — each custom charge line (labour and any named charge) rings as a
    plugin-managed **"Repair labour/service" product** carrying that line's amount,
    described `Repair R-0001: <label>`.
- **Deposits / advances** (optional, taken any time before collection, from
  **either** the cashier "🔧 Repairs" menu **or** the admin page):
  - Booked as `CashRegister.PayIn` (real cash into the drawer) **plus** a
    `repair_payments` row (kind=`deposit`) so it is tracked against the job.
  - At collection the sale is tendered as **`wallet` = total deposits** (a non-cash
    settlement — only `cash` touches the drawer, verified in `sales/tender.go`) +
    **`cash`/`card` = balance**. Net drawer over drop-off + collection =
    deposit + balance = total; the deposit shows on the receipt as already paid.
- **Cancel before collection**: refund any deposits via `CashRegister.Withdraw`;
  no stock to return (parts weren't consumed — the sale never rang).
- **Two collection paths, one result** (both call `Core.Sales`):
  - **Cashier menu**: a "🔧 Repairs" root lists open jobs; picking one injects its
    parts + charge lines as cart lines (the documents `pos-add-service` pattern) and the
    normal till checkout completes the sale.
  - **Admin collect**: the admin Repairs page collects directly — the plugin
    builds the same sale via `Core.Sales.Create` with the wallet+cash tenders — so
    the admin never has to route it to a cashier.
- **Post-sale link**: a post-checkout `record` hook (like documents
  `/cashier/documents/record`) links the sale to the job, flips status to
  `collected`, stamps `collected_at` + `warranty_until`.

## Data model (plugin migration `0001`)

`repair_jobs`
- `id`
- `ticket_no` TEXT — human ref `R-0001` (plugin-local sequence)
- `ticket_code` TEXT UNIQUE — the scannable code (e.g. `RPR000123`); prints as a
  barcode on the drop-off ticket
- `customer_id` BIGINT NULL — linked core customer (find-or-create by phone)
- `customer_name` TEXT, `customer_phone` TEXT — captured snapshot (also the search
  fields); a walk-in with no phone can stay snapshot-only
- `repair_type` TEXT — free text w/ datalist of distinct existing values
- `device_model` TEXT — free text w/ datalist of distinct existing values
- `fault` TEXT
- `status` TEXT — `received | in_progress | ready | collected | cancelled`
- `promised_date` DATE NULL — drives the "time remaining" countdown + auto-urgency
- `urgent` BOOLEAN NOT NULL DEFAULT false — manual rush flag (auto-urgent when
  overdue/near-due is derived, not stored)
- `warranty_days` INT NOT NULL DEFAULT 0
- `warranty_until` DATE NULL — stamped at collection
- `sale_id` BIGINT NULL — the core sale created at collection
- `rework_of` BIGINT NULL — set for a no-charge warranty rework of an earlier job
- `notes` TEXT
- `created_by`, `created_at`, `ready_at` NULL, `collected_at` NULL,
  `cancelled_at` NULL

`repair_parts`
- `id`, `job_id` FK, `product_id` BIGINT, `qty` NUMERIC, `unit_charge` NUMERIC
- `discount` NUMERIC, `discount_type` TEXT (`fixed|percent`), `discount_value`
  NUMERIC — per-item discount, same shape as sales / receiving

`repair_charges` — custom charge lines (labour + any named charge)
- `id`, `job_id` FK, `label` TEXT, `amount` NUMERIC

`repair_payments`
- `id`, `job_id` FK, `amount` NUMERIC, `kind` TEXT (`deposit | refund`),
  `user_id`, `created_at`
  (Balance/final payment lives on the sale, not here.)

Config/setup: a `repairs_labour_product_id` (the service product used for labour
lines) and a default `warranty_days`, created/seeded on plugin setup — same idea
as the documents service products.

Ticket sequence: a plugin-local counter, consistent with other plugins; not
wired into the core `receiptSequences`.

## Lifecycle

```
received ──▶ in_progress ──▶ ready ──▶ collected   (collection rings the sale)
   │              │            │
   └──────────────┴────────────┴──▶ cancelled       (only before collected)
```

- **received**: created at drop-off — device, fault, customer, promised date,
  optional deposit.
- **in_progress / ready**: status nudges (`ready_at` stamped at first `ready`);
  parts, charges, and the **promised date are editable** until collection — a
  deadline extension records a reason to the audit trail.
- **collected**: rings the sale (parts + charge lines), applies deposits as a
  wallet tender, collects the balance, stamps `collected_at` + `warranty_until`,
  links `sale_id`. Immutable after.
- **cancelled**: only before collection; refunds deposits; consumed nothing.

Totals: `total = Σ(charge.amount) + Σ(part.qty × part.unit_charge − part.discount)`;
`deposit_paid = Σ(repair_payments deposit − refund)`;
`balance_due = total − deposit_paid` (collected as cash/card at pickup).

## Warranty

- `warranty_days` on the job (default from plugin setting, editable per job).
- On collection: `warranty_until = collected_at + warranty_days`; shown on the job
  and printed on the sale/collection receipt ("Warranty until YYYY-MM-DD").
- Job list flags in-warranty jobs.
- **Warranty rework**: from a collected in-warranty job, "Redo under warranty"
  creates a linked job (`rework_of`) with labour 0. Parts on the rework still ring
  on its (zero-or-parts-only) sale so warranty cost is real. Deeper core Losses &
  Recovery integration is future work.

## UI

**Cashier** (`CashierMenuRoot` "🔧 Repairs", mirroring documents' menu):
- Root: **Apply a repair** (new job) + a list of open jobs (awaiting pickup).
- **Apply a repair** inline fragment: **repair type** and **device model** (each a
  text input backed by a `<datalist>` of distinct existing values), **customer
  (pick existing or register on the spot)**, fault, promised date, warranty days,
  and an optional **advance/deposit** taken
  right there; add parts (existing product search, with per-item discount) and
  custom charge lines.
- "Collect" injects the parts + charge lines as cart lines for the normal till
  checkout; the post-checkout `record` finalises the job. If the completed sale
  contains a repair, the till **prompts** to also print the repair receipt (below).

**Admin** (`AddAdminNav` "Repairs"):
- Job list by status + search (ticket / customer / phone / device); the default
  view is the **remaining (open) repairs queue** with, per row, the **time
  remaining vs promised date** and an **urgent marker** — red when overdue or the
  manual rush flag is set, amber when due soon. Plus "ready for pickup" and "in
  warranty" views.
- Job detail: full edit, take deposit, move status, **Collect** (rings the sale
  directly via `Core.Sales`), Cancel (refund), and Redo-under-warranty.
- Prints: drop-off **ticket** (customer copy) + reuse the core **sale receipt**
  at collection (warranty line added). Shared escpos header/title/footer.
- `DashboardCard`: jobs ready for pickup / overdue vs promised date.
  `PaletteEntry` for quick nav.

## Scannable ticket & scan routing

- Each job has a `ticket_code` with a distinct prefix (`RPR`) that prints as a
  **barcode on the drop-off ticket** to stick on the device.
- The till and admin scan boxes recognise the `RPR` prefix and, instead of a
  product lookup, **open that repair** (cashier → its Repairs menu detail; admin →
  its job page), showing at a glance the **time remaining until the promised date**
  ("due in 2 days" / "overdue by 1 day") and the status. This reuses the
  special-prefix scan trick the Quick-Sell `KQ…` handoff already uses in the till
  scan handler — a small prefix check dispatched to the plugin, not a rewrite of
  scanning.
- Datalist suggestions come from plugin endpoints returning
  `SELECT DISTINCT repair_type` / `SELECT DISTINCT device_model` from
  `repair_jobs` (same pattern as the smart expense-category combo) — no master
  tables, the lists grow themselves.

## Reporting

- Repairs are real sales, so they appear in core Sales, P&L, receipts, and
  Activity **without touching core report code**.
- A plugin **`ReportCard` → dedicated Repairs report page**: jobs in a date range
  with status, revenue (labour + parts), parts cost, outstanding deposits,
  warranty jobs, and rework rate. Its own page under the plugin's admin routes.
- `ActivityContributor` feeds job events (created / ready / collected / cancelled)
  into the central Activity view; hidden system/developer account excluded.

## Receipts (two receipts, one sale)

A collection sale can hold a repair line alongside other ordinary products, so the
two receipts serve different needs:

- **Sales receipt** (the customer's default, and the only one they get unless they
  ask): the repair shows as a normal priced line — the labour charge and the parts
  — plus a "Warranty until YYYY-MM-DD" note, exactly like any other sale.
- **Repair receipt** (separate, detailed): ticket no, repair type + device model,
  fault, the parts used (with any discount), the charge lines (labour/custom),
  deposit + balance, and the warranty (until date + days remaining). It is NOT
  printed automatically.

How the repair receipt is reached:
- A plugin **`ReceiptTab` "Repairs"** adds a Repairs tab to the core Receipts
  section, listing repair jobs/sales; from there any repair receipt can be
  reprinted by ticket / sale (reuses the existing ReceiptTab hook, like the
  recharge/documents receipt tabs).
- At checkout, when the completed sale contains a repair, the till **prompts** to
  also print the repair receipt then and there.

## Reuse & conventions

- Sale: `Core.Sales.Create` (admin path) + the cashier cart injection pattern.
- Customer pick/register: **`Core.Customers`** (a small additive field on
  `plugin.Core`, exposing the existing `customers.Service` — same instance the till
  uses, so dedup-by-phone and create behave identically).
- Product search: existing `/api/products`; product cost/price via `Core.Products`.
- Deposit/refund cash: `Core.CashRegister.PayIn` / `.Withdraw`.
- Stock: consumed through the sale's product lines (no separate Stock.Consume).
- Plugin plumbing: `//go:embed migrations/*.sql` + goose, `reg.Admin()`,
  `reg.Cashier()`, `reg.AddAdminNav`, `reg.AddCashierMenuRoot`, `reg.AddReportCard`,
  `reg.AddReceiptTab`, `reg.AddActivityContributor`, `reg.AddDashboardCard`,
  `reg.AddPaletteEntry`.
  `plugin.json` key `repairs`; enabled in the default build like the others.
- Front-end: templ + HTMX + Alpine; `response.RenderPage/RenderFragment`.

## Interacting with core: hooks, not edits

The rule: the core POS backend is **not modified in behaviour** — where the plugin
needs core to call into it, we add a **hook (an additive seam)** and register into
it, exactly like the existing `ProductBadgeProvider` / `ProductSaleSuggestionProvider`
/ `CashierSupplierAction` / `ReceiptTab` seams. Core stays plugin-agnostic; the
plugin plugs in.

What the plugin uses:

- Core services already on `plugin.Core`: `Sales`, `Stock`, `CashRegister`,
  `Products`, `Audit`, `Settings`, `DB`.
- Existing endpoints from the plugin's own front-end: `/api/customers` (GET to
  pick/search, POST to register — the till's dedup) and `/api/products` (part
  search). Reusing an endpoint from a plugin page is not a core change.
- Existing registration hooks: `AddAdminNav`, `AddCashierMenuRoot`, `AddReportCard`,
  `AddReceiptTab`, `AddActivityContributor`, `AddDashboardCard`, `AddPaletteEntry`.
- The plugin's own tables (via `Core.DB`) for job, parts, charges, payments.

**One new hook** (the only core addition — a seam, not a behaviour change):

- `ScanResolver` (working name) — the till and admin scan boxes, before treating a
  scanned code as a product, ask registered resolvers if the code is theirs. A
  resolver returns nil to pass, or an action (open URL / fragment) to claim it. The
  repairs plugin registers one that claims the `RPR` prefix and opens the matching
  job. This is how a scanned ticket routes to the repair globally without the scan
  handlers knowing anything about repairs — same shape as the other provider hooks.

**Inert without the plugin.** With no resolver registered (a core-only build, or the
repairs plugin disabled), the hook is a no-op and the scan boxes behave exactly as
today. The core must build and run identically whether or not the repairs plugin is
present — same guarantee every existing seam gives. The implementation plan verifies
this: core-only build green, scan unchanged, before the plugin is wired in.

## Testing

- Money math (money path → tested): job total, balance-due after deposits,
  warranty-until, and the wallet+cash tender split (deposit + balance = total).
- Rollback DB test (like `receiving_discount_test`): create a job, take a deposit,
  add a discounted part + a custom charge, collect, and assert — a core sale exists
  with the part + charge lines (part discount applied), stock decremented once at
  collection, the sale's tender = wallet (deposit) + cash (balance), drawer
  expected rose by deposit + balance only, `warranty_until` stamped, and the job
  links `sale_id` + status `collected`.

## Phasing

This plugin is larger than the others, so build it in two phases — each its own
implementation plan and shippable on its own.

**Phase 1 — the working repair desk (the core value):**
- Job entity + lifecycle (received → in_progress → ready → collected / cancelled).
- Customer pick/register (`Core.Customers` seam) + captured snapshot.
- Repair type + device model with datalists (cheap, central to the flow).
- Parts (+ per-item discount) and custom charge lines.
- Collect = a real `Core.Sales` sale (parts + charges); deposits via the
  `wallet`-tender reconciliation.
- Warranty period → until date, on the sales + repair receipts.
- Admin page (queue list, detail, collect, cancel, take deposit) **and** the
  cashier "🔧 Repairs" menu (apply, add, take advance, collect).
- Repair receipt + Receipts → Repairs tab + checkout print prompt.
- Repairs report, Activity feed, dashboard "ready for pickup" card.

**Phase 2 — the polish that makes it fast on the floor:**
- Scannable ticket barcode + scan-routing in the till/admin scan boxes (the
  second core touch), with the promised-date countdown shown on scan.
- Urgent markers + overdue/near-due highlighting + the remaining-repairs queue
  ordering.
- Deadline extension with a recorded reason.
- No-charge warranty rework (linked job).

## Out of scope (v1)

- Routing repair money to a specific locker instead of the till drawer (Core
  exposes only the till drawer to plugins).
- Serial/IMEI tracking, SMS "ready" notifications, technician assignment/payroll.
- Deep core Losses & Recovery integration for warranty-rework cost.
