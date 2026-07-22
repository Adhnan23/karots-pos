# Cashier expenses at the counter + a smart expense-category field

**Date:** 2026-07-22
**Status:** Approved (design)

## Problem

Two counter frustrations, one owner request:

1. **Expenses are admin-only.** Real costs happen during a shift while the owner
   isn't at the admin screen: an electricity or water bill arrives, the bags run
   out and the cashier is sent to buy more, a repairman is called in to fix the
   photocopier and must be paid. Today the cashier can sell but cannot record any
   of that, so cash leaves the drawer with no trail until an admin backfills it.

2. **The category field is a plain typed text box.** Every expense free-types a
   category, so the same cost is spelled three ways and the reporting is noisy.
   There is a short hardcoded `<datalist>` today, but it never learns from what
   has already been entered.

## Goals

- A cashier holding the existing counter-operations permission can record an
  expense at the till, paying only from cash locations they are allowed to use.
- The expense books money exactly as the admin path does — one atomic
  transaction, a CR- receipt, a ledger debit, the print policy, an audit line.
- The category field becomes a combo box that offers **hardcoded defaults plus
  every category already used**, deduplicated — no separate category table.
- Both improvements apply to the admin form and the new cashier form.

## Non-goals (YAGNI)

- No new permission flag. This "wraps under the same permission" as suppliers.
- No category-management CRUD screen. The learned list is derived, not stored.
- No expense **ledger view** for cashiers. The full list contains salaries and
  rent, which cashiers must not see. Feedback is the printed receipt + toast.
- No back-dating for cashiers. The cashier form always books *today*.
- No edit/delete of expenses by cashiers. Corrections stay an admin action.

## Permission

Reuse the existing `users.can_handle_suppliers` flag and its middleware
(`RequireSupplierAccess`, `MaySeeSuppliers`, `CanHandleSuppliers*`). The flag now
governs both suppliers and expenses at the counter. The only change is
user-facing: the checkbox label on the Users form broadens from
"Can handle suppliers" to **"Counter operations (suppliers & expenses)"** with a
one-line hint. Admin and manager roles pass the gate implicitly, as they do for
suppliers today. The internal helper names are left unchanged (renaming them is
churn with no behavioural gain).

## Cashier "Expenses" tab

- A new cashier tab, rendered next to "Suppliers" and gated on the same rule
  (`MaySeeSuppliers(role, CanHandleSuppliersCtx(ctx))`), added to both the tab
  strip and the command palette.
- Route group parallel to suppliers:
  `eg := cg.Group("/expenses", middleware.RequireSupplierAccess())`
  - `GET  /cashier/expenses`        — the page (form card).
  - `POST /cashier/expenses`        — record the expense.
- The page is a single card holding the expense form. No list of past expenses
  (salary/rent privacy).

### Cashier expense form

Fields:

- **Category** — smart combo (see below), `required`.
- **Amount** — `required`, `> 0`.
- **Description** — optional.
- **Paid from** — a picker restricted to the cashier's own till drawer plus
  cashier-accessible lockers, sourced from the existing `cashierCashSources`.
- **Relates to service** — optional dropdown of active service products
  (`products.ListServices`), so a repairman/toner cost is tagged to the service
  it belongs to. Same semantics as the admin field: still counted once as an
  operating expense, never COGS.

No date field. The handler stamps `expense_date = today`.

### Cashier create handler

Mirrors `adminUI.ExpenseCreate` precisely, differing only in the cash-source
validation:

1. Bind + validate `expenses.CreateInput` (category, amount, description,
   service_product_id). Ignore any submitted `expense_date`.
2. Resolve the source with `counterSource(c, "source")` — the same server-side
   guard the supplier counter uses. It accepts the cashier's till and
   cashier-flagged lockers only; a `locker:N` outside that set is rejected before
   any money moves.
3. In one `appdb.WithTx`:
   - `expenses.CreateInTx(ctx, tx, in, userID)`
   - `cashflow.MoveTx(ctx, tx, {From: src, To: External(), Amount, Reason:
     category[- description], ReceiptKind: "expense", Ref: expense, ActorID})`
4. `logAudit(ActionCreate, "expense", id, "recorded expense paid from …")`.
5. `afterMoneyMove(c, rec)` — the shared print-policy handler.

## Smart category field (admin + cashier)

The mechanism stays an `<input list="expense-cats">` bound to a `<datalist>`; only
the option set changes and moves from hardcoded-inline to computed-and-passed.

- `expenses.DefaultCategories() []string` — a shared canonical list. Extend the
  current six (Rent, Electricity, Salary, Transport, Water, Maintenance) with
  **Supplies** (the "buy bags" case) and **Repairs** (the repairman case).
- `Repository.DistinctCategories(ctx) ([]string, error)` —
  `SELECT DISTINCT category FROM expenses WHERE category <> '' ORDER BY category`.
- `expenses.MergedCategories(distinct []string) []string` — start from the
  defaults in their canonical order, then append any DB category not already
  present (compared case-insensitively, trimmed), keeping the DB extras in the
  alphabetical order the query returned. Unit-tested.
- `ExpenseFormData` gains a `Categories []string` field. The datalist renders its
  options from that slice instead of the inline literals.
- Both `adminUI.ExpenseForm`/`ExpenseEditForm` and the new cashier form handler
  load `DistinctCategories`, compute `MergedCategories`, and pass it in.

## Data flow

```
Cashier taps Expenses tab
  -> GET /cashier/expenses
       load cashierCashSources(till + allowed lockers)
       load active services
       load MergedCategories(DistinctCategories())
       render form card
  -> POST /cashier/expenses
       bind+validate; counterSource() guards the picked location
       WithTx: CreateInTx(expense) + MoveTx(source -> External, kind expense)
       audit + afterMoneyMove(print policy)
```

The admin path is unchanged except that its form now receives the merged
category list.

## Error handling

- Amount `<= 0` or unparseable → validation error (existing `parseCreate`).
- Empty category → validation error (`min=1`).
- Source not in the cashier's allowed set → rejected by `counterSource` before
  the transaction opens; nothing is written and no cash moves.
- Insufficient balance in the source locker → `cashflow.MoveTx` returns a 409 and
  the whole transaction rolls back (expense row included).
- Non-flagged cashier hitting the route → 403 from `RequireSupplierAccess`.

## Testing

- **Unit:** `MergedCategories` — defaults preserved and ordered first; a DB
  category duplicating a default (any case) is not repeated; a genuinely new DB
  category is appended; empty/whitespace DB rows ignored.
- **DB-guarded (rollback tx):** a cashier expense from an allowed source writes an
  expense row + a CR- receipt + a ledger debit that reconcile; a forbidden
  `locker:N` is rejected with nothing written.
- **Live (curl):** a flagged cashier records an expense from the till (expense
  row + receipt + drawer debit); an unflagged user gets 403; the category
  datalist for both admin and cashier contains hardcoded ∪ DB categories with no
  duplicates.

## Files touched (anticipated)

- `internal/features/expenses/expenses.go` — `DefaultCategories`,
  `DistinctCategories`, `MergedCategories` (+ test file).
- `internal/web/cashier_expenses.go` (new) — page + create handler, reusing
  `cashierCashSources` / `counterSource`.
- `internal/web/admin_more.go` — admin form handlers pass merged categories.
- `internal/web/web.go` — cashier `/expenses` route group.
- `templates/pages/admin/expenses.templ` — datalist renders from
  `ExpenseFormData.Categories`.
- `templates/pages/cashier/expenses.templ` (new) — the cashier form card.
- `templates/layouts/cashier.templ` — Expenses tab + palette entry.
- `templates/pages/admin/users.templ` — broadened flag label.
```
