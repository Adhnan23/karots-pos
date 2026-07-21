# Supplier at the counter

**Date:** 2026-07-21
**Status:** approved, not yet implemented

## The problem

In a small shop the cashier is often the only person present when a supplier
walks in. Three things happen at that counter today, and the POS supports none
of them without an admin login:

1. The supplier asks for money.
2. The supplier arrives with goods and wants them taken in.
3. The supplier asks what to send next time.

The cashier route group has **no** supplier capability at all — every supplier
route lives under `/admin`. So either the owner is called away to log in, or the
event goes unrecorded until they catch up.

Paying is the case that is actually broken rather than merely missing: cash
leaves the drawer physically, and if it is not recorded against the open till
session the Z-report will not balance.

## A pre-existing defect this work must fix

The admin receive form has a **Paid Amount** input
(`templates/pages/admin/purchases.templ:320`). It marks the invoice paid and
clears the supplier's balance, but **no money moves**.

Verified live on 2026-07-21 against the dev database — received 10 units at
Rs 100 with `paid_amount = 1000`:

| Effect | Result |
| --- | --- |
| `purchases.status` | `paid` ✅ |
| `suppliers.outstanding_balance` | `0.00` ✅ |
| `supplier_payments` rows | **0** ❌ |
| `money_receipts` (CR- receipts) | **0** ❌ |
| `locker_ledger` rows | **0** ❌ |
| Cash out of any till or locker | **none** ❌ |

The `purchases` package imports neither `cashflow` nor `supplierpay`, so it has
no mechanism to move money. The consequence in the shop: the owner hands over
notes, the system considers the invoice settled, and the drawer still counts
that cash as present. The till closes short by exactly that amount with no
explanation, and the payment appears in neither supplier history nor Cash Flow.

Since "pay the supplier while receiving" is precisely the flow being put on the
counter, shipping it on top of this defect would multiply it. Fixing it is part
of this work, for admin and cashier alike.

## Design

### 1. Permission

New column `users.can_handle_suppliers boolean not null default false`. Existing
cashiers gain nothing until it is switched on. Admins and managers always pass.

The flag is read **per request from the database**, not from the JWT, so
revoking trust takes effect on the next click rather than at next login. This
costs nothing: `internal/web/web.go:123` already runs
`SELECT is_active FROM users WHERE id = $1` on every authenticated request via
`middleware.SetUserValidator`. The flag rides in that same query.

That requires widening the `UserValidator` hook to return flags alongside the
active check:

```go
type UserFlags struct{ CanHandleSuppliers bool }
type UserValidator func(ctx context.Context, userID int64) (UserFlags, bool)
```

`JWTAuth` stores the flags in the echo context; a new `middleware.CanHandleSuppliers(c)`
reads them, and `RequireSupplierAccess` gates the routes. The middleware package
keeps no feature dependency — `UserFlags` is a plain struct.

Admin surface: one checkbox on the user form in `templates/pages/admin/users.templ`,
plus `CanHandleSuppliers` on `auth.CreateUserInput` / `auth.UpdateUserInput`.

### 2. Where it lives

The terminal topbar already carries eight tabs, so the three actions do not each
get one. A single **Suppliers** tab, rendered only for users who pass the gate,
opens `/cashier/suppliers`: a searchable supplier list showing what is owed to
each. Selecting a supplier offers **Pay**, **Receive goods** and **Order**.

The tab's visibility reaches the layout the same way `ShowChangePin` already
does — a field on each cashier page's data struct (eight call sites).

No new feature package. This is a cashier-facing door onto `suppliers`,
`purchases`, `supplierpay` and `cashflow`.

### 3. Paying

Reuses the existing flow whole: open invoices listed with per-invoice
allocations, or a plain unallocated amount when the supplier merely carries a
balance.

Methods are **cash, card and online** — `normSupplierMethod` accepts no others,
and this design does not add any.

For cash the source defaults to the **till drawer** and requires an open
session; `cashflow` already returns *"that till has no open session"*, so no new
guard is needed. A cashier may instead pick a locker, but only one marked
usable by cashiers (below).

The admin pay handler (`admin_more.go:172`, roughly ninety lines of parsing,
`WithTx`, `PayTx`, `MoveTx`, audit and print) is extracted into one helper on
`*Server` that both admin and cashier call. They differ in two ways only:

- which cash sources are offered;
- which print URL the receipt prompt points at. `afterMoneyMove` hardcodes
  `/admin/money-receipts/...`, which a cashier cannot reach — the cashier path
  uses `/cashier/money-receipts/:id/print`, following the pattern already
  established by `CreditPay`.

### 4. Receiving goods

Full entry — product search picker, quantity, cost price from the supplier's
invoice — using the same margin guard as admin. Two entry points:

- **Against an order you already placed**, so partial receipt and the
  keep-remainder draft keep working.
- **A walk-in delivery** with no prior order.

The walk-in case revives `purchases.Create`, which is currently **dead code**
(nothing in the repository calls it). It already inserts a purchase in received
state, books stock and batches, and updates the supplier balance atomically —
strictly better than chaining draft-then-receive.

#### Paying while receiving

`PaidAmount` is **removed** from `purchases.CreateInput` and
`purchases.ReceiveInput`. Leaving it would leave the trap in place.

`purchases` grows `CreateTx` and `ReceiveTx` — the existing function bodies with
the transaction lifted to the caller, following the `WithTx` + `*Tx` compose
pattern used across the codebase. The web layer then runs in one transaction:

```
purchases.ReceiveTx / CreateTx   (goods, stock, batches, full payable)
        ↓
supplierpay.PayTx                (payment row + allocation to that invoice)
        ↓
cashflow.MoveTx                  (cash out of the chosen till or locker + CR- receipt)
```

`PayTx` already advances `paid_amount` and status and decrements the supplier
balance, so the stored numbers land exactly where they do today — but with a
payment record, a drawer that genuinely goes down, and a printable receipt.
Receiving alone (paying nothing) simply skips the last two steps.

`purchases` still imports neither `cashflow` nor `supplierpay`. It cannot:
`supplierpay` imports `purchases`. The composition therefore lives in the web
layer, where every other money flow is already composed.

The GRN slip prints as it does today.

The admin receive screen changes with it: its bare **Paid Amount** number box is
replaced by the same "paying now?" block used at the counter — amount, method,
and a cash source — and `grnReceive` in `static/js/app.js` posts those instead
of `paid_amount`. There is no version of receiving left that can mark money paid
without moving it.

A cashier who passes the gate **sees cost prices** on the receive and order
screens. That is inherent to entering a supplier invoice and was accepted
deliberately; it is the reason the permission is off by default.

### 5. Ordering

Items and quantities become a normal draft purchase order against that supplier,
stamped with the cashier's user id, and the PO slip prints for the supplier to
take away. The phone call to the owner is the approval, so there is no second
confirmation step; the draft appears in the owner's Purchases list like any
other.

### 6. Locker access

New column `lockers.cashier_access boolean not null default true`. Defaulting to
true means no existing locker changes behaviour.

A switch on the admin locker form. The cashier's locker choices — both the new
supplier payment source and the existing withdraw dialog
(`cashier.CashierLockers`) — filter on it. Admin's own picker is unchanged.

This is a small widening of scope beyond suppliers, accepted deliberately: the
owner described cashiers withdrawing to a locker when the till gets full and
sometimes paying from it, and the alternative was to have the cashier see every
locker including the owner's safe.

## Migration

One migration adding both columns. `Down` drops both; neither has a narrowing
constraint to reconcile, so no row conversion is needed.

## Testing

Assertions that would have caught the defect above:

- Receiving with a payment produces a `supplier_payments` row, a ledger entry
  against the chosen location, and a `money_receipts` row.
- Receiving with a payment that fails rolls the goods back too, so stock never
  lands without its payable.
- Receiving without a payment leaves the full amount owed and moves no money.

Plus:

- Gate table test over the four role/flag combinations against every new route.
- Locker filtering: a locker with `cashier_access = false` is absent from the
  cashier's choices and present in admin's.
- Live end-to-end at the counter: a flagged cashier receives a delivery and pays
  on the spot; stock up, payable settled, drawer down by exactly that amount,
  CR- receipt printed, Z-report balanced. Then a plain cashier sees no tab and
  is refused on a direct URL.

## Out of scope

- Per-cashier locker lists. One flag per locker is enough for a shop with a
  drawer and a safe.
- Bank as a supplier payment method.
- Supplier returns at the counter.
- Any change to how drafts are approved.
- Creating new products from the counter receive screen. The cashier receives
  against products that already exist; a genuinely new line is added through
  Stock Intake as it is today.
- Editing or deleting a supplier from the counter. The cashier picks from
  existing suppliers only.
