# Cash-flow, lockers & a unified money model — design

**Date:** 2026-06-24
**Status:** Approved design (not yet implemented). Implementation is incremental over future
sessions; **each money operation must be tested end-to-end before the next is built.**

## 1. Problem

Today only physical drawer cash is tracked, and only inside an *open till session*
(`cash_movements`, types `opening|sale|credit_payment|withdrawal|pay_in|refund|closing`). Every
other money flow is recorded in its own ledger with **no link to a physical pile of cash**:

- **Expenses** (`internal/features/expenses`) — writes an expense row, touches no drawer.
- **Supplier payments** (`internal/features/supplierpay`) — the package comment even states the
  "cash-drawer impact is wired by the web layer", but only the **cashier** path wires it; the
  **admin** path does not.
- **Customer credit collection** (`customers.RecordPayment`) — adjusts the customer balance + ledger,
  no drawer when done from admin.
- There is **no locker/vault** — cash that leaves a till (to a safe, the bank, the owner's pocket)
  disappears from the books.
- The ad-hoc **"from a till"** pattern added for the recharge bank-cards and documents labour is
  per-plugin, not a shared core concept.

Result: the books say money came in / went out, but nothing says *which* cash it touched. Cash flow
is unaccounted for.

## 2. Goals / non-goals

**Goals**
- A first-class **cash location** model: every cash event has two endpoints.
- A core **locker** entity (safe, bank, owner-pocket, "owner's brother", a 2nd bank, …) — freely
  user-creatable, each with its own balance + ledger.
- A single core **`cashflow.Move`** helper that writes both sides of any cash event atomically.
- Wire a reusable **location picker** into every cash touchpoint (till open/close, mid-session
  withdraw/pay-in, expenses, supplier payments, customer credit collection).
- **Bank charges & interest** adjustments on lockers.
- A **combined cash-flow view** (routes: customer → safe → supplier) and a **net-position** summary
  (cash + stock − payables + receivables).
- **Finance and reports must track everything** — no money event is invisible to reporting.

**Non-goals (explicitly deferred)**
- **Not** absorbing the recharge plugin's bank-cards into core lockers yet (keep their own
  balance/ledger; fold in later once the core model is proven).
- **Not** building a general "other income" subsystem. Interest is surfaced into reports as
  other-income read directly from the locker ledger; a proper income ledger is a future option.

## 3. Concepts & terminology

A cash movement always has **two endpoints**, each one of:

- **Till** — an open `cash_register` session's drawer (existing `cash_movements`). Transient,
  per-shift.
- **Locker** — a new persistent named account with a tracked balance + ledger.
- **External** — the **trading counterparty**, and *only* the far side of an intake or an outflow:
  - **Out → external:** expenses, supplier payments (money leaves a tracked location to the outside).
  - **In ← external:** customer payments / credit collection (money arrives from the outside).
  - Also: capital injection (external → locker), bank charge (locker → external), interest
    (external → locker).

External is **never** a pickable "one of your own piles". The pickable endpoints are only **tills +
lockers**. Consequences:

- **Outflow** (expense, supplier pay): pick the **source location** (till/locker); external is the
  implicit destination. *Owner paid personally?* → he picks his **owner's-pocket locker** (which is
  why that locker allows a negative balance). Nothing leaks untracked.
- **Intake** (customer collect): pick the **destination location**; external (the customer) is the
  implicit source.
- **Transfer** (close→safe, open←safe, withdraw/deposit between piles): both endpoints are your
  tills/lockers.
- **Capital in** (first money into a locker): external → locker — the locker's opening balance.

The system is therefore **closed**: every cash event has at least one tracked endpoint, and the only
things crossing the boundary are real customer / supplier / expense / bank transactions.

## 4. Data model (core migration)

```
lockers
  id            BIGSERIAL PK
  name          TEXT NOT NULL
  kind          TEXT NOT NULL  -- 'safe' | 'bank' | 'pocket' | 'other'
  allow_negative BOOL NOT NULL DEFAULT false  -- owner-pocket/brother = true; safe/bank/till = false
  is_active     BOOL NOT NULL DEFAULT true
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()

locker_ledger          -- balance of a locker = SUM(balance_delta)
  id            BIGSERIAL PK
  locker_id     BIGINT NOT NULL REFERENCES lockers(id)
  balance_delta NUMERIC(14,2) NOT NULL        -- +in / -out
  kind          TEXT NOT NULL  -- 'open_balance'|'transfer'|'payment'|'intake'|'bank_charge'|'interest'|'adjust'
  counterparty  TEXT NOT NULL  -- 'till' | 'locker' | 'external'
  counter_till_session BIGINT  -- the other endpoint when it's a till (cash_register.id)
  counter_locker_id    BIGINT  -- the other endpoint when it's another locker
  ref_kind      TEXT           -- 'expense'|'supplier_payment'|'customer_payment'|'cash_movement'|'locker_ledger'|null
  ref_id        BIGINT         -- soft link to the row that caused this side
  note          TEXT NOT NULL DEFAULT ''
  created_by    BIGINT
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
  -- indexes: (locker_id), (created_at), (ref_kind, ref_id)
```

- **Locker balance** = `SUM(balance_delta)` over its rows (same pattern as recharge device/card
  balances). No per-session opening/closing for lockers — a locker is a persistent running balance.
- A **till↔locker** move = one `cash_movements` row + one linked `locker_ledger` row.
- A **locker↔locker** move = two linked `locker_ledger` rows.
- A **locker↔external** event (open_balance / bank_charge / interest / admin payment paid straight
  from a locker) = one `locker_ledger` row.
- The till engine and everything reading `cash_movements` are **untouched** by the schema.

## 5. `cashflow.Move` helper + drawer-engine refactor

A small core service is the single way money moves:

```
Move(from Location, to Location, amount, reason, ref) error
  Location = Till(userID) | Locker(id) | External
```

It writes whichever sides are tracked. **Chosen approach: one true atomic transaction** (rejected the
"source-first ordering" band-aid — a cash-flow ledger must never drift).

**Drawer-engine refactor required (additive):**
- `cashregister`: expose tx-usable repo helpers — `OpenSession(ctx, userID)` (lookup `FOR UPDATE`),
  an overdraw / expected-cash guard, and reuse the existing repo-level `AddMovement` — all callable
  over an injected `*sqlx.Tx`.
- Keep `Service.Withdraw` / `Service.PayIn` as thin back-compat wrappers (existing callers unchanged);
  their core moves into the tx-usable repo path that `cashflow` also calls.
- `cashflow.Move` runs one `db.WithTx`, constructing `cashregister.NewRepository(tx)` +
  `locker.NewRepository(tx)` over that single tx: guard the source, write both sides, commit once —
  both sides commit or neither does.

**Overdraw guard:** runs on the *source* before writing. A Till, or a locker with
`allow_negative=false`, is blocked (HTTP 409, same pattern as the recharge/documents from-till guard)
if it would drop below zero. A locker with `allow_negative=true` (owner's pocket / brother) may go
negative.

This also promotes the per-plugin "from a till" into a real core concept; recharge & documents can
later call `cashflow.Move` instead of their bespoke logic (out of scope here, noted for follow-up).

## 6. Wired flows + reusable location picker

One Templ **location picker** component (generalizing the recharge tap-button "External / From a
till" picker): lists **the valid tracked endpoints** (open tills + active lockers) and returns a
structured endpoint. Used everywhere money moves:

| Flow | Today | After |
|------|-------|-------|
| **Till open** | opening float, no source | float pulled **from** a location (locker −) or capital (external →) |
| **Till close** | counts cash, drawer ends | counted cash deposited **to** a chosen location (locker +) |
| **Mid-session withdraw / pay-in** | drawer only | names the **counterparty** location |
| **Expense pay** (admin) | expense row only | **source** = till or locker (external = the expense itself) |
| **Supplier pay** (admin) | payment row only | **source** = till or locker |
| **Customer credit collect** | balance + ledger only | **destination** = till or locker |
| **Locker↔locker transfer** | n/a | both endpoints picked |
| **Capital injection** | n/a | external → locker (`open_balance`) |
| **Bank charge / interest** | n/a | locker ↔ external (§7) |

Every one routes through `cashflow.Move`, so the domain row (expense / supplier-payment /
customer-payment) and the cash side are written together in one tx and cross-link via `ref`.

**Out of scope to re-wire now but on the checklist to verify:** refunds and purchase returns already
touch the drawer — confirm they keep working and, where sensible, gain the location dimension.

## 7. Bank charges, interest & opening balances

Locker ↔ external events, each a `locker_ledger` row with a note:

- **Opening balance / capital injection** — external → locker (`kind='open_balance'`).
- **Bank charge** — locker → external (`kind='bank_charge'`); **also books a core Expense**
  (category "Bank charges"), linked via `ref`, so the P&L stays honest.
- **Interest earned** — external → locker (`kind='interest'`); surfaced into **reports as
  other-income** read from the locker ledger (no separate income subsystem now).
- **Manual adjust** — `kind='adjust'`, mandatory note, no accounting side (true-ups/corrections).

All admin-only, performed on the locker's page.

## 8. Combined cash-flow view & finance/report integration

A core **Cash Flow** page in the Finance hub. For any date range:

1. **Live balances** of every location — each open till + every locker — owner's-pocket free to be
   negative, the rest guarded ≥ 0.
2. **Money IN by route:** customer payments, capital injections, interest.
3. **Money OUT by route:** suppliers, expenses, bank charges.
4. **Transfers** between your own locations (net-zero to the business, shown for traceability).
5. **Net change per location** over the period.
6. A **combined ledger** — `locker_ledger` + relevant `cash_movements` unified into one time-ordered,
   filterable stream (by location, by route) — so you can trace *customer cash → safe → supplier*.
7. A **routes visualization** (sources → uses) using the existing SVG chart kit
   (`templates/shared/charts`).

**Reports/finance integration so nothing is missed (the hard requirement):**
- Bank charges land in **Expenses** → P&L correct.
- **Interest** is read from `locker_ledger` into reports as **other income**, so the P&L and the
  cash-flow view reconcile.
- Existing reports (sales method, expenses, supplier payments) gain the **location** dimension where
  relevant.
- Surface via the existing `plugin.ReportCard` / Finance-hub split from the reports overhaul.

## 9. Net-position summary

A summary block (Finance hub / Cash Flow page):

- **Total cash on hand** = Σ(all open till drawers) + Σ(all locker balances, incl. negative
  owner-pocket).
- **Total assets** = cash + **stock valuation** (existing) + **customer receivables** (credit owed
  *to* the shop).
- **Net worth estimate** = assets − **supplier payables** (aggregate owed). This is the "after
  removing those payments" figure.

## 10. Permissions

- **Cashier** — at their own till: open (pick source), close (pick destination), mid-session
  withdraw/pay-in (pick counterparty). Picks lockers by name; does **not** see locker balances.
- **Admin** — create/manage lockers (name, kind, `allow_negative`, deactivate), view all balances +
  ledgers + the combined view + net position, locker↔locker transfers, bank charge/interest/opening
  adjustments, and all admin payment flows (expense / supplier / credit) with the source/destination
  picker.

## 11. Plugin ripple

- **Additive core only** — `cash_movements` schema + the till engine's existing callers
  (`sales`, `reports`, recon, recharge, documents) keep working unchanged.
- The new `cashflow.Move` is exposed on `plugin.Core` so plugins can adopt it later. **Follow-up
  (not now):** migrate recharge bank-card from-till and documents labour from-till to call
  `cashflow.Move`; eventually fold recharge bank-cards into core lockers.
- Reports gain a cash-flow `ReportCard`; Finance hub gains Cash Flow + Net Position cards.
- Core DB version bumps (one migration adding `lockers` + `locker_ledger`).

## 12. Rollout strategy (per user)

Build **incrementally**; **each money operation is tested end-to-end before the next is started.**
Suggested order (each its own slice, E2E-verified):

1. `lockers` + `locker_ledger` schema; admin locker CRUD; opening balance; balances panel.
2. `cashflow.Move` helper + drawer-engine tx refactor; locker↔locker transfer (simplest two-locker
   case) E2E.
3. Location picker component; wire **expense pay** source (till/locker) E2E.
4. Wire **supplier pay** source E2E.
5. Wire **customer credit collect** destination E2E.
6. Wire **till open** (source) and **till close** (destination) + mid-session withdraw/pay-in
   counterparty E2E.
7. Bank charge (+ expense) / interest / manual adjust E2E.
8. Combined cash-flow view + routes viz; reports integration (interest as other-income) E2E.
9. Net-position summary E2E.

## 13. Open questions / future

- Fold recharge bank-cards into core lockers (separate spec).
- A proper "other income" ledger if interest/other income should be first-class on the P&L beyond the
  read-through.
- Whether refunds / purchase returns should require a location (currently keep as-is, verify).
