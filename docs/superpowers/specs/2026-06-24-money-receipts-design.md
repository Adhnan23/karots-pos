# Money Receipts — design

Status: APPROVED design, to be built as a slice of the cash-flow work on branch
`cashflow-lockers`. Companion to `2026-06-24-cashflow-lockers-design.md`.

## Problem

Money moves (locker transfers, and — as later slices land — supplier payments,
customer collections, expenses, refunds, capital) leave a ledger trail but no
**receipt**: nothing dated and detailed to hand a customer/supplier, nothing to
tuck in the rubber band with cash put in a locker, and nothing searchable by a
unique number the way sale receipts are (`S-…`). The shop wants every money
movement to produce a tracked, viewable, reprintable receipt carrying the shop's
name and details.

## Decisions (from the user)

1. **Print = both, like sales.** A thermal ESC/POS slip (the rubber-band slip)
   *and* an on-screen HTML receipt page. Reprintable.
2. **Scope = every money movement.** `cashflow.Move` writes a receipt for every
   move, so transfers get one now and supplier/customer/expense/refund/capital
   get one automatically as those slices are wired.
3. **Unique searchable number** per receipt, `CR-000042` style (mirrors sale
   `S-…`).
4. **Tracked list** under Money: searchable (number / party / kind) + date-range
   presets, with view + reprint — mirroring the sale receipts list.
5. **Shop name + details** (address, phone) on every receipt, from Settings.
6. **Reuse + rename the existing print-prompt setting.** It already means *off →
   skip the prompt and auto-print; on → show a Print button*. Since it now covers
   payments too, rename it from `prompt_after_sale` / `PromptAfterSale` to
   `ask_to_print` / `AskToPrint` (label "Ask before printing (sales &
   payments)"). No new setting — one flag governs all receipts.
7. **Recharge stays as-is for now** (it already prints + reprints its own slips);
   fold recharge transactions into this unified registry in a later slice when
   recharge routes through `cashflow.Move`.
8. **Adjacent fix:** the sale receipts list already searches by receipt number;
   broaden it to also match **customer name / phone**.

## Data model

New core migration `0038_money_receipts.sql`:

```sql
CREATE SEQUENCE money_receipt_seq;

CREATE TABLE money_receipts (
  id          BIGSERIAL PRIMARY KEY,
  receipt_no  TEXT          NOT NULL UNIQUE,           -- 'CR-000042'
  kind        VARCHAR(24)   NOT NULL,                  -- transfer | supplier_payment | customer_payment | expense | refund | capital | bank_charge | interest | adjust | intake | payment
  from_label  TEXT          NOT NULL DEFAULT '',       -- 'Shop safe' | 'Till — Kasun' | 'External'
  to_label    TEXT          NOT NULL DEFAULT '',
  party       TEXT          NOT NULL DEFAULT '',        -- outside party name (customer/supplier), for search
  amount      NUMERIC(14,2) NOT NULL,
  note        TEXT          NOT NULL DEFAULT '',
  ref_kind    VARCHAR(24),                              -- soft link to the domain row (expense, supplier_payment, …)
  ref_id      BIGINT,
  created_by  BIGINT,
  created_at  TIMESTAMPTZ   NOT NULL DEFAULT now()
);
CREATE INDEX idx_money_receipts_created ON money_receipts (created_at DESC);
CREATE INDEX idx_money_receipts_kind    ON money_receipts (kind);
```

`receipt_no` is assigned in the INSERT: `'CR-' || lpad(nextval('money_receipt_seq')::text, 6, '0')` — atomic, gap-tolerant, no app-side counter.

## `cashflow.Move` integration

`MoveInput` gains optional fields (all default-safe so existing callers keep
working):

- `ReceiptKind string` — semantic label stored as `money_receipts.kind`
  (defaults to `"transfer"` when both ends are tracked own-piles, else
  `"payment"`/`"intake"` mirroring the locker-leg classifier).
- `Party string` — outside party name (customer/supplier) for the receipt + search.
- `Ref *Ref` — already exists; copied to `ref_kind`/`ref_id`.

Inside the existing single `db.WithTx`, after writing the ledger legs, Move
inserts one `money_receipts` row and returns the created `*Receipt` (so the
caller can open/print it). Labels are derived **in-tx**:

- Locker endpoint → the locker's name.
- Till endpoint → `"Till — " + cashier name` (join `users` via the session).
- External endpoint → `Party` if given, else `"External"`.

`cashflow.Move` itself does **no printing** and takes no settings dependency — it
only creates the receipt row and returns it. The print *policy* lives in the web
layer (below), so core stays pure and there is one place that knows the
`AskToPrint` rule.

One move = **one** receipt (a two-leg locker↔locker transfer still produces a
single receipt).

## Receipts feature

`internal/features/cashflow/receipts.go` (same package as Move — they share the
domain):

- `Receipt` struct mapping the table + `CreatedByName` (joined).
- `Repository` (on `db.Queryer`, tx-reusable): `Insert(ctx, in)` (used by Move
  in-tx), `List(ctx, Filter)` (q ILIKE over receipt_no/party/from/to + kind +
  date range, newest first, limit), `Get(ctx, id)`.
- Move uses `Repository.Insert` over its tx; the web layer uses a `Service`
  wrapper for List/Get.

## Printing

- **Thermal slip** — `buildReceiptSlip(cfg, Receipt) []byte` producing ESC/POS
  via `internal/printing` + the denomination/escpos helpers already used by sale
  receipts and recharge. Header = shop name + address + phone + currency from
  Settings; body = receipt no, date/time, kind, from → to, party, amount, note,
  cashier. Sent with `printing.Raw(ctx, cfg.ReceiptPrinter, …)` (no-op when no
  printer configured).
- **HTML page** — `GET /admin/money-receipts/:id` renders the same content as a
  print-friendly page (shop header from Settings) with a **Print** button
  (browser dialog, for A4/no-thermal) and a **Reprint slip** button
  (`POST /admin/money-receipts/:id/print` → re-sends the thermal slip,
  best-effort, returns a toast). Used for viewing + reprint at any time.

## Tracking UI

- `GET /admin/money-receipts` — list page under the **Money** nav section:
  search box (receipt no / party / kind), the shared `rptRangeForm` date presets,
  columns (date, no, kind, from → to, party, amount, by), row actions View /
  Reprint. Mirrors the sale receipts list.
- Nav entry `{"/admin/money-receipts", "Cash Receipts", "money-receipts", …}` in
  the Money section.

## Settings rename

Migration `0039_rename_prompt_after_sale.sql`:

```sql
-- +goose Up
ALTER TABLE settings RENAME COLUMN prompt_after_sale TO ask_to_print;
-- +goose Down
ALTER TABLE settings RENAME COLUMN ask_to_print TO prompt_after_sale;
```

Rename across the code: `Settings.PromptAfterSale` → `AskToPrint`, `UpdateInput`
form tag `prompt_after_sale` → `ask_to_print`, the settings checkbox label →
"Ask before printing (sales & payments)", and the cashier JS `promptAfterSale`
param/`pos.templ` wiring → `askToPrint` (behaviour unchanged for sales).

## Post-move UX — honors `AskToPrint`

A single shared web helper applies the print policy after every money move, so
all current and future money-move handlers behave identically:

```
afterMoneyMove(c, receipt):
  if settings.AskToPrint:             # "ask to print"
      HX-Redirect → /admin/money-receipts/:id   # the page with a Print button
  else:                               # "skip & print"
      best-effort thermal print of the slip
      toast "Receipt CR-… · <toast>"  + HX-Refresh / redirect back
```

This mirrors the sale checkout exactly (`askToPrint` → show the Print prompt;
else auto-print and move on). The thermal slip is therefore printed **once**,
only on the skip-and-print path; on the ask path nothing prints until the user
clicks Print (browser) or Reprint slip (thermal) on the receipt page.

## Adjacent: sale receipts search

`sales.ListFilter.Receipt` currently matches the receipt number only. Broaden the
cashier `/cashier/receipts` search so `q` also matches customer name/phone
(ILIKE), keeping the receipt-number match. Small, isolated change; no schema.

## Out of scope (follow-ups)

- Routing recharge deposit/withdrawal/billpay/reload through `cashflow.Move` so
  its slips join this registry (later cash-flow slice).
- Folding documents labour / supplier-pay / existing expense flows onto
  `cashflow.Move` — happens as each is wired in its own slice (3–8); each then
  produces a `CR-` receipt for free.

## Verification

- `make templ && go build ./... && go vet ./...` green; core-only build
  unaffected (no plugin or enabled_plugins change).
- E2E: a locker transfer creates exactly one `money_receipts` row with a unique
  `CR-` number, correct from/to labels and amount; the HTML receipt shows shop
  name + details; Reprint re-sends the slip; the list search finds it by number
  and by party; date presets filter it. psql confirms the row + sequence advance.
- E2E both print modes: with `AskToPrint` ON a transfer lands on the receipt
  page (Print button, nothing auto-printed); with it OFF the slip prints
  best-effort and the user stays on the lockers page with a toast. Sale checkout
  still respects the (renamed) flag.
- Sale receipts list search now also matches a customer name.
