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

6. **Units / conversions** — unit CRUD; product conversions (the consume-on-sale seam shared with
   plugins). Run a conversion, verify stock math + movement + cost.

7. **Suppliers CRUD + Supplier returns (debit notes)** — supplier create/edit/deactivate;
   `purchase-returns` new/detail flow → stock down + supplier balance/credit note correct.

8. **Import / export (csv / xlsx / ods)** — shipped this cycle, not yet QA-driven. Round-trip
   each of products / customers / suppliers / stock-take: download template → fill → import →
   export → re-import (idempotent). Test the broadened file-picker accept on Linux (GTK/XFCE),
   bad rows, and the row-limit cap.

9. **Product groups / Cashier Menu** — admin Cashier Menu build (groups + items + emoji); till
   drill-down, breadcrumb, Back.

10. **Unified receipts — 58/80mm reprint** — across S- / CR- / DP- / warranty on **both** cashier
    and admin: view (kiosk same-tab + Back), reprint, size switch. Verify ESC/POS bytes + HTML.

## Tier C — plugin deep flows (deferred; previously E2E-verified, re-run live)

11. **+recharge live** — per-device float: deposit / withdraw / billpay / topup; overdraw hard-
    block (409 + client guard); Wallet tender at checkout; reconciliation; admin float refill;
    bank cards (no-float); service charge; all slips. Then regress core.

12. **+documents live** — a real service sale (photocopy/print/laminate/bind/custom labour) with
    consume-on-sale + `sale_items.description` + FEFO cost-per-unit. Then regress core.

13. **Both plugins live** — run real recharge + documents txns together; assert no double-count in
    reports/cashflow and no receipt-number collisions across plugins.

## Tier D — cross-cutting hardening (`Cross-cutting` ⚠️)

14. **Security sweep** — IDOR via `:id` across roles (cashier hitting admin-scoped ids), authz on
    every route group, error pages. (QA-002 already closed the stale-token hole.)

15. **i18n** — Sinhala/Tamil product names + shop name render correctly in UI **and** on thermal
    receipts (PC437 codepage limits — confirm graceful handling).

16. **Performance** — seed 1000s of products + sales; check list pagination, search, and report
    query times.

17. **QA-KNOWN-1 re-verify** — long-running operator under the 12h TTL (no sliding refresh):
    confirm an active session can't expire mid-task; flag if it bites.

18. **Windows printing re-confirm** — `internal/printing` path (already checked once via cross-
    compile); re-confirm after this cycle's changes.

## Wrap-up gate (do last)

- Re-run `go vet ./...`, `go test ./...`, `GOOS=windows go build ./...` — all green.
- One more **restore round-trip with plugin data present** (recharge+documents tables populated)
  to confirm QA-010's fix covers plugin sequences too / no new collisions.
- Final pass on `findings.md`: matrix all ✅ or explicitly noted; refresh exec summary.

## Suggested order

Tier A (1→3) → Tier B (6→10) → Tier C (11→13) → Tier D (14→18) → wrap-up.
A and B are core sellability; C is per-deployment value; D is hardening/scale.
