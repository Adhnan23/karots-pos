# QA — remaining work plan (post-fix follow-up)

Companion to `findings.md`. All **launch-blocker** findings (QA-001..QA-010) are fixed,
verified, and committed; **QA-004** is closed won't-fix (vendor on-site onboarding). This file
tracks what is **still ⚠️ / never-tested / deferred** so the next sessions can close the audit.

**Mode for these sessions:** test-first. Log every defect to `findings.md` immediately. Fix
**small/safe** defects inline (with a quick heads-up); **defer large** ones as new QA-NNN items.
After each area: update the `findings.md` status matrix + the `qa-audit-progress` memory.

**How to run:** core binary on `:3000` (`make templ && make css && go build`); psql via
`docker exec pos_db psql -U pos_user -d pos_db -c "…"`. Logins: system-admin `0000000001/2273`,
cashier `0771111111/1111`, admin `0770000001/…`. For plugin tiers, add the blank import to
`cmd/server/enabled_plugins.go`, `make migrate`, rebuild — then **restore it to core-only**.

**FIRST THING NEXT SESSION — finish the Playwright browser install (so the MCP works):**
The Playwright MCP config is healthy (`claude mcp list` → ✔ Connected) but it crashes mid-use
because `@playwright/mcp@latest` bundles **playwright-core 1.61.0-alpha**, which needs **chromium
v1226 (Chrome 149)** while only **v1223** is installed. Run (≈177 MB download, started but
cancelled last session):

```
npx -y playwright@1.61.0-alpha-1781023400000 install chromium chromium-headless-shell
```

Then verify `~/.cache/ms-playwright/` has `chromium-1226` + `chromium_headless_shell-1226`, and
do a quick MCP `browser_navigate http://localhost:3000/login` to confirm it drives a page.
Until then (or if it drops again), drive flows via authenticated `curl` (login → cookie jar →
hit endpoints) — that worked well last session.

---

## Tier A — finish the ⚠️ areas (money/stock correctness; highest value)

1. ~~**Cash register close / Z-report (over/short)**~~ — ✅ DONE 2026-06-30. Drove balanced
   (2600/2600/0), over (2450/1000/+1450), short (700/740/−40) closes via API; closing movement +
   audit row + Z-report over/short labeling all correct. Matrix cell → ✅.

2. ~~**Cashflow / lockers / CR- transfers**~~ — ✅ DONE 2026-06-30. Two lockers; fund/transfer/
   bank-charge/interest all via cashflow.Move; one CR- per move (transfer = 2 ledger legs, 1 CR);
   balances reconcile (Safe 6000 / Bank 4070); Net position + P&L (expense + Other Income) correct.

3. ~~**"Cash received" on P&L**~~ — ✅ DONE 2026-06-30 → **QA-011**. Confirmed the line counts
   only sale-time tender (excludes debt collections, which the Cashflow view does count). Owner
   chose to **relabel** "Cash received" → "Sale tender (paid at sale)" (HTML + CSV) and keep the
   P&L accrual-pure; Cashflow view stays the source of truth for true cash-in.

4. ~~**Onboarding ⚠️ re-mark**~~ — ✅ DONE 2026-06-30. Only residual is QA-004 (won't-fix) +
   QA-006 (fixed). Matrix cell → ✅ (noted: residual = QA-004 won't-fix). A full empty→shop
   re-walk is folded into the wrap-up restore round-trip.

5. ~~**Stock ⚠️ re-mark**~~ — ✅ DONE 2026-06-30. QA-009 fixed; re-confirmed: abs adjustment
   (Cola 61→100, +39 `adjust` movement w/ note), stock-take page, movements page, and
   low-stock/reorder report all render 200. Movement types present: purchase/sale/adjust/return/
   damage/warranty_replacement. Cell → ✅.

## Tier B — core areas never tested (breadth)

6. ~~**Units / conversions**~~ — ✅ DONE 2026-06-30. Unit CRUD clean; conversion sack→1kg (ratio
   25) run 2→50, value preserved (10000=10000, dest 200/kg), movements + run logged; overdraw
   guard 409. See findings Tier B section.

7. ~~**Suppliers CRUD + Supplier returns (debit notes)**~~ — ✅ DONE 2026-06-30. Supplier
   create+edit; supplier return 5×Cola@80 → 201, balance 600→200, stock 100→95, purchase_return
   movement −5, detail + dues report render. See findings Tier B.

8. ~~**Import / export (csv / xlsx / ods)**~~ — ✅ DONE 2026-06-30 → found+fixed **QA-012**
   (barcode-less products duplicated on re-import; added name fallback → idempotent). Products CSV
   round-trip (update+create+opening stock), xlsx/ods export valid + xlsx read path, bad-row
   skip+report, customer/supplier export headers. (Linux file-picker accept = UI-only, not driven.)

9. ~~**Product groups / Cashier Menu**~~ — ✅ DONE 2026-06-30. Drinks›{Cold,Hot} built; cashier
   drill-down API (top/children/breadcrumb/products+emoji), item-emoji update, reorder all work.

10. ~~**Unified receipts — 58/80mm reprint**~~ — ✅ DONE 2026-06-30. All 4 types view 200 on both
    roles; all 4 hub tabs render both roles; ?size=58/80 switch differs; reprint POST 200 {"ok"};
    ESC/POS covered by escpos+tspl unit tests. **Tier B complete.**

## Tier C — plugin deep flows (deferred; previously E2E-verified, re-run live)

11. ~~**+recharge live**~~ — ✅ DONE 2026-06-30 → found+fixed **QA-013** (admin float refill
    misattributed to refiller's session → now lands in active cashier till). Verified: deposit +svc
    + overdraw 409, bank-card billpay + overdraw 409, refill, reload, wallet tender, reconciliation,
    all admin pages, core regression clean. See findings Tier C.

12. ~~**+documents live**~~ — ✅ DONE 2026-06-30. Photocopy service (consumable=1 A4 sheet/copy);
    20-copy sale S-00010 → consume-on-sale fired (paper 500→480), line cost_price FEFO-rolled
    (2.00/unit), description persisted. Admin pages render.

13. ~~**Both plugins live**~~ — ✅ DONE 2026-06-30. Both migrate cleanly (own goose tables, core
    v42); both nav hooks + both ReportCard hooks coexist; zero receipt-number collisions; no
    double-count. **Tier C complete.** enabled_plugins.go restored to core-only.

## Tier D — cross-cutting hardening (`Cross-cutting` ⚠️)

14. ~~**Security sweep**~~ — ✅ DONE 2026-06-30. Cashier→admin routes all 403; admin-only sale
    APIs 403; `/cashier/z/:id` ownership enforced (403 cross-user). See findings Tier D.

15. ~~**i18n**~~ — ✅ DONE 2026-06-30 → **QA-014** (P3 known limit): HTML receipts render Sinhala
    fine; thermal sanitises non-ASCII to '?' by design (PC437 Latin-only). Graceful, documented.

16. ~~**Performance**~~ — ✅ DONE 2026-06-30. 2010 products → all endpoints sub-15ms (list 6.8ms,
    search 5.1ms, finance 4.5ms). Deep sales-load not driven; products surface fast.

17. ~~**QA-KNOWN-1 re-verify**~~ — ✅ DONE 2026-06-30. Confirmed by config: 12h access TTL = cookie
    MaxAge, no sliding refresh wired. ≤12h shift safe; >12h continuous still expires. Acceptable.

18. ~~**Windows printing re-confirm**~~ — ✅ DONE 2026-06-30. `GOOS=windows go build ./...` (incl.
    plugins) clean; core exe 32MB; printing has _unix + _windows variants. **Tier D complete.**

## Wrap-up gate (do last) — ✅ DONE 2026-06-30

- ✅ `go vet ./...`, `go test ./...`, `GOOS=windows go build ./...` — all green.
- ✅ **Plugin-data restore round-trip — VERIFIED LIVE under the plugin binary (2026-06-30).**
  recharge + documents enabled & populated → backup → `-reset` (drops schema, re-migrates core +
  both plugins empty) → restart → `POST /admin/restore`. Every table came back identical, core
  (sales/money/debt/products/lockers/users) AND plugin (recharge_carriers/devices/transactions,
  doc_service/consumable). Post-restore writes mint clean new numbers, **no collision** (sale →
  S-00012, money → CR-000012) — QA-010's sequence fix holds for the plugin build too. Backup
  captures plugin tables via dynamic `pg_tables` discovery; plugin seqs are column-owned.
  enabled_plugins.go restored to core-only after.
- ✅ Final `findings.md` pass: matrix all ✅ / noted; exec summary refreshed; audit marked COMPLETE.

**Audit complete.** Findings QA-001..014: all fixed/resolved except QA-004 (won't-fix) and QA-014
(documented known limit). 0 P0 / 0 P1. Ship-ready.

## Suggested order

Tier A (1→3) → Tier B (6→10) → Tier C (11→13) → Tier D (14→18) → wrap-up.
A and B are core sellability; C is per-deployment value; D is hardening/scale.
