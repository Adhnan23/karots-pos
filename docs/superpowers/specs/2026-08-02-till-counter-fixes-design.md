# Till counter fixes: sell-when-uncounted, credit at the till, no duplicate customers

**Date:** 2026-08-02
**Status:** approved design, ready for implementation plan

## Problem (owner, real counter pain)

Three things stop a cashier from completing an ordinary sale:

1. **A counted item shows 0, but the customer is holding it.** The stock count says 0
   (it was simply never counted, or under-counted), so the till greys the product out and
   the server refuses the line. The cashier cannot sell a pen the customer physically has.
   The only fix today is an admin stock adjustment — the cashier can't self-serve.
2. **A credit sale tips a customer over their credit limit and the sale is blocked outright.**
   The cashier can neither approve the over-limit sale nor raise the customer's limit — both
   are admin/manager-only.
3. **Cashiers create duplicate credit customers.** The till add-customer form makes phone
   optional and never checks for an existing match, so the same person is entered many times
   and their balance is split across duplicates.

## Guiding principle

The human at the counter, looking at the physical goods and the real customer, beats an
imperfect count or a stale limit. The system should **let the sale happen and record the
truth honestly** (an audit trail), not block it. Trust-sensitive powers are gated by a
per-user flag, matching the existing `supplier_counter_access` / cashier-expenses pattern.

---

## Section 1 — Sell an item the count shows short ("found at till")

### Insight that shaped this
This is **not** an oversell (selling stock you don't have). The unit physically exists; the
count was wrong (too low). So the correct action is to **correct the count up to reflect what
was actually there**, then sell it. Driving on-hand negative would misrepresent it — a
negative says "you owe the shelf a unit" when the opposite is true.

### Behaviour
- **Till UI** (`templates/pages/cashier/pos.templ:160`): remove the hard
  `x-bind:disabled="!p.is_service && Number(p.stock_qty) <= 0"`. A 0-stock non-service item
  becomes tappable, keeping a dim "0 left" chip.
- Tapping an item the till knows is short opens a small confirm: **"Stock shows 0 — sell
  anyway?"** (rush-safe: one tap, no typing). On confirm, `addToCart` proceeds and the line
  carries an `allow_oversell` intent.
- Checkout payload sends the flag so the server permits the short line.
- **Server** (`internal/features/sales/service.go`, the non-service branch at ~:372): when the
  line is short and the flag is set, before depleting, write a **positive `adjust` movement**
  noted **"found at till — count corrected before sale"** that raises on-hand to exactly cover
  the shortfall (opening an adjustment batch at the product's cost, so COGS stays sane). Then
  the existing `DecrementGuarded` + FEFO/DepleteBatch path runs and succeeds. Net: on-hand
  lands at **0**, with two honest movements (`+N found`, `−qty sell`).
- Without the flag, every path is byte-identical to today (`DecrementGuarded` still refuses a
  genuine shortfall — the flag is the only way to relax it).

### Interaction with per-batch pricing (already shipped, migration 0052)
- **Item has ≥2 live batches at different prices** → the existing **"which price?"** prompt
  (`pos.templ:502`) fires first, unchanged. The cashier reads the sticker and picks. If the
  **chosen batch** is short for the quantity, the found-stock top-up applies to **that same
  batch at that batch's price**, so the found unit inherits the price on the package.
- **One price, or zero live batches** → no price prompt; the found-correction opens a single
  adjustment lot at the **product's current selling price** and sells at that price.
- **0 total stock but the product historically sold at two prices** (both lots empty) →
  **use the product's current price, no prompt** (there are no live lots to choose; the
  cashier can still hand-edit the line price if the sticker differs).

### Visibility
Found-corrections are ordinary stock movements with the distinct note above — visible in
Stock & Movements like any adjustment. No new screen, fully traceable.

### Admin Stock Adjust is already batch-aware (checked — no change needed)
The admin Stock Adjust screen already handles multiple batches consistently with this design:
- **Reducing** shows a "If reducing, take from" lot picker (`stock.templ:388` →
  `fragments/admin/pickers.templ:38 LotPicker`) that appears **only when the product has >1
  live lot** (`x-show="lots.length > 1"`) — you pick which delivery the stock leaves from.
- **Increasing** opens a fresh adjustment lot (no "which batch" question), with an optional
  per-lot `selling_price`; any lot's price is editable later in the Batches modal
  (`stock.templ:264`).
The till found-stock correction reuses these same lot mechanics, so admin and till stay
consistent. No admin-side change is in scope.

### Not gated
Selling a short item is a normal counter action, gated only by the confirm — **not** by the
credit flag.

---

## Section 2 — Credit at the till (over-limit override + inline limit edit)

Both powers are gated by the per-user flag from Section 4 and re-verified server-side.

### Override this sale
- The credit prompt (`confirmPutOnAccount`, `pos.templ:689`) gains, for flagged cashiers, an
  **"Exceeds limit by Rs X — approve & continue"** confirmation when the on-account amount
  would breach available credit.
- Server: `CheckTender` (`internal/features/sales/tender.go:46`) takes a new
  `allowOverLimit bool`. When true it skips the `t.OnAccount.GreaterThan(availableCredit)`
  refusal (all other tender rules still apply). The debt is still posted honestly to the
  account. The web sale handler passes `allowOverLimit` only when the caller has the flag.
- The override is audit-logged (who approved which over-limit sale).

### Edit the credit limit inline
- A small **"Adjust credit limit"** control on the till's customer panel (near the customer
  chip). Raises (or sets) the stored `credit_limit`, then the normal check passes.
- Server: a **new cashier-accessible endpoint** that updates **only** `credit_limit` (not
  name/phone/address). The existing `PUT /api/customers/:id` stays admin/manager-only.
  New repo method `SetCreditLimit(ctx, id, limit)`; validated non-negative; audit-logged.

---

## Section 3 — No duplicate customers (phone required + reuse on match)

- **Till add-customer modal** (`pos.templ:474`): phone field label becomes **Required**;
  client blocks submit when blank.
- **Validation** (`internal/features/customers/customers.go:47`): `CreateInput.Phone` moves
  from `omitempty` to required (min length sensible for the locale, keep `max=15`). Phone is
  normalized (trim, strip spaces/dashes) before comparison and storage.
- **`Service.Create`**: before inserting, call the existing `FindByPhone`
  (`customers.go:137`) on the normalized phone. If an **active** customer matches, **return
  that existing customer** (with a signal, e.g. an `existed bool` or a distinct result) rather
  than inserting a duplicate. The API surfaces it so the till shows **"Customer already exists
  — using them"** and selects that customer.
- Admin create keeps working; the CSV importer already upserts by phone (`FindByPhone`),
  unaffected. Admin manual create form left as-is unless the owner later wants phone required
  there too (out of scope for now).

---

## Section 4 — Per-user flag "Manage credit at till"

- **Migration `0057`**: add a boolean column (e.g. `credit_counter_access`) to `users`,
  default `false`, mirroring `0051_supplier_counter_access.sql`.
- **Admin Users form**: a checkbox to grant the flag (beside the existing per-user cashier
  toggles). Threaded through the user create/update path.
- **Cashier page context**: the flag is passed into the till render so the UI shows/hides the
  over-limit override and the inline limit-edit control.
- **Server-side enforcement**: both the over-limit tender allowance and the credit-limit
  endpoint re-check the flag on the request's user — never trust the client (same posture as
  the supplier-at-counter 403 guard). A cashier without the flag gets the current behaviour
  (blocked over-limit, no limit editing).

---

## Blast radius & safety

- **Migration is additive** — one nullable/defaulted boolean column; no backfill, no data
  rewrite, one-line `goose down`.
- **Section 1 default path is unchanged**: without `allow_oversell`, `DecrementGuarded` still
  refuses a shortfall exactly as today; on-hand never goes negative in normal selling.
- **Section 2** adds a parameter to `CheckTender` (default `false` = today's behaviour) and a
  narrow new endpoint; the admin credit-limit path is untouched.
- **Section 3** tightens creation (phone required + dedup); existing customers and imports are
  unaffected.
- All work and E2E happen on the disposable dev `pos_db`.

## Out of scope

- Making phone required on the **admin** manual-create form.
- A dedicated "items that sold while showing 0" review list (Section 1 is recorded in
  movements only).
- A price picker for fully-empty multi-price products (decided: use current product price).
- Merging pre-existing duplicate customers (this only prevents new ones).

## Verification

1. `make build` (regenerates templ + css) + `go vet ./...` + `go test ./...`.
2. `goose up` → `down` → `up` on the dev DB — clean round-trip for `0057`.
3. **Section 1 unit/DB tests:** a short sale with `allow_oversell` writes a `+N` found
   `adjust` movement (with the note) then a `−qty` sell, leaving on-hand at 0; without the
   flag the same sale is refused ("insufficient stock"). Multi-batch: picking a short chosen
   batch tops up that batch at its price.
4. **Section 2 tests:** `CheckTender(..., allowOverLimit=true)` accepts an over-limit account
   line and still rejects a shortfall / over-payment; `SetCreditLimit` persists and is
   audit-logged; the credit endpoint is reachable by a flagged cashier and 403s for an
   unflagged one.
5. **Section 3 tests:** creating a customer with a phone that matches an active one returns
   the existing customer (no new row); blank phone is rejected.
6. **Live E2E on the dev server:** (a) scan a 0-stock item → confirm → sale completes, count
   shows 0, movements show found+sell; (b) push a customer over their limit as a flagged
   cashier → approve → sale posts to account; raise a limit inline → normal sale passes;
   (c) as an unflagged cashier both credit powers are absent/refused; (d) add a customer with
   an existing phone → till reuses the existing customer.
7. Restore the dev DB to baseline afterwards.
