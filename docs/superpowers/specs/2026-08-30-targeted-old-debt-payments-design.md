# Targeted old-debt payments & editing (customers + suppliers)

**Date:** 2026-08-30
**Status:** Approved design, ready for implementation plan
**Scope:** admin side only — cashier flows unchanged

## Problem

The "old debt" (pre-system opening balance) and the transactional (linked) debt
are entangled in how they can be paid and edited:

1. **Editing old debt forces a full re-entry.** `AdjustOpening` takes an
   *absolute* new figure, so to change old debt you must recalculate and type the
   whole remaining amount every time.
2. **No way to pay the old debt specifically.** Every payment erodes the
   **transactional part first, old debt last** (fixed order, baked into
   `AddBalance`). You cannot deliberately pay down the old debt while leaving a
   current invoice, or vice-versa.
3. **Customer vs supplier asymmetry.** Customer payments are blanket (reduce the
   whole aggregate); supplier payments force per-invoice allocation. Neither
   exposes the old debt as its own payable target.

The owner wants, on the **admin** side, to treat old debt as a first-class thing
you can **Add to**, **Adjust** (correct), or **Pay down** — none of which should
require re-entering the whole figure (except a deliberate "set exact"
correction). The **cashier** side stays automatic: the cashier just enters an
amount and the system allocates it as it does today.

## Current state (verified 2026-08-30)

- Both `customers` and `suppliers` carry three columns:
  - `opening_balance` — gross original old debt, frozen (statement's opening line).
  - `opening_unlinked` — how much of the old debt is **still unpaid** (editable).
  - `outstanding_balance` — total currently owed (linked + unlinked).
  - `LinkedBalance()` (derived) = `outstanding_balance − opening_unlinked`.
- `AddBalance(delta)` (both repos): `outstanding += delta`, then
  `opening_unlinked = LEAST(opening_unlinked, GREATEST(outstanding+delta, 0))`
  — i.e. a payment settles linked first, erodes opening only as overflow.
- `AdjustOpening(newOpening)` (both repos): sets `opening_unlinked = newOpening`
  and shifts `outstanding_balance` and `opening_balance` by the delta
  `(newOpening − old_unlinked)`; linked untouched. Absolute only.
- **Suppliers:** `supplierpay.PayInput` = per-invoice `Allocations []Alloc` +
  `Unallocated` (an advance that sits on the balance). `PayTx` advances each
  purchase's `paid_amount`, records the payment/allocations, and drops the
  aggregate by the full total via `AddBalance`. `parseAllocations` (web) reads
  `alloc_<id>` + `advance`/`amount`.
- **Customers:** no per-invoice model. `RecordPaymentTx` reduces the aggregate
  via `AddBalance(-amt)`, writes a `DP-` receipt row (`balance_before/after`),
  and the web layer books the cash intake via `cashflow.MoveTx`.
- Admin handlers: `SupplierPay` (`admin_more.go:303`) → `parseAllocations` →
  `paySupplierTx` (`supplier_pay_shared.go`); `CustomerPay` (`admin_more.go:1866`)
  → `RecordPaymentTx` + `MoveTx`. Cashier: `cashier_suppliers.go`
  (`SupplierPayForm`/`SupplierPayAtCounter`) and `CreditPay` (`cashier.go`).
- Shared editing modal: `adminpages.OpeningAdjustForm` / `OpeningAdjustData`
  (`components.templ`), posts `opening` (absolute) to
  `/admin/{customers,suppliers}/:id/opening` → `{Customer,Supplier}OpeningAdjust`
  → `AdjustOpening`. Alpine state via `openingXData` shows a live "New total".

**No migration is required** — every operation reuses existing columns.

## Design

Three operations on old debt, all admin-only:

### 1. Pay old debt down (money in — receipt + cash move)

New shared repo method on **both** `customers` and `suppliers`:

```go
// PayOpening reduces the old (opening) debt directly: outstanding_balance and
// opening_unlinked each drop by amt (clamped >= 0), leaving the gross
// opening_balance and the linked part untouched. Contrast AddBalance, which
// erodes the transactional part first.
func (r *Repository) PayOpening(ctx context.Context, id int64, amt decimal.Decimal) error
```

SQL:
```sql
UPDATE {customers|suppliers} SET
    outstanding_balance = GREATEST(outstanding_balance - $1, 0),
    opening_unlinked    = GREATEST(opening_unlinked    - $1, 0)
WHERE id = $2
```

`opening_balance` is **not** touched — a payment leaves the historical figure and
is recorded as a payment (the statement nets it), exactly as linked payments
leave sale totals alone. An old-debt payment that **exceeds `opening_unlinked` is
rejected** with a clear message ("payment exceeds the old debt; use Current
credit for the rest") — never silently capped, so the recorded receipt always
equals the cash received.

**Suppliers (`SupplierPay` admin only):**
- Pay dialog gains one **"Old debt (before system)"** line above the invoice
  lines, rendered **only when `opening_unlinked > 0`**, showing the remaining old
  debt and an amount input `pay_opening`.
- `supplierpay.PayInput` gains `Opening decimal.Decimal`. `parseAllocations`
  reads `pay_opening` into it (validated non-negative, `<= opening_unlinked`).
- `supplierpay.PayTx`: `total = Unallocated + sum(Allocations) + Opening`.
  Apply invoice allocations as today; call `PayOpening(Opening)` for the opening
  portion; call `AddBalance(-(total − Opening))` for the rest. Net: outstanding
  drops by the full total, opening_unlinked drops by exactly `Opening`. Payment
  + cash move + `CR-` receipt (`paySupplierTx`) unchanged (records the full
  total).
- **Cashier `SupplierPayForm`/`SupplierPayAtCounter`: no old-debt line** — omits
  `pay_opening`, so `Opening` is zero and behaviour is identical to today.

**Customers (`CustomerPay` admin only):**
- Pay dialog gains a target choice: **Current credit** (default = today's
  behaviour) vs **Old debt** (rendered only when `opening_unlinked > 0`, showing
  the remaining amount).
- `customers.PaymentInput` gains `ApplyToOpening bool`. `RecordPaymentTx`: when
  set, use `PayOpening(amt)` (capped at `opening_unlinked`) instead of
  `AddBalance(-amt)`; everything else (the `DP-` receipt row with
  `balance_before/after`, the cash intake via `MoveTx`) is identical.
- **Cashier `CreditPay`: untouched** — `ApplyToOpening` stays false; pays the
  whole as today.

### 2. Add to old debt (no money — debt goes up)

The shared `OpeningAdjustForm` modal gets a small **"Add amount / Set exact"**
toggle (Alpine, client-side):

- **Add amount** (new default): user enters an amount; the form posts
  `opening = current opening_unlinked + amount`.
- **Set exact:** user enters the correct total; posts `opening = amount`
  (today's behaviour, for correcting a wrong figure).

The posted field stays `opening` (absolute), so **the server and `AdjustOpening`
are unchanged** — only the modal computes the value. The live "New total"
preview already present is retargeted to reflect the chosen mode.

### 3. Adjust old debt exact (no money — correction)

Unchanged — the "Set exact" branch of the toggle above, calling the existing
`AdjustOpening`.

## What is deliberately NOT built

- **No per-credit-sale targeting for customers** (owner chose two buckets: Old
  vs Current). Customers have no per-sale ledger; adding one is out of scope.
- **No migration / new columns.** The payment's bucket is not separately
  recorded — `opening_unlinked` already reflects reality live, and statements net
  payments against the gross opening. A per-bucket breakdown report can be added
  later if needed.
- **No cashier changes.** Targeting is admin-only by explicit decision.

## Testing

Follow the existing `internal/features/suppliers/opening_test.go` DB-backed
pattern:

- `PayOpening` clamps correctly in **both** packages: reduces outstanding +
  opening_unlinked by amt; never below zero; leaves linked and gross
  opening_balance untouched.
- `supplierpay.PayTx` splitting a payment across an invoice **and** old debt:
  invoice `paid_amount` advances, opening_unlinked drops by the opening portion,
  outstanding drops by the full total.
- `customers.RecordPaymentTx` with `ApplyToOpening=true`: opening_unlinked drops,
  linked untouched, `DP-` receipt still written; with the flag false, behaviour
  is unchanged (regression guard).
- Overpay guard: paying more than `opening_unlinked` against old debt is
  **rejected** with a validation error — assert the rejection.

## Files touched

- `internal/features/customers/customers.go` — `PayOpening` repo method;
  `PaymentInput.ApplyToOpening`; `RecordPaymentTx` branch.
- `internal/features/suppliers/suppliers.go` — `PayOpening` repo method.
- `internal/features/supplierpay/supplierpay.go` — `PayInput.Opening`; `PayTx`
  split + `validatePay` total.
- `internal/web/supplier_pay_shared.go` — `parseAllocations` reads `pay_opening`.
- `internal/web/admin_more.go` — `CustomerPay` reads the target; pass through.
  (`CustomerPayForm`/`SupplierPayForm` pass `opening_unlinked` to the templates.)
- `templates/pages/admin/*` — supplier pay form old-debt line; customer pay form
  Current/Old target; `OpeningAdjustForm` Add/Set toggle.
- Tests in `customers`, `suppliers`, `supplierpay` packages.
- Cashier templates/handlers: **no change** (regression-verify only).
```
