# Credit becomes a payment type, not a sale type

**Date:** 2026-07-21
**Status:** approved, not yet implemented

## Problem

Two unrelated things in the till are both called "credit", and they can disagree
with each other.

**`sales.sale_type`** is chosen by the cashier from three buttons — Retail,
Wholesale, Credit — but it only ever affects **pricing**, and only for
Wholesale (`internal/features/sales/service.go:232`). Tapping **Credit**
changes no behaviour whatsoever. The value is stored, printed on receipts and
shown in the Sales report, where it reads as a statement of fact that is not
one.

**`sales.status`** becomes `'credit'` when the payment lines do not cover the
total (`service.go:358-379`). That is the real mechanism: it requires a
customer, checks the credit limit and adds to the customer's balance.

Because the two are independent, both of these are possible today:

| What the cashier did | `sale_type` | `status` | Reality |
|---|---|---|---|
| Tapped Credit, took full cash | `credit` | `completed` | Nothing owed, but it reads as a credit sale |
| Left it on Retail, customer paid short | `retail` | `credit` | Money is owed, but it reads as a retail sale |

The owner reports the second case happening by accident. Selecting a customer
is **not** the cause — that was checked; picking a customer touches neither
`saleType` nor the status. The cause is that short payment silently converts a
sale to credit with no deliberate act.

## Decisions

| Question | Decision |
|---|---|
| What is `sale_type`? | **A price list only: `retail` or `wholesale`.** The Credit button is removed. |
| How is credit taken? | **A payment line with method `credit`**, labelled "On account" in the UI. |
| Short payment? | **Refused.** Checkout blocks and names the shortfall; credit only happens when someone chose it. |
| Change on a credit sale? | **Not possible.** When any amount is on account, payments must equal the total exactly. |
| New enum values? | **None.** `payment_method` already contains `credit`. |

## Design

### Server — `internal/features/sales/service.go`

Today one figure, `paid`, sums every payment line (`service.go:348-356`). Since
an on-account line is not money, it must not be counted as money. Split it:

```go
// paid is money actually received; onAccount is the part the customer owes.
// They are summed separately because an "On account" line settles the sale
// without any money changing hands — counting it as paid would report a debt
// as a completed cash sale.
var paid, onAccount decimal.Decimal
for _, p := range in.Payments {
    amt, err := money.Parse(p.Amount)
    if err != nil || amt.IsNegative() {
        return apperr.Validation("payment amount is invalid")
    }
    if p.Method == "credit" {
        onAccount = onAccount.Add(amt)
    } else {
        paid = paid.Add(amt)
    }
}
```

Rules, in order:

1. `paid.Add(onAccount)` must be `>= total`. Otherwise reject with
   `apperr.Validation` naming the shortfall — for example
   *"Rs 700.00 is unpaid — take the money or put it on a customer's account."*
   This replaces the silent conversion and is the fix for the reported problem.
2. When `onAccount` is positive:
   - `in.CustomerID` is required (message: *"choose a customer to put this on account"*).
   - `paid.Add(onAccount)` must **equal** `total`; a credit sale gives no change.
   - The customer's `AvailableCredit()` must cover `onAccount`, else 409 as today.
   - `custRepo.AddBalance(ctx, customerID, onAccount)`.
   - `status = "credit"`.
3. When `onAccount` is zero: `status = "completed"` and `change = paid.Sub(total)`.

Validation tag changes:

- `PaymentInput.Method` (`service.go:74`): `oneof=cash card online wallet` →
  `oneof=cash card online wallet credit`.
- `CreateSaleInput.SaleType` (`service.go:81`): `oneof=retail wholesale credit` →
  `oneof=retail wholesale`.

The drawer needs no change: `CashCollectedSince` already counts only `cash`
(`service.go:894-898`), so an on-account line cannot inflate the expected till.

### Till — `templates/pages/cashier/pos.templ`, `static/js/app.js`

- Remove the **Credit** button (`pos.templ:208`). Retail and Wholesale remain.
- Add **On account** to the payment methods. It is disabled with the hint
  *"pick a customer first"* until a customer is selected.
- While a positive on-account amount is entered, show
  *"<Customer> will owe Rs X"* beneath the payment lines.
- Disable **Complete sale** whenever `paid + onAccount < total`, showing the
  shortfall. The client mirrors the server rule; the server remains the
  authority.
- `saleType` initialises to `retail`; any stored `credit` default falls back to
  `retail` (`app.js:300`, `app.js:894`).

### Reports and receipts

Every place that infers credit from `sale_type` reads the sale's `status` (or
the on-account payment total) instead. This is what stops the Receipts and
Sales sections describing a fully-paid sale as credit. The Sales report keeps
both columns — Type is now honestly just the price list, Status carries the
credit fact.

### Migration

One migration rewrites any surviving `sale_type = 'credit'` row to `'retail'`.
There are **zero** such rows in the live database today, so this is a safety net
for other deployments rather than a data fix.

The `sale_type` enum keeps its `credit` label: Postgres cannot drop an enum
value without rewriting the type, and the risk of that outweighs the tidiness.
The code simply stops writing it, and validation stops accepting it.

The `Down` migration is a no-op with a comment explaining why: the original
values are not recoverable, and re-widening the validation is a code concern,
not a schema one.

## Testing

**Unit (no database)** — the split-and-validate logic, extracted as a pure
function so each case is a table row:

- exact cash → completed, no change
- overpayment in cash → completed, change is the excess
- part cash + part on account → credit, balance rises by the on-account part only
- fully on account → credit, drawer unaffected
- on account with no customer → validation error
- on account beyond the credit limit → conflict
- short payment with no on-account line → validation error naming the shortfall
- on account plus overpayment → validation error (no change on a credit sale)

**Live:**
- Each case above through the real till.
- The customer's balance moves by exactly the on-account amount.
- The drawer's expected cash moves by exactly the cash amount.
- A part-credit sale prints a receipt showing both lines and the balance due.
- The Sales report shows Type `retail` with Status `credit` — and no sale
  anywhere is typed `credit`.

## Out of scope

- Changing how credit is *collected* (the existing DP- credit-payment flow).
- Credit limits, ageing, or statements.
- The other two cashier topics raised in the same conversation: small cash in
  and out (tips, change left behind, giveaways) and supplier interactions at the
  counter. Each gets its own spec.
