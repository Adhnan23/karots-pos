# Unified Receipts (Sales · Cash · Warranty) — Design

**Date:** 2026-06-28
**Branch:** `main` (cashflow-lockers already merged)
**Status:** Approved design — ready for implementation plan.

## Problem

Every cash touchpoint now emits a tracked, numbered `CR-` money receipt (see
`2026-06-24-money-receipts-design.md`), but those receipts are **viewable and
reprintable only in the admin panel** (`/admin/money-receipts`). A cashier who
collects a credit payment or pays out a cash refund gets a slip printed at the
moment, yet cannot go back later to find, view, or reprint that `CR-` receipt.
The cashier "Receipts" page (`/cashier/receipts`) lists **sale receipts (`S-`)
only**.

Separately, warranty replacements print a slip at the time but are **not
reprintable at all** — the `warranty_claims` row is persisted, but no screen
re-renders its slip.

Goal: one **Receipts** surface — on both the cashier terminal and the admin
panel — where staff can find, view, and reprint every kind of receipt the shop
hands out: sales, cash movements, and warranty replacements.

## Decisions (locked with user)

- **Three tabs:** **Sales (`S-`) · Cash (`CR-`) · Warranty**. Warranty is neither
  a sale nor cash, so it gets its own tab rather than being folded into Cash.
- **Cashier sees all `CR-` receipts shop-wide** (not just their own session) —
  simplest, and a cashier reprinting another till's receipt is acceptable here.
- **Warranty is in scope:** warranty replacements become listable + reprintable,
  reconstructed from the existing `warranty_claims` record (no new table).
- **Unify admin too:** admin gets the same three-tab Receipts hub. Existing deep
  pages (`/admin/money-receipts`, warranty pages) stay in place — no breakage.

## Approach

**Per-tab HTMX fragments** (chosen over a single merged UNION read-model). Each
receipt type keeps its own repository/query; the Receipts page is a shell whose
three tabs lazy-load a table fragment per type. This matches the existing
`ReturnsTable` / `CreditTable` / `WarrantyTable` idiom, requires **no schema
change** and **no UNION**, and lets each tab paginate/search independently. The
working sales list is left untouched.

## Components

### Data / service layer

- **cashflow `ReceiptService`** — already has `List(ctx, ReceiptFilter)` and
  `Get(ctx, id)` (built for admin). Reused as-is for the cashier Cash tab
  (shop-wide). No change.
- **sales `ListFilter`** — already powers the cashier Sales tab. No change.
- **warranty** — add two read methods (today only `ClaimsForUnit(unitID)`
  exists):
  - `Repository.ListClaims(ctx, f ClaimFilter) ([]Claim, error)` where
    `ClaimFilter{Search string; From, To *time.Time; Limit int}` — recent claims
    across all units, newest first, joining product name, old serial, replacement
    serial, customer name, handled-by name, created_at. `Search` matches old/new
    serial or customer (ILIKE); `From`/`To` filter by claim `created_at` for the
    shared date-range presets.
  - `Repository.GetClaim(ctx, claimID int64) (*Claim, error)` — one claim with
    the same joined fields, enough to rebuild the slip.
  - `Service.ListClaims` / `Service.GetClaim` wrappers.
  - The `Claim` struct gains the joined display fields it doesn't already carry
    (product name, old serial, customer name, warranty-until of the replacement
    unit) so both the table row and the reprinted slip render without extra
    round-trips.

### Web layer — cashier

New routes under the existing `cg` (cashier) group:

```
GET  /cashier/receipts            page shell, 3 tabs (existing handler, gains tabs)
GET  /cashier/receipts/sales      sales table fragment   (q + date range)
GET  /cashier/receipts/cash       CR- table fragment     (q + kind + date range)
GET  /cashier/receipts/warranty   warranty-claims table fragment (q + date range)
GET  /cashier/money-receipts/:id  CR- receipt view page (cashier layout)
POST /cashier/money-receipts/:id/print   reprint CR- slip → toast
POST /cashier/warranty/:claimId/print    reprint warranty slip → toast
```

- The **Sales tab** uses `sales.List`; its existing search already matches
  receipt number + customer name/phone. It gains the shared date-range presets.
- The **Cash tab** fragment calls `cashflowReceipts.List` (shop-wide) with the
  shared date-range presets, the existing kind filter, and a text search.
- The **Warranty tab** fragment calls `warranty.ListClaims` with search +
  date-range presets.

See **Filters** below for the unified filter set applied to all three tabs.
- **CR- view page**: lift the receipt HTML view so it renders inside the cashier
  layout too (currently admin-only). Reprint posts to the cashier print route,
  which reuses `Server.printMoneyReceipt` / `buildReceiptSlip` (best-effort slip
  + toast; cashiers can't redirect to the admin receipt page).
- **Warranty reprint**: reload the claim via `warranty.GetClaim`, reconstruct
  `escpos.WarrantySlip` (the same fields `printWarrantySlip` builds today), send
  best-effort, return a toast. Factor the slip-building out of the current
  `printWarrantySlip` so the create path and the reprint path share it.

### Web layer — admin

New unified hub `GET /admin/receipts` with the same three tabs, reusing existing
machinery as the tab fragments:

- **Sales tab** — `sales.List` (links to the existing admin sale detail/print).
- **Cash tab** — reuse the existing `MoneyReceipts` list rendering + its
  view/reprint routes (`/admin/money-receipts/...`, unchanged).
- **Warranty tab** — `warranty.ListClaims`, with view + a new admin warranty
  reprint route `POST /admin/warranty/:claimId/print` (shares the same slip
  builder).

Nav: the Money section's "Cash Receipts" entry points at the new `/admin/receipts`
hub (Cash tab default), keeping the deep `/admin/money-receipts` reachable.

### Templates

- `templates/pages/cashier/receipts.templ` — gains a tab strip + per-tab table
  fragments (Sales / Cash / Warranty), following the existing cashier table-row
  styling. New `cashierpages` fragments for the Cash and Warranty tables and a
  cashier CR- receipt view.
- `templates/pages/admin/receipts.templ` — admin hub shell reusing the money-
  receipts table partial + a warranty-claims table partial + a sales table
  partial.
- Reuse the shared `rptRangeForm` date-preset component on every tab.

## Filters

The same filter bar sits above all three tabs (cashier and admin), so the
experience is consistent — only the columns differ per type:

- **Date range** — the shared `rptRangeForm` / `reports.ResolveRange` preset
  buttons (Today / This week / This month / … + exact dates), already used by the
  locker ledger, admin money-receipts, and reports. Applied to all three tabs
  (Sales by sale date, Cash by `created_at`, Warranty by claim `created_at`).
- **Text search (`q`)** — one search box per tab, matching the natural identifiers
  for that type:
  - **Sales** — receipt number + customer name/phone (already supported).
  - **Cash** — receipt number, **party**, and from/to labels. Because a `CR-`
    receipt stores the trading counterparty in its `party` column, this single
    search covers **both customers** (credit collection, refunds) **and
    suppliers** (supplier payments) **and** the location labels (locker/till
    names) — no separate customer/supplier boxes needed.
  - **Warranty** — old/new serial + customer name.
- **Kind filter (Cash tab only)** — the existing money-receipt kind dropdown
  (transfer / expense / supplier_payment / customer_payment / refund / bank_charge
  / …), reusing what `/admin/money-receipts` already has.

Filters are plain query params on each tab's HTMX `GET`, so deep links and the
date presets compose naturally. Party search already covers suppliers and
customers today: the supplier-payment, customer-payment, credit-collection, and
refund moves all pass the counterparty name as `Party` on their
`cashflow.MoveInput` (verified). No migration or call-site change needed.

## Data flow

1. Staff opens **Receipts** → default tab renders server-side; switching a tab
   issues an HTMX `GET` for that tab's fragment.
2. **Reprint** posts to the type-specific print route → server reloads the record
   → rebuilds the ESC/POS slip via the shared builder → `escpos.Send` /
   `printing.Raw` best-effort → returns a toast. No record is mutated.
3. **View** (`CR-`) renders a print-friendly HTML page with a browser Print
   button + a Reprint-slip button (mirrors the admin receipt page).

## Error handling

- Print failures are non-fatal (logged + toast "couldn't reach printer"), exactly
  as the existing slip paths behave — a printer problem never blocks the screen.
- Missing/invalid id → 404 via `apperr.NotFound`.
- Reprint is idempotent and read-only; no transactions needed.

## Testing

- `make templ && go build ./... && go vet ./...` green.
- E2E (Playwright + psql, dev port 3000, admin 0000000001/2273):
  - Cashier Receipts shows all three tabs; each tab has the shared date-range
    presets. **Cash** lists the `CR-` receipts created in slice-7/credit-pay
    testing (shop-wide), searchable by number, by **customer** party (credit
    pay / refund), and by **supplier** party (supplier payment), filterable by
    the kind dropdown and by date preset.
  - Cashier **reprint** of a `CR-` re-sends the slip (toast), record unchanged.
  - Cashier **View** of a `CR-` renders the receipt with shop header.
  - Warranty tab lists a recorded replacement; **reprint** re-sends the warranty
    slip; record unchanged.
  - Admin `/admin/receipts` shows the same three tabs; Cash + Warranty reprints
    work; existing `/admin/money-receipts` deep page still works.
  - Sales tab unchanged (existing `S-` search still works).

## Out of scope (YAGNI)

- No new tables; no UNION read-model.
- No merging of sales + cash into one list (tabs keep them separate).
- Returns keep their existing goods-return slip; the cash refund simply appears
  in the Cash tab as its `CR-` receipt.
- No per-session scoping of the cashier Cash tab (shop-wide by decision).
- No change to how receipts are generated — this is a view/reprint surface only.
