# Admin bill payment & cash (get-money) + cashier bank-access fix

Date: 2026-07-30
Section: Reload & Bills plugin (`plugins/recharge`)

## Problem

Bill payment / get-money ("cash") is **cashier-only** today. The owner (admin)
also needs to run these from the back office — e.g. pay a customer's bill from
the shop bank while the physical cash lands in the shop safe, or hand a customer
cash out of the safe against money that landed in a bank account. The admin is
not sitting at a till, so the cash side cannot be assumed to be a till.

Two differences from the cashier flow drive the design:

1. **Cashier** is (and stays) restricted: the money can only come from / go to a
   **bank-type** locker the owner has marked accessible, and the physical cash is
   always the cashier's own open till. Today that "only if allowed" restriction is
   **not actually enforced** — see the bug below.
2. **Admin** may pick **any** existing storage pile on **both** sides — the
   account side and the physical-cash side — because the owner moves money
   between the safe, a bank, and the "pocket" locker freely. "Pocket / outside"
   is not a new concept: it is already a locker **kind** (`safe / bank / pocket /
   other`), so no External counterparty is introduced.

## Bug found (cashier side)

`plugins/recharge/cashier.go` `bankLockers()` lists banks via
`lockers.List(ctx, true)` (every active locker), **not** `ListForCashier`, which
filters `cashier_access = true`. So today a cashier's bill-pay picker shows every
bank regardless of the owner's `cashier_access` setting, and `BankTx` validates
only `Kind == bank && IsActive` — never `cashier_access`. The "only if allowed"
gate the owner expects does not exist. This spec fixes it (strict enforcement,
confirmed with the owner).

## What already exists (reused, not rebuilt)

- `cashflow.MoveTx` — one atomic transaction, a CR- receipt per leg, shows in
  core Cash Flow. The **cashier** `BankTx` already moves every leg this way
  (`cashflow.Till(uid)` / `cashflow.Locker(bankID)` / `cashflow.External()`),
  overdraw-guarded; a partial failure rolls the whole thing back.
- `adminUI.cashLocationChoices(ctx)` (`plugins/recharge/admin.go`) — lists every
  active locker (any kind, with balance) **plus** every open till, as
  `LocationChoice`s for the shared `LocationPicker`. Already used by admin Refill.
- `parseLocation(v)` — turns `"locker:3"` / `"till:5"` into a `cashflow.Location`.
- `RecordBillTx` / `BillTxByID` / `BillLedger` / `BillSlipPage` and the admin
  **Bills** + **Reload** receipts tabs — all already present on the admin side
  (`/admin/recharge/receipts/bill`, `/admin/recharge/bill/:id`, etc.).
- `printPolicy` behaviour (Ask-to-print → shared Print/Skip prompt; else
  best-effort print now) — admin Refill's exact pattern.
- `kickIfTill(ctx, loc)` — setting-gated, best-effort drawer pulse; no-op unless
  the location is a till.

## Schema — no migration

`recharge_bill_tx`: `session_id` is **nullable**; `bank_locker_id BIGINT NOT NULL`
has **no foreign key**. So an admin row records with `session_id = NULL`,
`bank_locker_id` = the account-side locker id (or `0` when the account side is a
till), `bank_name` = the account-side label. No schema change.

## Design

### 1. Admin "Bill payment & cash" page

- Hub card on the Reload & Bills admin hub (`HubPage`), linking a new page.
- Routes (`plugins/recharge/recharge.go`, admin group, existing manage-role
  gating): `GET /admin/recharge/bills` (page) and `POST /admin/recharge/bank-tx`
  (record).
- Page (`adminUI.Bills` → new templ `AdminBillsPage`): the same type toggle as the
  cashier form (**Pay a bill** / **Give cash out**) plus **two** `LocationPicker`s
  fed by `cashLocationChoices` (all lockers of any kind + open tills):
  - **billpay:** *Pay biller from* (account, goes down) and *Cash received into*
    (cash, goes up).
  - **getmoney:** *Cash paid out from* (cash, goes down) and *e-money lands in*
    (account, goes up).
  - Amount, optional service charge (extra cash into the cash side), reference,
    note. Labels flip with the type toggle (Alpine, mirroring `bankTxForm`).

### 2. `adminUI.BankTx` — money flow

Parse both locations with `parseLocation`; reject if the two sides are the same
pile. Build legs exactly like the cashier `BankTx`, substituting the picked
locations for the fixed bank/till, and run them in ONE `appdb.WithTx` /
`cashflow.MoveTx` transaction (overdraw on any leg rolls everything back):

- **billpay:** `account → External(biller)` for `amount`; then
  `External(customer) → cashDest` for `amount + svc`.
- **getmoney:** `cashSrc → External(customer)` for `amount`; then
  `External(customer) → account` for `amount`; then, if `svc > 0`,
  `External → cashSrc` for `svc`.

`ReceiptKind: typ`, `Party` = "Bill …"/"Customer" per leg (same labelling as
cashier). After the tx commits, `kickIfTill` on whichever picked side is a till
(best-effort, setting-gated).

**Negative-allowed lockers need no special handling.** `cashflow.Move`'s
`guardSource` (`cashflow.go:268`) already honours each pile's `allow_negative`: a
locker the owner marked negative-allowed (e.g. a "pocket"/owner account) may go
below zero, while a till or a normal locker cannot. So a source picked on either
side is guarded exactly per its own setting — the admin flow relies on that guard
rather than adding its own.

### 3. Record + receipts

`RecordBillTx` with `SessionID: nil`, `Type: typ`, `Amount`, `ServiceCharge`,
`Reference`, `Note`, `CreatedBy: adminUID`, `BankName` = account-side label,
`BankLockerID` = account-side locker id or `0`. Then the admin `printPolicy`
(Ask-to-print) over `/admin/recharge/bill/:id/print`. The existing admin Bills /
Reload receipts tabs and `BillSlipPage` list and reprint it with no change.

### 4. Cashier-side fix (strict enforcement)

- `bankLockers()` filters to `cashier_access = true` **bank** lockers (reuse
  `lockers.ListForCashier`, keep only `Kind == bank`). The cashier picker (`Banks`
  handler + `reconData.Banks`) then shows only allowed banks; empty ⇒ the form's
  existing "No banks" state disables submit — i.e. no access.
- `BankTx` gains a server-side guard: reject a posted `bank_locker_id` whose
  locker lacks `cashier_access` (defense in depth, mirroring the Refill 403 guard).
- Cashier money model otherwise unchanged: bank-type only, cash → own till.

Owner note: new lockers default `cashier_access = ON`. Before deploy the owner
should confirm `cashier_access` on the bank lockers cashiers use for bill-pay, so
none are unexpectedly locked out.

### 5. Tests

- DB-guarded admin `BankTx` test (mirroring `supplier_money_test.go` helpers):
  billpay and getmoney each land the right amounts in the two picked piles, in one
  atomic transaction, with a session-less `recharge_bill_tx` row; a same-side pick
  and an overdrawn source are both rejected.
- Cashier guard test: a `bank_locker_id` with `cashier_access = false` is rejected
  by `BankTx`, and `bankLockers()` omits it.

## Out of scope

- No External "pocket" counterparty — pocket is an existing locker kind.
- No change to the cashier money model (bank-only, cash → own till).
- No new cash-drawer mirroring for admin (cashflow.Move is the single source of
  truth, exactly as admin Refill already does).
- No schema/migration change.

## Verification

1. `make build` (regenerates templ) + `go vet ./...` + `go test ./plugins/recharge/... ./internal/web/...`.
2. Live admin billpay: pay from BOC bank → cash into Safe; confirm bank down, safe
   up, two CR- receipts, one bill slip, appears in admin Bills tab.
3. Live admin getmoney with a service charge: cash out of Safe, e-money into bank,
   svc back into Safe; confirm balances and the three CR- receipts.
4. Pick an open till as the cash side → confirm the drawer kick fires (setting on).
5. Cashier regression: a bank with `cashier_access = off` no longer appears and is
   rejected if posted; an accessible bank still works as before.
