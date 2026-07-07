# Next-session plan — Recharge "bill-pay via core bank locker"

**Owner-approved 2026-07-06.** Goal: fix the cramped/limited recharge bank-card subsystem by
deleting it and leaning entirely on core lockers + `cashflow.Move`. Plugin → core only; **no
core changes to accommodate the plugin** (boundary stays intact).

## The decision (locked with owner)
- **Retire the plugin's `bank_cards` table + its own balance.** No plugin-owned card entity.
- A "bank" **is a core `kind="bank"` locker** (`lockers.KindBank` already exists — no new core
  schema). Banks are created/managed in **Money → Cash Lockers**. The plugin only *reads &
  moves* them.
- Rename the concept **"bank card" → "bank"** in the recharge UI.
- **No data migration** — demo data; drop/retire `bank_cards`, start fresh on lockers.
- "Many banks" is free: just create several bank-kind lockers.

## Money model (all via `cashflow.Move`, so every leg gets a CR- receipt + shows in core
Cash Flow / net position)
- **Pay a bill:** `bank-locker → External(biller)` for the bill amount (bank down) **+**
  `External(customer) → Till` for bill + service charge (cash in). Net = shop keeps the
  service charge as profit; reconciles exactly.
- **Get money:** mirror — `External → bank-locker` (bank up) + `Till → External(customer)`
  (cash out).
- **Adjust a bank balance** (old "card withdrawal/deposit"): just a **core locker move**
  (Cash Flow / locker transfer) — already exists, already prints. Nothing plugin-specific.
- **Carrier float refill:** the *cash* paid to the supplier goes out via `cashflow.Move` from a
  **picked source location** (Shop Safe / My Bank / a till); the airtime float goes up in the
  plugin ledger. Fixes "refill asks amount but not where."

## Keep-in-plugin vs move-to-core
- **Stays plugin:** carrier float (airtime IOU from Dialog/Mobitel — *not* cash, never a
  locker), reconciliation, and optionally a thin **bill-pay log** (reference + which bank
  locker + service charge) for reporting — **balance-free** (money lives in core).
- **Moves to core:** everything about the bank's *balance* → it's a bank-kind locker.

## UI changes
- **Recharge admin collapses** to: **Carriers & Devices** (float + refills w/ source-location
  picker) and **Reconciliation**. The bank-card admin block is **removed** — banks live under
  Money → Cash Lockers.
- **Cashier "Reload & Bills":** clean **Pay bill / Get money** action — pick a bank
  (bank-kind locker via a picker of active `kind="bank"` lockers), amount, optional service
  charge → `cashflow.Move`.

## Guards / edges (plugin-side, not a core clash)
- Bill-pay picker lists only **active `kind="bank"`** lockers; if none exist, disable the
  action and prompt to add a bank in Cash Lockers.
- If a chosen bank locker is later deleted/deactivated, degrade gracefully (re-pick), same
  defensive pattern recharge already uses for tills.

## Why no core/plugin clash
Dependency points **plugin → core** only (recharge already imports lockers/cashflow/
cashregister). Core stays oblivious. This is the opposite of the previously-dropped "fold
recharge into core lockers" (which would have put a plugin concept into core).

## Build order (suggested)
1. Cashier Pay-bill / Get-money action on `cashflow.Move` + bank-locker picker (+ optional
   bill log). 2. Carrier-float refill source-location picker. 3. Remove bank-card admin block;
retire `bank_cards`. 4. Graceful "no bank / deleted bank" guards. 5. E2E vs the emulator.
