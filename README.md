# 🏪 karots-pos

Production-grade point-of-sale for a Sri Lankan shop: a **cashier terminal** (sell,
scan, price/stock check) and an **admin panel** (manage catalog, inventory, sales,
purchasing, finances, settings). Built with Go · Echo · sqlx · Goose · Templ · HTMX
· Alpine.js · Tailwind, backed by PostgreSQL 17.

The whole app compiles to **one fully static binary** — templates, CSS/JS (a
prebuilt Tailwind stylesheet plus vendored htmx / Alpine / JsBarcode), and DB
migrations are all embedded via `go:embed`. To deploy you ship **just the binary +
a `.env`** (and a Postgres).

## Quick start (dev)

```bash
cp .env.example .env          # adjust JWT_SECRET (>= 32 chars)
make db-up                    # start Postgres 17 in Docker
make migrate                  # apply schema
make seed                     # staff users, shop settings, catalog, suppliers, customers
make run                      # http://localhost:3000
```

The seed is **entities only** — staff users, the shop's identity, a nested
category tree, 8 stocked products, suppliers and customers — so the dashboard and
reports start from a clean, zero-transaction state. It's idempotent: it skips if
any users already exist (so reseeding wants a fresh database — see
[Reset the database](#reset-the-database)).

For a **populated demo** instead, run `make demo` in place of `make seed`: it seeds
the same entities and then layers backdated sample transactions on top — purchases,
~10 sales (retail/wholesale/credit, with discounts), a customer repayment, returns,
cash-register sessions and expenses — so every report and chart shows realistic data.
Like the seed it's idempotent (skips if any sale exists).

Open <http://localhost:3000> and sign in with **phone number + PIN** — the server
routes you to the admin panel or the cashier terminal automatically based on your
role (there is no admin/cashier toggle on the login screen):

| Role | Phone | PIN |
|---|---|---|
| Admin | `0771234567` | `1234` |
| Manager | `0772222222` | `2222` |
| Cashier | `0771111111` | `1111` |

### Reset the database

The seed/demo only populate an empty database. To wipe everything and start fresh
**in one command** — handy for re-running a demo, and for hosted databases (e.g.
Neon) where there's no Docker volume to drop:

```bash
make reset                    # DROP SCHEMA + re-run migrations → empty DB
make reset-seed               # …then re-seed entities
make reset-demo               # …then re-seed the full demo dataset
```

These run `DROP SCHEMA public CASCADE` and let migrations rebuild everything, so
they work against any Postgres over `DATABASE_URL`. **Stop the running server first**
(the drop needs no competing connections). As a safety rail, `-reset` refuses when
`APP_ENV=production` unless you also pass `-force`:

```bash
./bin/karots-pos -reset -demo -force
```

Alternatively, on the local Docker Postgres you can still drop the volume:

```bash
docker compose down -v        # drop the Postgres volume (all data gone)
make db-up                    # fresh Postgres
make seed                     # re-applies migrations on start, then seeds
```

## Self-contained binary (deploy)

```bash
make build                    # → bin/karots-pos  (CGO-free, fully static)

# run it anywhere — no static/ dir, no templates on disk, just env + binary:
export DATABASE_URL=postgres://user:pass@host:5432/db?sslmode=disable
export JWT_SECRET=$(openssl rand -hex 24)
./bin/karots-pos -migrate      # apply schema (run once / on deploy)
./bin/karots-pos -init         # create the shop's first admin (real deploy)
./bin/karots-pos               # serve on :3000
```

Migrations also run automatically on every start, so `-migrate` is optional.

### `-init` vs `-seed`

For a **real shop's first boot**, use `-init`: it creates a single **admin** account
and nothing else, leaving the catalog empty and the shop identity at its neutral
default (`My Shop`) so the owner configures everything in the UI. The admin is
forced to choose their own PIN on first login. Defaults can be overridden:

```bash
POS_ADMIN_NAME="Jane"  POS_ADMIN_PHONE=0771112222  POS_ADMIN_PIN=4321 \
  ./bin/karots-pos -init
```

`-seed` is the **development/demo** dataset (staff users, "Karots Super Mart"
identity, a nested category tree, 8 stocked products, suppliers and customers) —
**do not run it on a production install.** Both commands are idempotent (they skip
if any users already exist) and exit without serving.

`-demo` builds on `-seed` and adds backdated sample **transactions** (purchases,
sales, returns, a customer repayment, cash-register sessions, expenses) for a
fully-populated demo; it skips if any sale exists. `-reset` wipes the database
(`DROP SCHEMA` + re-migrate) and can be combined: `-reset -seed` or `-reset -demo`
repopulate in one step (see [Reset the database](#reset-the-database)). Both are
**dev/demo only** — `-reset` refuses on `APP_ENV=production` unless given `-force`.

### Full stack in Docker

```bash
JWT_SECRET=$(openssl rand -hex 24) make docker-up   # postgres + server
docker compose run --rm server -seed                # one-time seed
```

## Plugins & per-shop builds

The POS ships as a frozen **core**; optional features are **plugins** compiled in
**per shop at build time** (still one static binary, no runtime `.so`). A plugin
lives entirely under `plugins/<name>/` — its own Go package, Goose `migrations/`,
Templ pages and a `plugin.json` manifest — and weaves into the core shell through
additive hooks (admin nav, cashier tab, dashboard/settings cards, command
palette, POS actions, **payment tenders**) plus whole-route overrides via the
plugin Mux. Core stays untouched apart from a few tiny generic seams.

**Late-enable, never a wipe.** Each plugin tracks its schema in its **own**
`goose_db_version_<name>` table, independent of core. So a shop can buy/enable a
plugin *after* going live: on the next boot only that plugin's migrations apply
(additively, with idempotent backfill) on top of the existing data — core's
migrations and data are never touched or re-created.

The committed default (`cmd/server/enabled_plugins.go`) imports **no plugins** =
a core-only binary. To build a shop's binary with plugins compiled in, use the
**bootstrapper**:

```bash
make bootstrap                 # interactive: pick plugins + target OS

# non-interactive (CI / scripted):
make bootstrap ARGS="-plugins recharge -os linux  -name acme-pos"
make bootstrap ARGS="-plugins all      -os windows -name acme-pos"
go run ./cmd/bootstrap -plugins recharge -os darwin -name acme-pos -yes
```

It discovers `plugins/*/plugin.json`, rewrites `enabled_plugins.go` with the
selected imports, runs `templ generate` + Tailwind + a static `CGO_ENABLED=0`
build, merges each plugin's `env.sample` into `dist/.env.sample`, then **restores
`enabled_plugins.go`**. Output lands in `dist/` (binary + `.env.sample`); deploy
it exactly like the core binary (`-migrate` / `-init`, then serve). Flags:
`-plugins` (keys or `all`), `-os` (`linux`/`windows`/`darwin`), `-arch`
(`amd64`), `-name`, `-out` (`dist`), `-yes`.

### Bundled plugin: Mobile Recharge (carrier mobile-money agent)

`plugins/recharge` turns the till into a carrier agent (Dialog **eZ Cash**,
Mobitel **mCash**, …). A per-device **reload balance** (the device's working
float — labelled "reload balance" in the UI) drives both airtime reload *and* the
wallet payment system, while **banks** (core `kind="bank"` lockers) are the money
source for bill payments and cash hand-outs:

- **Reload** at the POS (a hidden non-stocked service line — on the receipt,
  counts as a sale, not returnable, no stock movement).
- **Device money transactions** on a dedicated cashier screen: cash-in/deposit,
  cash-out/withdrawal and supplier reload top-up — each posts the matching
  cash-drawer movement (top-ups also book an expense) and prints a slip.
- **Bill-pay / get-money** — a **bank** is a core `kind="bank"` locker (created
  and topped up on the core Lockers page). Cashiers **pay a bill** (bank balance
  ↓, cash in) or **get money** (bank balance ↑, cash out), with an optional
  service charge added to the drawer; an overdraw on bill-pay is hard-blocked.
  The money moves through core cash-flow, so it lands in the combined Cash Flow /
  net-position view alongside the till.
- **Wallet tender** — a customer can pay a normal product sale by wallet
  transfer; it credits the device reload balance and stays out of the cash drawer.
- **Per-device reconciliation** — cashier enters each device's counted opening &
  closing balance; the system computes the per-carrier expected balance and the
  bonus/loss (carrier commission is unpredictable, so it surfaces at close).
- **Admin** — carrier & device management and a reconciliation/ledger report;
  banks are managed as core lockers (live balances on the Lockers page).

### Bundled plugin: Communication Store (photocopy / print / laminate / bind)

`plugins/documents` runs the communication-store counter as a tabbed quick-action
at the till (a `🖨 Photocopy` tab beside `📶 Reload`):

- **Metered jobs** — pick a service, size, colour and side; the price is resolved
  server-side from a per-service matrix with **quantity-tier** breaks, and the
  job **consumes paper/film from normal stock** via the core consume-on-sale seam
  (double-side = 1 sheet / 2 impressions). The receipt shows the full line
  ("A4 colour 2-side ×20") and the sale line freezes the consumed FEFO cost as COGS.
- **Custom labour jobs** (photo edit / CV / poster) — cashier types the price and,
  optionally, a worker's "mini salary"; labour accrues to a per-worker balance and
  a **Pay worker** action books a core expense and settles the jobs.
- **Paper & film are ordinary products** — bought/refilled through purchasing,
  sellable directly, and mapped as a service's consumables (a search picker; the
  size is chosen from the service's own price-matrix sizes to avoid mismatches).
- **Admin** — service / pricing / consumable management, a revenue–consumables–
  labour report, and the worker-payouts page. Own `goose_db_version_documents`.

## Two surfaces

| Surface | Path | Who | Does |
|---|---|---|---|
| **Cashier terminal** | `/cashier` | all roles | barcode scan, product search, live cart, retail/wholesale/credit checkout, **split-tender payments (cash/card/online)**, **hold/park & resume sales**, **count-by-denomination drawer open/close**, **mid-shift withdrawals**, **day-end Z-report**, thermal receipt (80mm/58mm) + **reprint**, **returns/refunds**, **damage write-off**, **credit collection**, **serial/warranty lookup & replacement**, **suppliers at the counter** (opt-in per user) |
| **Admin panel** | `/admin` | admin, manager | dashboard + alerts, products, inventory & **FEFO batches**, sales + **partial returns**, purchasing (GRN) + **supplier returns**, suppliers, customers & credit, expenses, **finance/profit** (net of returns, with **losses & recoveries**), reports (incl. **customer dues**, **returns**, **profit-by-category**, **sales-trend**, **warranty**), **cash register sessions & denominations**, **categories (nested)**, units, **conversions**, **barcode labels**, **warranty tracking + supplier recovery**, **damage report**, users, **audit log**, settings + **backup/restore** |

Both call the **same services**. The cashier UI talks to the JSON API
(`/api/*`); the admin panel is server-rendered HTML with HTMX partials.

### Suppliers at the counter

In a small shop the cashier is often alone when a supplier walks in. Ticking
**Can deal with suppliers** on a user (Admin → Users, off by default) adds a
**Suppliers** tab to the terminal, where that person can:

- **pay a supplier** — cash leaves their own drawer, or a locker the owner has
  marked *Cashiers can use*, and produces a `CR-` receipt;
- **take in a delivery** — with or without a prior order, paying all, part or
  none of the invoice in the same step;
- **take an order** — saved as a normal draft PO stamped with their name, and
  printed for the supplier to take away;
- **add a supplier or a product met for the first time** — a supplier by name and
  phone, a product from the delivery line with the cost and price off the
  invoice. New products land in Uncategorized flagged `needs_review`, so they
  appear in **Admin → Products → Review** for a real category and a tidy name.
  Without this a delivery containing anything new could only be half-recorded,
  which leaves the stock and the payable both wrong.

The flag grants **sight of cost prices**, which is inherent to entering a
supplier invoice — that is why it is off until you turn it on. It is read fresh
on every request, so turning it off takes effect on their next click rather than
at their next login. Where the cash may come from is enforced on the server, not
just in the picker: a cashier cannot reach a locker you have kept to yourself,
or another cashier's drawer.

Paying a supplier and receiving the goods are **one transaction**. If the
payment fails — an empty locker, a closed till — the goods roll back with it, so
stock can never land without the payable that pays for it. Cash handed over at
the counter reduces what the drawer is expected to hold, so the day still
balances.

### Inventory: FEFO batch tracking

Every receipt of goods (GRN), adjustment, return, or conversion creates a
**batch** (`stock_batches`) carrying its own expiry date and cost. Sales and
write-offs deplete batches **first-expiry-first-out**, and the weighted-average
cost of the consumed units becomes the per-line COGS snapshot — so profit reports
are accurate even when costs change between deliveries. `stock.quantity` stays the
atomic oversell guard and a cached aggregate of the batches. Expiry and low-stock
reports (plus dashboard badges) read straight off this ledger.

### Feature highlights

- **Split-tender checkout** — one bill across cash + card + online (with a
  reference per non-cash line); underpayment rolls onto customer credit.
- **Hold / park sale** — suspend the current cart to serve the next customer,
  then resume it from the **Held** list (survives reloads — stored server-side).
- **Cashier receipt reprint** — find a past sale by receipt number and reprint
  the thermal bill from the terminal (`/cashier/receipts`).
- **Z-report (day-end)** — a printable per-session summary: sales totals,
  payments by method, the cash ledger, and expected-vs-counted over/short.
- **Audit log** — who did what (voids, edits, deletes, payments, withdrawals,
  closes, settings, backup/restore), filterable at `/admin/audit`.
- **Customer dues report** — printable receivables/aging snapshot of who owes
  you money, mirroring the supplier-payables report.
- **Backup & restore** — one-click backup download and restore-from-file in
  Settings. Runs **entirely over the database connection** (pure Go, gzipped
  data snapshot) — no `pg_dump`/`psql`, no Docker CLI, nothing to install. Works
  the same whether Postgres is in a container or on a remote VPS. Set `BACKUP_DIR`
  to also run **automatic time-based backups** in-process (same snapshot format;
  default every 6h, keeping the last 28). Point `BACKUP_DIR` at a mounted volume
  or an off-site-synced path — a backup on the DB's own disk won't survive disk loss.
- **Quick-add at the till** — when a cashier scans/looks up an item that isn't in
  the catalog yet, they can sell it anyway with a 2-field quick-add (name + price;
  barcode optional — scanned, generated, or left blank). It's created on the fly,
  its stock seeded so the sale nets to zero ("count later"), and flagged
  **needs review** stamped with who added it. A dashboard banner + an **Items to
  Review** list let the admin finish each one (real category, unit, cost); saving
  the cost also **back-fills the placeholder cost 0 onto its past sale lines**, so
  historical profit becomes accurate. Every missed item is captured once and scans
  normally thereafter.
- **Stock-take / opening stock** — a bulk screen (`/admin/stock/take`) to enter the
  counted quantity (and cost) for many products at once — how a shop already running
  loads the stock it owned before adopting the system. Each change is an audited
  `adjust` movement (not a purchase, so it doesn't hit expenses or supplier dues).
  For one product, **Stock → Adjust** still sets an absolute count.
- **Partial sale returns** — return any quantity of any line; restocks, splits
  refund vs credit, flows `completed → partially_returned → returned`.
- **Purchase returns (debit notes)** — send goods back to a supplier; FEFO
  deplete + reduce the payable.
- **Product conversions** — break a parent product into a child (e.g. 1 bag of
  rice → 25 kg loose), moving stock value across.
- **Nested categories** — sub-categories; filtering by a parent includes all
  descendants.
- **Discounts** — bill-level and per-item discounts, each toggleable between a
  fixed amount (Rs) or a percentage (%); per-item fixed discounts apply per unit.
- **Supplier payments** — record payments against a supplier and allocate them to
  specific open purchase invoices (flipping each to `partial`/`paid`); cash
  payments leave the drawer. A **supplier-dues** (payables aging) report mirrors
  the customer-dues one.
- **Barcode labels** — printed **server-side** straight to a label printer (TSPL,
  e.g. Xprinter XP-365B) — no browser, no driver; an A4 sticker-sheet (JsBarcode)
  is the fallback. Receipts print server-side as ESC/POS (80mm/58mm) with an
  optional shop logo. Each cashier can have their **own counter printer** (a
  `tcp://ip:9100` network address on their user account; falls back to the shop
  default), and a Settings toggle can **auto-print** the receipt on sale completion
  instead of showing the Print/New-Sale prompt. See **[PRINTING.md](PRINTING.md)**.
- **Management reports** — a Reports hub (`/admin/reports`) of filterable,
  **print/Save-as-PDF** reports: sales, finance/P&L, cash register, purchases,
  suppliers, inventory valuation, batches/expiry, low-stock, expiring. Each has a
  date/section filter, a totals row, and a print-optimized layout (no PDF library
  — the browser's print dialog saves the PDF).
- **Cash management** — admin-managed note/coin **denominations**; the cashier
  opens and closes the drawer by **counting pieces** (total computed), can
  **continue with the last close** or count fresh, and records **mid-shift
  withdrawals** (amount + reason). Every cash event — opening, sales (net of
  change), credit collected, withdrawals, close — is logged to a per-session
  ledger that drives the **expected-vs-counted over/short** reconciliation and
  rolls into Finance. Admins audit every session at `/admin/cash-register`.

## Architecture

```
HTTP ─▶ Echo router ─▶ ┌ API handler (JSON)  ┐
                       ├ UI handler (Templ)  ┼─▶ Service ─▶ Repository ─▶ Postgres
                       └ (internal/web)      ┘   (business    (sqlx, accepts
                                                  logic, tx)   *DB or *Tx)
```

- `internal/features/<f>/` — one package per feature: `model + repository + service + api`.
- `internal/web/` — the **UI layer**: HTMX/Templ handlers. It imports feature
  services and templates. Feature packages never import templates.
- `templates/` — Templ components; may import feature model types only.
- `migrations/` — Goose SQL, embedded into the binary.

### Why UI handlers live in `internal/web`, not inside each feature

The original design put `ui_handler.go` inside each feature, importing the
templates — while the templates imported the feature's models. That is a **Go
import cycle and will not compile**. Moving UI handlers into a dedicated `web`
package makes the dependency one-directional (`web → features`, `templates →
feature models`), which compiles and keeps a clean separation.

## Correctness & production hardening (fixes over the original plan)

| Issue in the plan | Fix |
|---|---|
| `//go:embed ../../migrations/*.sql` (illegal — can't escape package dir) | embed lives in `migrations/embed.go` next to the SQL |
| `jmoiern/sqlx` typo; `NamedExec` used with `RETURNING` | correct import; `GetContext`/`RETURNING` to scan back rows |
| Money as `float64`/`string` | `shopspring/decimal` end-to-end (DB `DECIMAL` ↔ `decimal.Decimal`) |
| Concurrent **oversell** race | atomic `UPDATE stock SET qty = qty - $1 WHERE qty >= $1` guard |
| `receipt_no` had no atomic source | dedicated Postgres sequence `sales_receipt_seq` |
| Sale not actually transactional | stock guard + movements + sale + items + payments + credit in **one tx** |
| No CSRF story | `SameSite=Lax` httpOnly session cookie (blocks cross-site POST) |
| `-migrate` flag referenced but missing | implemented (`-migrate`, plus `-seed` / `-demo` / `-reset`) |
| No graceful shutdown, no PIN rate-limit, leaky errors | all added |

Verified live: login → list → barcode lookup → retail sale (stock decrements,
change computed) → **oversell returns 409 with stock unchanged** → credit sale
(customer balance updated, credit-limit guard) → register open/close
(expected-vs-counted reconciliation).

## Make targets

`db-up` · `migrate` · `seed` · `demo` · `reset` · `reset-seed` · `reset-demo` · `run` · `build` · `build-windows` · `bootstrap` · `templ` · `test` · `docker-up` · `docker-down`

## Notes for going further

- **Offline-friendly:** htmx, Alpine and JsBarcode are vendored under
  `static/vendor/` and embedded; Tailwind is a **prebuilt, minified
  `static/css/tailwind.css`** (compiled by `make css` — Node/npx is a *build-time*
  dependency only, never needed at runtime). The UI works with no internet, and
  there's no runtime CDN or in-browser JIT.
- Refresh-token rotation is implemented for API clients; the cookie UI uses a
  shift-length access token (`JWT_EXPIRES_IN=12h`).
- **Login is phone + PIN.** Each user's phone number is unique and is their login
  id; there is no user list on the login page (so staff aren't enumerable). The
  admin "Users" page sets each staff member's phone + PIN.
- **Forced PIN change.** Seeded, admin-created, and admin-reset accounts are flagged
  `must_change_pin`, so the user is redirected to `/account/pin` to choose their own
  PIN on next login. Anyone can change their own PIN any time via the "Change PIN"
  link. A real deploy uses **`-init`** (one admin, empty shop) — see SETUP.md —
  rather than the demo `-seed` credentials below.
- The demo `-seed` credentials (**Admin 0771234567 / 1234**, **Manager 0772222222 /
  2222**, **Cashier 0771111111 / 1111**) are for development only; change them before
  any real deployment.
```
