# Recharge pass-through P&L Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop reloads/recharges being counted as full profit in the core reports, and instead report the shop's real recharge earning (service charge + carrier float commission) — with all recharge knowledge kept in the plugin, not core.

**Architecture:** One *generic* core capability — a `products.pass_through` flag whose face value is excluded from core revenue/COGS/profit — plus a new generic plugin hook (`PLIncome`) that lets a plugin contribute an income line to the P&L, folded in by the `internal/web` layer (which already imports both `plugin` and `reports`, so no core↔plugin cycle). The recharge plugin marks its carrier service-products pass-through and registers a `PLIncome` source returning service charge + realized float commission.

**Tech Stack:** Go + Echo + sqlx + Goose + templ, PostgreSQL. Compile-time plugin framework (`internal/plugin`, package-level hook registry consumed via getters like `plugin.ReportCards()`).

## Global Constraints

- **No recharge-specific code in core.** Core reports may only honor the generic `pass_through` flag and call generic `plugin.PLIncomes()`; they must never mention recharge/float/commission.
- Commission is **realized on close only**: count float bonus only from device sessions closed & counted within the range; open/uncounted sessions contribute 0.
- Commits go to `main`, trailer `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`; user pushes. Run `make build` before `go test ./...`/`go vet ./...`; commit `static/css/tailwind.css` if template classes change. DB-guarded tests skip without `DATABASE_URL` and roll back.
- Behaviour is byte-identical for shops **not** using recharge: `pass_through` defaults false, and with no `PLIncome` sources the P&L is unchanged.

---

### Task 1: Generic `pass_through` product flag (migration + model)

**Files:**
- Create: `migrations/0058_product_pass_through.sql`
- Modify: `internal/features/products/products.go` (Product struct + create/update write path)

**Interfaces:**
- Produces: `products.Product.PassThrough bool` (db `pass_through`); products can be created/updated with it set.

- [ ] **Step 1: Migration**
```sql
-- +goose Up
-- pass_through marks a product whose sale is money passing through the shop, not
-- the shop's own margin (resold airtime, bill face value, gift cards). Core
-- reports exclude these lines from revenue, COGS and profit. Default false keeps
-- every existing product a normal sale.
ALTER TABLE products ADD COLUMN pass_through boolean NOT NULL DEFAULT false;
-- +goose Down
ALTER TABLE products DROP COLUMN pass_through;
```
- [ ] **Step 2: Apply + verify** — `make migrate`; `\d products` shows `pass_through`. `goose down`/`up` round-trips.
- [ ] **Step 3:** Add `PassThrough bool` with `db:"pass_through"` to the `Product` struct (mirror an existing bool like `IsService`). It only needs to be settable by the plugin via a repo helper (Task 5) — the admin product form does NOT expose it (out of scope). Confirm `selectProduct` uses `p.*` or add `p.pass_through` to the column list so scans still work.
- [ ] **Step 4:** `make build` + `go vet ./internal/features/products/...`; commit.

---

### Task 2: Exclude pass-through from core report profit/revenue

Core honors the flag only — no recharge references.

**Files:**
- Modify: `internal/features/reports/reports.go` — `Compute` (revenue, COGS, returns), `ProfitByCategory`, `TopProducts`, `SalesByCashier`
- Test: `internal/features/reports/passthrough_test.go` (create, DB-guarded)

**Interfaces:**
- Consumes: `products.pass_through` (Task 1).
- Produces: report figures with pass-through lines removed.

- [ ] **Step 1: Write the failing DB test.** Seed (in a rolled-back tx): a category/unit, one normal product (cost 60, sell 100, 1 sold) and one `pass_through` service product (sell 500, 1 sold), both in one sale. Assert `Compute` returns `GrossRevenue = 100` (not 600), `GrossProfit = 40`, and `ProfitByCategory`/`TopProducts` contain the normal product but **not** the pass-through one. (Mirror the harness in `internal/features/stock/batch_price_test.go`: `testDB`, tx, rollback. `reports.NewService` takes `*sqlx.DB`; build it on `tx` via a small local constructor or call the exported `Service` with the tx if supported — otherwise assert at the repo-query level.)
- [ ] **Step 2: Run — expect FAIL** (`GrossRevenue = 600`). `DATABASE_URL=… go test ./internal/features/reports/ -run PassThrough -v`.
- [ ] **Step 3: Revenue (header-based).** `Compute`'s gross query is `SUM(sales.total)` (`reports.go:91`). Subtract pass-through line value: after computing `head.Gross`, subtract
```sql
SELECT COALESCE(SUM(si.subtotal),0)
FROM sale_items si JOIN sales s ON s.id = si.sale_id
JOIN products p ON p.id = si.product_id
WHERE p.pass_through AND s.status <> 'void' AND s.created_at >= $1 AND s.created_at < $2
```
from `pl.GrossRevenue`. (A reload line has no discount/tax, so `subtotal` is its face value.)
- [ ] **Step 4: COGS + returns.** Add `AND NOT p.pass_through` to the COGS query (`reports.go:114-117`) and the returns line query — pass-through cost is 0 so COGS is unaffected, but keep it explicit and correct for returns.
- [ ] **Step 5: Line-item reports.** Add `AND NOT p.pass_through` to the WHERE of `ProfitByCategory` (`:416-422`), `TopProducts` (`:321-325`), and the profit block that feeds `PL.TopProducts` (`:229-234`).
- [ ] **Step 6: SalesByCashier (header-based).** Its `SUM(s.subtotal/discount/total)` per cashier (`:268-273`) includes reloads. Subtract each cashier's pass-through line total by LEFT JOINing a per-sale pass-through subtotal, or subtract a per-cashier correction:
```sql
... COALESCE(SUM(s.total),0) - COALESCE(SUM(pt.amt),0) AS net
LEFT JOIN (SELECT si.sale_id, SUM(si.subtotal) amt FROM sale_items si
           JOIN products p ON p.id=si.product_id WHERE p.pass_through GROUP BY si.sale_id) pt
       ON pt.sale_id = s.id
```
(adjust `gross`/`net` the same way; `count` of sales stays as-is).
- [ ] **Step 7: Run — expect PASS.** Then `go test ./internal/features/reports/...` full.
- [ ] **Step 8:** `make build` + `go vet ./...`; commit.

---

### Task 3: `PLIncome` plugin hook (generic income contributor)

**Files:**
- Modify: `internal/plugin/hooks.go` (struct + `AddPLIncome` + `PLIncomes()` getter)

**Interfaces:**
- Produces:
```go
// PLIncome lets a plugin add an income line to the core P&L. Amount is summed
// for the [from,to) range; keep it cheap (one query). Label appears in the P&L.
type PLIncome struct {
    Label  string
    Amount func(ctx context.Context, from, to time.Time) (decimal.Decimal, error)
}
```
`func (r *Registry) AddPLIncome(s PLIncome)` + `func PLIncomes() []PLIncome` (package-level slice + getter, mirroring `reportCards`/`ReportCards()` at `hooks.go:131,159,178`).

- [ ] **Step 1:** Add the `plIncomes []PLIncome` package var, the struct (imports `context`, `time`, `github.com/shopspring/decimal`), `AddPLIncome`, and `PLIncomes()`. Follow the exact ReportCard pattern.
- [ ] **Step 2:** `make build` + `go vet ./internal/plugin/...`; commit.

---

### Task 4: Fold plugin income into the P&L (web layer)

**Files:**
- Modify: `templates/pages/admin/*finance*.templ` (FinanceData struct + P&L render) — locate via `grep -rn "FinanceData struct" templates`
- Modify: `internal/web/admin_more.go` (`financeData` ~:611) and `internal/web/admin_reports.go` (`FinanceReport` ~:255, incl. CSV)

**Interfaces:**
- Consumes: `plugin.PLIncomes()` (Task 3), `reports.PL` (Task 2).
- Produces: `FinanceData.PluginIncome []IncomeLine` + a displayed net that includes it.

- [ ] **Step 1:** Add to the finance page data struct: `PluginIncome []struct{ Label string; Amount decimal.Decimal }` (or a named type in adminpages), and a helper on it summing the amounts.
- [ ] **Step 2:** In `financeData`, after `Compute`, iterate `plugin.PLIncomes()`, call each `Amount(ctx, from, to)` (skip/log individual errors so one bad source can't blank the page), collect non-zero lines, set `PluginIncome`. (Confirm `admin_more.go` already imports `internal/plugin`; it does for other hooks — otherwise add it.)
- [ ] **Step 3:** In the P&L template, render each plugin-income line beside the existing **Other income** line, and change the bottom line to `Net profit = pl.NetProfit + Σ PluginIncome`. Keep `pl.NetProfit` intact as "core net"; show the combined figure as the headline. (`pl.OtherIncome` bank-interest line stays as-is.)
- [ ] **Step 4:** `FinanceReport` + its CSV: include the same plugin-income lines and adjusted net so the page and export agree.
- [ ] **Step 5:** `make css` (if classes changed) + `make build` + `go vet ./...`; commit. (No unit test — verified live in Task 6; a plugin with no sources leaves this a no-op.)

---

### Task 5: Recharge plugin wires itself in

All recharge logic stays here.

**Files:**
- Modify: `plugins/recharge/store.go` (mark carrier product pass-through; `RangeEarnings`)
- Create: `plugins/recharge/migrations/00012_carrier_products_passthrough.sql`
- Modify: `plugins/recharge/recharge.go` (register `PLIncome` in setup)
- Test: `plugins/recharge/earnings_test.go` (DB-guarded)

**Interfaces:**
- Consumes: `plugin.AddPLIncome` (Task 3), `products.pass_through` (Task 1).
- Produces: `Store.RangeEarnings(ctx, from, to) (decimal.Decimal, error)` = service charge + realized float commission.

- [ ] **Step 1: Mark carrier products pass-through.** In `CreateCarrier` (and wherever a carrier's service product is created), set `pass_through = true` on that product (add a tiny `UPDATE products SET pass_through=true WHERE id=$1` in the store, or via a products repo helper). One-time migration for existing carriers:
```sql
-- +goose Up
UPDATE products SET pass_through = true
WHERE id IN (SELECT product_id FROM recharge_carriers WHERE product_id IS NOT NULL);
-- +goose Down
UPDATE products SET pass_through = false
WHERE id IN (SELECT product_id FROM recharge_carriers WHERE product_id IS NOT NULL);
```
(Plugin migrations live under `plugins/recharge/migrations/`; next number after `00011`.)
- [ ] **Step 2: Write the failing earnings test.** In a rolled-back tx: insert a carrier + device, a `recharge_transactions` row with `service_charge = 5`, and a closed `recharge_device_sessions` row whose `closing − expected = 3`, all dated inside the range. Assert `RangeEarnings(from,to) = 8`. Cover: a session closed *outside* the range contributes 0; an *open* (uncounted) session contributes 0.
- [ ] **Step 3: Run — expect FAIL** (method undefined).
- [ ] **Step 4: Implement `RangeEarnings`.** Two summed parts:
  - **Service charge**: `SELECT COALESCE(SUM(service_charge),0) FROM recharge_transactions WHERE created_at >= $1 AND created_at < $2` **plus** the same over the bill table (see `BillLedger`/`admin.go:198-213` "service charge earned").
  - **Float commission (realized)**: sum of `(closing − expected)` over `recharge_device_sessions` closed in range. Reuse the recon baseline logic (`recon.go:51` `Reconciliation` / the `opening_expected` = last close + refills-since) so "expected" matches the per-session recon; only include rows with a non-null `closing` and `closed_at` in `[from,to)`.
  Keep it defensive: never error the whole P&L — return `(0, err)` and let the web layer skip.
- [ ] **Step 5: Register the hook** in the recharge plugin's `Setup`/`RegisterUI` (beside `AddReportCard`/`AddCashierMenuRoot` calls in `recharge.go`):
```go
reg.AddPLIncome(plugin.PLIncome{
    Label:  "Reload & Bills earnings",
    Amount: func(ctx context.Context, from, to time.Time) (decimal.Decimal, error) {
        return p.store.RangeEarnings(ctx, from, to)
    },
})
```
- [ ] **Step 6: Run — expect PASS**; `make build` + `go vet ./...` + `go test ./...`; commit.

---

### Task 6: Verify end-to-end on real data

- [ ] **Step 1:** `make build` + `go vet ./...` + `go test ./...` (with `DATABASE_URL`) all green.
- [ ] **Step 2:** Migration round-trips: `0058` and the recharge `00012` (`goose up`/`down`/`up`).
- [ ] **Step 3: Live before/after.** On the dev server: note the current Finance P&L (gross revenue, gross profit, net) with recharge activity present. Apply the build. Confirm: reload face value is **gone** from Gross Revenue, Gross Profit, Profit-by-Category, Top-Products, Sales-by-Cashier; a **"Reload & Bills earnings"** income line appears equal to service charge + closed-session float commission; NetProfit = core net + that line.
- [ ] **Step 4: Regression.** A normal (non-recharge) sale still shows full margin; a shop with the plugin disabled shows an unchanged P&L (no income line, no revenue change).
- [ ] **Step 5:** Restore the dev DB; confirm `tailwind.css` committed if touched.

---

## Self-Review

**Spec coverage:** Pass-through decision → Tasks 1,2,5(step1). Commission realized-on-close → Task 5 step 4. Plugin-driven / no core pollution → hook in Task 3, generic flag in Task 1/2, recharge-only wiring in Task 5; core files reference only `pass_through` + `plugin.PLIncomes()`. Report surface (P&L, profit-by-cat, top-products, sales-by-cashier) → Task 2. ✓

**Placeholder scan:** The two "locate via grep" notes (finance template path; carrier-product creation site) are explicit lookups with the anchor given, not vague TODOs. Everything else has concrete SQL/Go.

**Type consistency:** `PLIncome{Label, Amount}` defined in Task 3 matches the registration in Task 5 and the consumption in Task 4. `RangeEarnings(ctx, from, to) (decimal.Decimal, error)` matches its call in the hook. `Product.PassThrough`/`pass_through` consistent across Tasks 1,2,5.

**Open risk to watch during build:** whether `reports.Service` can be constructed on a `*sqlx.Tx` for Task 2's test; if not, test at the query level or seed+commit+cleanup like `internal/features/sales/oversell_db_test.go`.
