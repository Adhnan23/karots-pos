# Plugin returns / reversals + own-use (recharge & documents)

Date: 2026-08-09
Status: design approved (sections A–C + core-impact), pending spec review

## The problem (owner, real)

Core can return normal products, but **nothing plugin-related can be reversed**, and
there is no way to record plugin services consumed for the shop's own use:

1. **Recharge / reload.** A reload is cash-in + float-out. Two failure modes happen at
   the counter: (a) the reload **fails / doesn't go through** — the money must be given
   back and the float returns; (b) the cashier sends airtime to the **wrong number** —
   the float is really gone but the customer must still be refunded (the shop eats it).
   Today core `sales.Return` **explicitly refuses** any sale with a service line
   (`internal/features/sales/service.go:703` — "this sale includes a recharge item,
   which can't be returned"), so even the cash can't be refunded through the normal path,
   and there is no float-reversal transaction type at all.
2. **Documents (photocopy / printing).** A mistaken job can't be returned — the paper is
   already consumed and can't be reused, so it must be written off as a loss while the
   customer is refunded. Same core block applies (photocopy is a service line).
3. **Own-use.** The shop does its own photocopy/printing (adverts, forms). Normal
   products already have a "shop use" path (`stock.Consume` + `own_use`), but there is no
   equivalent for a plugin service, so the paper it eats is invisible.

## Hard constraint: do not modify or pollute core

Core **business logic** (sales, returns, stock, P&L) is not touched. This design uses:

- **Existing hooks** — no new hook types are needed. The `ReceiptTab` hook already gives
  each plugin a panel on the Receipts page (cashier + admin); that is where reversals
  live.
- **Existing exposed Core services** — `core.CashRegister` (drawer moves, already used by
  recharge), `core.Expenses` (P&L lines), `core.Audit`, and `core.Stock` for own-use.
- **Exactly one additive change to the frozen plugin API surface:**
  `internal/plugin/core.go` gains `Stock *stock.Service`, wired in the web-layer DI where
  the `plugin.Core` value is built. This *exposes* an existing service (the same way
  `CashRegister`, `Sales`, `Expenses`, `Products` were added as features grew) — it
  changes no core logic. The web layer is allowed to change; it owns wiring.
- **Plugin-owned migrations** for the reversal/own-use bookkeeping columns.

## Decisions locked with the owner

- Reload reversal has **two buttons**: *Failed / didn't go* vs *Wrong number / delivered*.
- Reversals are a **cashier** action, from the **Receipts** page. Same posture as normal
  product returns (cashiers already do those). Audited.
- Documents: a **mistaken job** = refund + the paper is a **loss** (shown as a loss in
  P&L / the Print & Copy report; the already-consumed sheets are *not* re-tagged into the
  damage stock bucket, which would double-decrement). **Own-use** = a **separate action**
  (no customer, no cash) that consumes paper as `own_use` on the same P&L line products
  use.
- Own-use is documents-only. Reloading airtime "for the shop" is not a real case.

## The accounting model (the crux — why each treatment differs)

The original sale is **never mutated** (core forbids it). Each reversal posts *compensating*
moves. Whether the compensating cash-out is a plain drawer **Withdraw** (not a loss) or is
paired with an **Expense** (a P&L loss) is chosen so the books come out right — no new
loss-tracking machinery.

Two facts pin this down:

- **Reloads are `pass_through`** — face value is excluded from core revenue/COGS. Reload
  profit is the **realized float commission** (`recharge` `RangeEarnings` =
  Σ service_charge + Σ (`closing − opening − net float_delta`) per closed device session),
  contributed to core P&L via the existing `PLIncome` hook.
- **Buying float already expenses its cost.** `refill` books a real core expense
  (`plugins/recharge/cashier_refill.go:145` `Expenses.CreateInTx`) plus `cashflow.MoveTx`
  for the cash, and credits float. So the cost of any float is in P&L the moment it is
  bought.
- **`Expenses.Create` is P&L-only** — it records an expense row and moves **no** drawer
  cash (`internal/features/expenses/expenses.go:73`). Cash is moved separately via
  `CashRegister`/`cashflow`.
- **Documents services are normal-priced** (not pass_through): revenue = price, COGS =
  paper consumed via the core consume-on-sale seam.

### Reload — Failed (didn't go through)

Float is still on the device (carrier never debited it). Refund and restore:

- `CashRegister.Withdraw(face)` — cash back to customer (drawer −face; net 0 over
  sale+refund).
- Reversal tx `{type: "reversal", float_delta: +face, reversal_of: <reload tx id>}` —
  float restored, device balance immediately correct, the float is resellable.
- **No expense.** Net loss: zero. The refill cost will be recovered by a future real sale.

### Reload — Wrong number (delivered, float gone)

Float is truly gone and will never be counted back. Refund the customer; the loss is
**already** in P&L as the un-recovered refill expense — booking another expense would
**double-count**.

- `CashRegister.Withdraw(face)` — cash back to customer (drawer net 0).
- Reversal tx `{type: "reversal", float_delta: 0, reversal_of: <reload tx id>,
  note: "wrong number — float lost"}` — the ledger keeps the original −face reload, so the
  device's live balance stays reduced (correct — the float is physically gone).
- **No new expense.** The float's cost is the refill expense that will never be recovered;
  that *is* the loss, already on the books. Worked example (float bought 100 face for 98):
  refill −98 (already booked); wrong reload: cash +100 in then −100 refunded = 0; float
  −100 gone. P&L ends at −98 = exactly what the shop paid for the lost float. Correct, and
  not over-stated to the 100 face value.

Visibility: the reversal tx row (with its note) is surfaced in the recharge report /
reconciliation as reversed reloads (count + total, split failed vs wrong-number), so the
owner sees the losses even though the P&L number rides on the refill expense.

### Documents — mistaken job

Paper was consumed as real COGS at sale time; revenue is real. To undo without touching
the sale, refund the cash **and** contra the revenue so the paper COGS is left as the loss:

- `CashRegister.Withdraw(price)` — cash back to customer (drawer −price; net 0).
- `Expenses.Create(price, category "Print & Copy — mistaken job")` — P&L contra of the
  sale revenue. Result: P&L = +price (revenue) − paper COGS − price (expense) = **−paper
  COGS** = the wasted paper. Traceable: the owner sees the sale and the matching
  mistaken-job expense net to zero, with the paper cost remaining as the loss.
- `doc_job.reversed_at` is stamped to block double-reversal and mark the row.
- **Stock is not touched** — the sheets are already gone; re-tagging them as damage would
  double-decrement.

### Documents — own-use (shop's own work)

No customer, no cash. Consume the paper exactly as a normal product's shop-use:

- For each consumable of the chosen service/size (`doc_consumable` → product ×
  `qty_per_unit` × qty), call `core.Stock.Consume({ProductID, Quantity, Reason:"own_use"})`
  — the same path products use, landing on the same **own-use P&L line**.
- Record a `doc_job` with `kind = "own_use"`, price 0, so the Print & Copy report shows
  shop-use paper separately from sales.

## Part A — recharge reload reversal (plugin-only)

**Migration** (`plugins/recharge/migrations`, own goose suffix):
```sql
-- +goose Up
ALTER TABLE recharge_transactions ADD COLUMN reversal_of BIGINT NULL
  REFERENCES recharge_transactions(id);
ALTER TABLE recharge_transactions ADD COLUMN reversed_at TIMESTAMPTZ NULL;
CREATE INDEX idx_recharge_tx_reversal_of ON recharge_transactions(reversal_of)
  WHERE reversal_of IS NOT NULL;
-- +goose Down
DROP INDEX idx_recharge_tx_reversal_of;
ALTER TABLE recharge_transactions DROP COLUMN reversed_at;
ALTER TABLE recharge_transactions DROP COLUMN reversal_of;
```
A `"reversal"` entry is added to the `txKinds`/`Deltas` map with `cashSign 0` (cash handled
by the explicit `Withdraw`) and `floatSign` driven by the chosen mode (+1 for failed, 0 for
wrong-number — passed as an already-signed delta rather than the map, or a second kind
`"reversal_lost"` with floatSign 0; implementer picks the cleaner of the two).

**Routes** (recharge cashier UI):
- `GET  /cashier/recharge/reload/:id/reverse` — modal fragment: shows the reload's
  device/amount and the two radios (Failed / Wrong number).
- `POST /cashier/recharge/reload/:id/reverse` — performs it (see flow).

**Reverse flow** (one DB tx, requires an open drawer):
1. Load the reload tx; guard: `type = "reload"`, `reversed_at IS NULL`, no existing row
   with `reversal_of = id`.
2. `CashRegister.Withdraw(face, reason "Reload reversal #…")` (drawer-guarded).
3. Insert reversal tx: `reversal_of = id`, `float_delta = +face` (failed) or `0`
   (wrong-number), note carrying the mode.
4. Stamp `reversed_at = now()` on the original.
5. `core.Audit` the reversal.
6. Print policy: reuse the existing recharge slip/print-policy path (CR-/RL- style).

**UI:** the recharge float `ReceiptTab` fragment (`ReceiptsFloat`) gains a **Reverse**
button on each `type = "reload"` row where `reversed_at IS NULL`; reversed rows show a
"reversed" chip. hx-get the modal into `#modal-container`.

## Part B — documents mistaken-job reversal (plugin-only)

**Migration** (`plugins/documents/migrations`, own goose suffix):
```sql
-- +goose Up
ALTER TABLE doc_job ADD COLUMN kind TEXT NOT NULL DEFAULT 'sale';   -- sale | own_use
ALTER TABLE doc_job ADD COLUMN reversed_at TIMESTAMPTZ NULL;
-- +goose Down
ALTER TABLE doc_job DROP COLUMN reversed_at;
ALTER TABLE doc_job DROP COLUMN kind;
```

**New surface:** documents registers a `ReceiptTab` ("Print & Copy") — a panel listing
recent `doc_job` rows (cashier + admin) with a **Reverse** button per un-reversed sale job.
This is new (documents has no receipts surface today) and uses the existing hook.

**Routes** (documents cashier UI):
- `GET  /cashier/documents/receipts` — the ReceiptTab fragment (job list).
- `GET  /cashier/documents/job/:id/reverse` — confirm fragment.
- `POST /cashier/documents/job/:id/reverse` — performs it.
- (admin variant `AdminHref` for the ReceiptTab, read-only or same reverse gated to
  manage role — implementer keeps cashier reverse as the primary path per the decision.)

**Reverse flow** (one DB tx, requires an open drawer):
1. Load the job; guard `kind = 'sale'`, `reversed_at IS NULL`.
2. `CashRegister.Withdraw(job.line_total, reason "Print & Copy reversal #…")`.
3. `Expenses.Create({Category: "Print & Copy — mistaken job", Amount: line_total,
   Description: job description})` — the revenue contra.
4. Stamp `reversed_at`.
5. `core.Audit`.

## Part C — documents own-use (needs the one Core line)

**Core:** `internal/plugin/core.go` gains `Stock *stock.Service`; web DI sets it from the
already-built stock service.

**UI/route:** the Print & Copy cashier flow gains a **Shop use** action (a node in the
existing `/cashier/documents/menu`, or a mode toggle on the job form) that reuses the
existing service/size/qty picker but records shop-use instead of adding to the cart:
- `POST /cashier/documents/ownuse`.

**Flow** (one DB tx):
1. Resolve consumables for the service/size (`ConsumablesFor`).
2. For each, `core.Stock.Consume({ProductID, Quantity: qty_per_unit×qty,
   Reason: "own_use", Note: "Print & Copy shop use"})`.
3. Insert `doc_job` with `kind = "own_use"`, `unit_price = 0`, `line_total = 0`.
4. `core.Audit`.
No `CashRegister`, no customer, no receipt.

The Print & Copy admin report is updated to break out `kind = 'own_use'` (paper used for
shop work) and reversed jobs, so the two new row kinds don't pollute the sales figures.

## Core impact (the whole point)

- **One additive field:** `Stock *stock.Service` on `internal/plugin/core.go` + its DI
  wiring in `internal/web`. No new hook types. No change to sales/returns/stock/P&L logic.
- Everything else: two plugin migrations, plugin routes, plugin fragments, existing
  `ReceiptTab` hook, existing `core.CashRegister` / `core.Expenses` / `core.Stock` /
  `core.Audit`.

## Testing

1. `make build` (regenerates templ) + `go vet ./...` + `go test ./...`.
2. `goose up`/`down`/`up` on the dev DB for both plugin migrations — clean round-trip.
3. **Recharge (DB-guarded):**
   - Failed reversal: drawer nets 0, device float restored to pre-reload, original marked
     reversed, double-reversal blocked, `RangeEarnings` unchanged.
   - Wrong-number reversal: drawer nets 0, device float stays reduced, no new expense
     created, reversal row present with mode note.
4. **Documents (DB-guarded):**
   - Mistaken-job reversal: drawer nets 0, an expense of `line_total` under the mistaken
     category exists, stock unchanged (paper stays consumed), job marked reversed, P&L for
     the job nets to −(paper cost).
   - Own-use: no sale/cash, each consumable decremented via `own_use` movement (cost booked
     on the own-use line), `doc_job kind='own_use'` recorded.
5. **Live E2E** on the dev server for each of the four flows, plus a regression check that
   normal reloads / photocopy sales and existing product returns are unchanged.
6. Restore the dev DB to baseline afterwards.

## Out of scope

- Own-use for recharge.
- Editing the original core sale row (deliberately untouched; core forbids service returns).
- Partial reload reversal (a reload is atomic — reverse the whole thing).
- Re-tagging already-consumed documents paper into the damage *stock* bucket (double-count).
