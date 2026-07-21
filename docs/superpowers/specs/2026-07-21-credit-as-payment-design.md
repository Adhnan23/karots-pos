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
| Short payment? | **Confirmed, not refused.** Completing a short-paid sale raises a prompt naming the shortfall and the customer; confirming adds the On-account line itself. |
| Account line but paid in full? | **The mirror prompt** — offer to take it off the account and complete as a normal paid sale. |
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

   The server refuses rather than converting, because a silent conversion is
   the bug. The **till never sends a short-paid sale**: it resolves the
   shortfall through the confirmation prompt below and posts an explicit
   On-account line. This server rule is therefore the backstop, not the
   cashier's experience of it.
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
- `saleType` initialises to `retail`; any stored `credit` default falls back to
  `retail` (`app.js:300`, `app.js:894`).

#### The confirmation prompt

**Complete sale** stays enabled. Pressing it checks the tender before posting
and, when something does not add up, raises a prompt instead of submitting.

**Short paid** (`paid + onAccount < total`) — the common case, and the one the
owner reports going wrong:

> **Rs 700.00 is not paid**
> Total Rs 1,200.00 · Cash Rs 500.00 · Short Rs 700.00
> Put Rs 700.00 on Nimal's account? *(Nimal can owe up to Rs 5,000)*
> `[ Go back ]` `[ Put on account ]`

Confirming **adds the On-account line for the shortfall** and completes the
sale, so a genuine credit sale is one extra tap rather than a manual tender
entry. Going back returns to the payment box with everything intact.

**Account line no longer needed** (`onAccount > 0` and `paid >= total`) — the
mirror case:

> **Nothing left on account**
> Cash covers the whole Rs 1,200.00, but Rs 1,200.00 is marked On account.
> `[ Go back ]` `[ Remove from account ]`

Confirming drops the On-account line and completes as a normal paid sale.

Two guards against clicking through without reading, because a prompt nobody
reads is no better than the silent conversion it replaces:

- **No button is focused by default**, so Enter or a stray keypress cannot
  confirm it. This is a deliberate departure from the other dialogs in the
  till, which do focus their primary action.
- **"Put on account" is disabled with no customer selected**, showing
  *"pick a customer first"*. An accidental credit sale is then impossible
  rather than merely discouraged.

### Two other places `credit` is stored — both must be converted first

**`settings.default_sale_type`** (`internal/features/settings/settings.go:63`)
is validated `oneof=retail wholesale credit` and the Settings page offers
**Credit** as the shop-wide default (`templates/pages/admin/settings.templ:65`).
The till seeds `saleType` from it (`internal/web/cashier.go:99`).

This is the dangerous one. If a shop has that default set to `credit` and the
API starts rejecting `sale_type=credit`, **every sale fails** — the till is
unusable until an admin changes a setting they cannot reach past a broken till.
The migration must therefore rewrite the setting, and it must do so before the
tightened validation ships. This deployment has it set to `retail`, but the
migration cannot assume that.

Changes: drop `credit` from the validation tag, remove the third
`@saleTypeOption` from the settings page, and convert the stored value.

**`held_sales.sale_type`** (`internal/features/heldsales/heldsales.go:29`) — a
parked sale stores the type it was held under and restores it. A sale held as
`credit` before the change would restore an invalid type and be rejected on
checkout, stranding the parked basket. The migration converts these too. There
are zero held sales in this deployment.

### Reports and receipts

Every place that infers credit from `sale_type` reads the sale's `status` (or
the on-account payment total) instead. `sale_type` is still displayed — it is
honest now, being just the price list — in the Sales report
(`templates/pages/admin/mgmt_reports.templ:144`), admin Sales
(`templates/pages/admin/sales.templ:97`), the dashboard
(`templates/pages/admin/dashboard.templ:87`) and the cashier's recent-sales list
(`templates/pages/cashier/more.templ:67`).

**The receipts lists are the real defect.** Both the cashier's Receipts → Sales
tab (`templates/pages/cashier/receipts.templ:83`) and the admin one
(`templates/pages/admin/receipts.templ:101`) show a single **Type** column drawn
from `sale_type`, and carry **no status at all**. A sale where the customer
still owes money is therefore listed as plain "Retail", indistinguishable from
one paid in full. Nothing in either list reveals a debt.

Both lists gain a status indication beside the type: a muted "Paid" for a
completed sale and a highlighted **"On account"** for `status = 'credit'`,
plus the existing return states. This is what the owner is describing when they
say the receipts section is affected.

The printed slip already handles this correctly — it keys off
`Sale.Status == "credit"` and prints a "Total due" line
(`templates/pages/cashier/receipt.templ:153`) — so it needs no change, which
also confirms `status` is the right field to read.

### Migration

One migration rewrites every stored `credit` sale type to `retail`, in all
three places it can live:

```sql
UPDATE sales       SET sale_type = 'retail' WHERE sale_type = 'credit';
UPDATE held_sales  SET sale_type = 'retail' WHERE sale_type = 'credit';
UPDATE settings    SET default_sale_type = 'retail' WHERE default_sale_type = 'credit';
```

All three are zero rows in this deployment, but the settings one is not
optional: shipping the tightened validation against a shop whose default is
`credit` would reject every sale.

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

The prompt decision itself is a pure function of the tender — given `total`,
`paid`, `onAccount` and whether a customer is selected, it returns *none*,
*offer to put on account* (with the shortfall) or *offer to remove from
account*. Testing it directly keeps the rule out of the click handler.

**Live:**
- Each case above through the real till.
- Short-paying raises the prompt; confirming adds the On-account line and
  completes; going back leaves the cart and tender untouched.
- The prompt's confirm button is **not** focused — pressing Enter on it does
  nothing — and is disabled until a customer is picked.
- Marking the whole bill On account and then taking full cash raises the mirror
  prompt, and confirming completes it as a paid sale with no customer balance.
- The customer's balance moves by exactly the on-account amount.
- The drawer's expected cash moves by exactly the cash amount.
- A part-credit sale prints a receipt showing both lines and the balance due.
- The Sales report shows Type `retail` with Status `credit` — and no sale
  anywhere is typed `credit`.
- **Both receipts lists show "On account" against that sale** and "Paid"
  against a fully-paid one — the check that the reported receipts problem is
  actually fixed.
- The Settings page no longer offers Credit as a default sale type, and a
  database seeded with `default_sale_type = 'credit'` still takes sales after
  migrating.
- A sale held before the change and resumed after it still checks out.

## Out of scope

- Changing how credit is *collected* (the existing DP- credit-payment flow).
- Credit limits, ageing, or statements.
- The other two cashier topics raised in the same conversation: small cash in
  and out (tips, change left behind, giveaways) and supplier interactions at the
  counter. Each gets its own spec.
