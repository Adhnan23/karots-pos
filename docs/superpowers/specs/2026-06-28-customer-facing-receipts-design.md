# Customer-facing receipts: debt payment, returns, warranty

## Problem

Three customer-facing money operations hand the customer the wrong paper:

- **Debt payment (credit collection)** — cashier (`cashier.go:CreditPay`) and admin
  (`admin_more.go` ~1342) route the cash through `cashflow.Move` and then print the
  **generic money slip** (`buildReceiptSlip`: from→to / party / note / signature style).
  There is no customer-facing debt receipt. This is the main gap.
- **Returns/refunds** — already print a dedicated `*** REFUND ***` slip
  (`escpos.ReturnDocument`), but it omits the customer's remaining balance.
- **Warranty claims** — already print a dedicated `*** WARRANTY REPLACEMENT ***` slip
  (`escpos.WarrantyDocument`), but it shows only the end date, not the remaining months.

The internal money movement must still be tracked (the CR- money receipt for cash is
unchanged); separately, each flow must produce a detailed, customer-friendly slip.

## Decisions (locked with user)

- Scope: **build** the debt-payment receipt; **enhance** the existing return and warranty slips.
- Debt receipt shows: shop header, date, receipt no, customer name + **phone**, amount paid,
  method, **balance before → balance after**, **credit limit**, **cashier name**.
- Debt receipt is **reprintable & searchable** (not print-only).
- Receipt number prefix **`DP-`** (`DP-000123`), its own sequence.
- Reprint/search lives in a **new "Credit" tab** in the unified Receipts page, on **both**
  cashier and admin (mirrors the existing Sales / Cash / Warranty tabs).
- The CR- money record is still created for cash debt payments (tracking unchanged); it is
  simply no longer the paper handed to the customer for this flow.

## Architecture

Key insight: the debt receipt keys off the **customer payment record**, not the CR- money
receipt — because card/online debt payments never go through `cashflow.Move` (no CR- exists),
and CR- carries no balance snapshot. So the snapshot + number live on `customer_payments`.

### 1. Data — migration `migrations/0042_debt_receipts.sql`

```sql
-- Up
CREATE SEQUENCE debt_receipt_seq;
ALTER TABLE customer_payments
  ADD COLUMN receipt_no      TEXT,
  ADD COLUMN balance_before  NUMERIC(14,2),
  ADD COLUMN balance_after   NUMERIC(14,2);
-- backfill existing rows with a number so the list/reprint never sees a blank
UPDATE customer_payments
  SET receipt_no = 'DP-' || lpad(nextval('debt_receipt_seq')::text, 6, '0')
  WHERE receipt_no IS NULL;
CREATE UNIQUE INDEX customer_payments_receipt_no_key ON customer_payments(receipt_no);
```

`receipt_no` is assigned inside the INSERT (atomic, no app-side counter):
`'DP-'||lpad(nextval('debt_receipt_seq')::text,6,'0')`. Down drops the columns + sequence.

Backfilled rows have `balance_before/after` left NULL — the slip omits the before→after block
when either is NULL (old payments predate the snapshot), still printing amount/method/date.

### 2. Service — `internal/features/customers`

- `RecordPaymentTx` already reads/updates the customer row inside the tx. Capture
  `balance_before` (the outstanding balance it reads) and `balance_after` (`before − creditReduction`),
  write them + the generated `receipt_no` into the INSERT, and return them on `PaymentResult`
  (add `ReceiptNo`, `BalanceBefore`, `BalanceAfter`).
- `CustomerPayment` struct gains `ReceiptNo`, `BalanceBefore`, `BalanceAfter` (nullable for
  backfilled rows).
- New repo/service reads for the Credit receipts tab:
  - `ListPayments(ctx, Filter)` — q ILIKE over receipt_no + customer name/phone, date range,
    newest first, limit; joins `customers` (name, phone) and `users` (cashier name).
  - `GetPayment(ctx, id)` — one payment with the same joined fields, for View/reprint.
  (These return a `DebtReceipt` view struct with everything the slip/page needs.)

### 3. Slip — `internal/escpos`

`DebtSlip` struct + `DebtDocument(s DebtSlip, cfg, opts) []byte`, mirroring the
Return/Warranty document header/footer:

```
        <ShopName>  (raster sub-name)
       *** CREDIT PAYMENT ***
--------------------------------
Receipt:            DP-000123
Date:        2026-06-28 14:05
Customer:        Nimal Perera
Phone:             0771239876
--------------------------------
Amount paid          Rs. 2,000.00   (bold)
Method:                     Cash
--------------------------------
Previous balance     Rs. 5,000.00
Remaining balance    Rs. 3,000.00   (bold)
Credit limit         Rs.10,000.00
--------------------------------
Served by: Cashier
   Thank you - please retain.
```

The before→after + credit-limit block is omitted when balances are NULL.

### 4. Print at payment time

- **Cashier** (`cashier.go:CreditPay`): after `RecordPaymentTx`, build + send `DebtDocument`
  best-effort via the cashier print queue for **every** method (cash/card/online) —
  **replacing** the current `printMoneyReceipt(generic)` call. The cash CR- record is still
  created in-tx (unchanged); it is just not the printed paper.
- **Admin** (`admin_more.go` credit flow): same — print `DebtDocument` best-effort
  (`printing.Raw`, `cfg.ReceiptPrinter`) instead of `afterMoneyMove`'s generic slip for this
  flow. Honor the `ask_to_print` policy the same way the area already does.

A shared helper `buildDebtSlip(ctx, cfg, DebtReceipt) []byte` on `*Server` (in
`cashier_receipts.go`, beside `buildWarrantySlip`) keeps cashier + admin + reprint identical.

### 5. Reprint / search — new "Credit" tab

Mirror the existing Warranty tab wiring:
- Cashier: tab in `templates/pages/cashier/receipts.templ` + `ReceiptsCredit` fragment handler
  (`cashier_receipts.go`) listing `DP-` rows (date / no / customer / amount / method / by),
  search box + `shared.RangeForm` date presets, View + Reprint actions.
- Admin: matching tab + handler in the admin Receipts page (`admin_receipts.go`).
- View: print-friendly HTML page (shop header) with browser Print + "Reprint slip"
  (`POST …/credit/:id/print` → re-sends `DebtDocument`, toast). Reprint route on both roles.
- A reusable `cashierpages.ReceiptsCreditTab` / fragment renders the rows for both.

### 6. Return slip enhancement — `escpos.ReturnDocument`

When the returned sale has a customer, add below the totals:
`Remaining balance   Rs. X` (the customer's outstanding balance after the return).
`ReturnReceipt` gains optional `CustomerName *string` + `RemainingBalance *decimal.Decimal`;
`sales.ReturnReceipt(...)` populates them (join customer when present). Block omitted for
walk-in (no customer).

### 7. Warranty slip enhancement — `escpos.WarrantyDocument`

Show remaining months next to the end date:
`Warranty until: 2027-06-13 (8 mo left)`. Compute months-left from `time.Now()` to
`WarrantyUntil` in `buildWarrantySlip` (clamp at 0 = "expired"); pass on `WarrantySlip` as a
preformatted `WarrantyLeft string` so escpos stays format-only.

## Components & boundaries

| Unit | Responsibility | Depends on |
|------|----------------|-----------|
| migration 0042 | add number + balance snapshot columns | — |
| `customers` service | snapshot balances, assign DP- no, list/get payments | db |
| `escpos.DebtDocument` | render debt slip bytes (format only) | settings |
| `escpos.ReturnDocument`/`WarrantyDocument` | enhanced render (format only) | settings |
| `Server.buildDebtSlip` | assemble DebtSlip from a DebtReceipt | customers, settings, escpos |
| Credit tab handlers | list/search/view/reprint | customers, printing |

## Testing

- `make templ && go build ./... && go vet ./...` green; core-only build unaffected.
- Migration up adds columns + backfills every existing payment a unique `DP-` number; down
  reverts cleanly.
- **Debt, cash** (cashier): record a repayment → a `DP-` slip prints showing amount, method,
  previous→remaining balance, credit limit, cashier; the generic money slip is **not** printed;
  psql shows the `customer_payments` row with `receipt_no` + both balances and a CR- money row
  still exists for the cash leg.
- **Debt, card/online**: a `DP-` slip prints (no CR- row created — non-cash).
- **Reprint**: Credit tab lists the payment; search finds it by customer name, phone, and DP-
  number; date presets filter it; View shows the HTML page; Reprint re-sends the slip; a
  backfilled (pre-migration) payment reprints with amount/method but no before→after block.
- **Admin**: admin credit collection prints the `DP-` slip (not the generic money slip) and the
  Credit tab appears on admin too.
- **Return**: a return on a sale with a customer prints the refund slip now showing the
  remaining balance; a walk-in return omits it. Got-back amount (CASH REFUND) unchanged.
- **Warranty**: a claim prints the replacement slip showing "(N mo left)" next to the end date;
  an expired-cover case shows "expired".

## Constraints (carry-over)

Do NOT commit `cmd/server/enabled_plugins.go` (keep remote core-only). Leave
`static/css/tailwind.css` unstaged but run `make css` if new utility classes appear. `_templ.go`
are generated — run `make templ`, never stage them. Commit messages end with
`Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`.
