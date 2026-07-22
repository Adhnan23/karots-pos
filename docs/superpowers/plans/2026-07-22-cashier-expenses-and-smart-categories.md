# Cashier Expenses + Smart Category Field Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a cashier holding the counter-operations permission record an expense at the till (paying only from allowed cash locations), and turn the expense category field into a combo box that learns from categories already used — in both the admin and cashier forms.

**Architecture:** Reuse the existing `can_handle_suppliers` permission and its `RequireSupplierAccess`/`MaySeeSuppliers` middleware unchanged. Add three small pure helpers to the `expenses` feature (default categories, distinct-from-DB, merged/deduped). Add a new `cashierUI` handler that mirrors the admin expense-create flow exactly but validates the cash source through the existing `counterSource` guard. Reuse the existing `cashflow.MoveTx` atomic pattern, `printMoneyReceipt`, and print-prompt finish already used by the cashier supplier flow.

**Tech Stack:** Go, Echo, sqlx, Postgres, Templ, HTMX, Alpine, Tailwind (CSS embedded in binary), shopspring/decimal.

## Global Constraints

- Money uses `shopspring/decimal`; parse amounts with `money.Parse`, display with `money.Display`/`money.Format`.
- No new permission flag and no migration — reuse `users.can_handle_suppliers`.
- Cashier cash source is restricted server-side to `counterSource` (till + cashier-flagged lockers). A menu filter is never a permission.
- Cashier expense is always dated today — do not render or honor a date field on the cashier form.
- Cashiers must NOT see the expense ledger (salaries/rent are private) — the cashier tab is form-only.
- Web-layer cycle rule: feature packages never import `templates/...`; cross-package composition lives in `internal/web`.
- Templates are generated — run `templ generate` after editing any `.templ`. Static assets are embedded — rebuild the binary before live-testing.
- After adding Tailwind utility classes not already present, run `make css`.
- Default category set (canonical order): `Rent, Electricity, Salary, Transport, Water, Maintenance, Supplies, Repairs`.

---

### Task 1: Category helpers in the expenses feature

**Files:**
- Modify: `internal/features/expenses/expenses.go`
- Test: `internal/features/expenses/categories_test.go` (create)

**Interfaces:**
- Produces:
  - `func DefaultCategories() []string`
  - `func (r *Repository) DistinctCategories(ctx context.Context) ([]string, error)`
  - `func MergedCategories(distinct []string) []string`

- [ ] **Step 1: Write the failing test**

Create `internal/features/expenses/categories_test.go`:

```go
package expenses

import (
	"reflect"
	"testing"
)

func TestMergedCategoriesKeepsDefaultsFirst(t *testing.T) {
	got := MergedCategories(nil)
	want := DefaultCategories()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("with no DB rows, merged should equal defaults\n got: %v\nwant: %v", got, want)
	}
}

func TestMergedCategoriesAppendsNewDBCategories(t *testing.T) {
	got := MergedCategories([]string{"Bags", "Ink"})
	// Defaults first (canonical order), then the two new ones alphabetically.
	want := append(DefaultCategories(), "Bags", "Ink")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("new DB categories should append after defaults\n got: %v\nwant: %v", got, want)
	}
}

func TestMergedCategoriesDedupesCaseInsensitively(t *testing.T) {
	// "electricity" collides with default "Electricity"; "  water " with "Water".
	got := MergedCategories([]string{"electricity", "  water ", "Bags"})
	want := append(DefaultCategories(), "Bags")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("case/whitespace duplicates of defaults must not repeat\n got: %v\nwant: %v", got, want)
	}
}

func TestMergedCategoriesIgnoresBlankDBRows(t *testing.T) {
	got := MergedCategories([]string{"", "   "})
	want := DefaultCategories()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("blank DB rows must be ignored\n got: %v\nwant: %v", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/features/expenses/ -run TestMergedCategories -v`
Expected: FAIL — `undefined: MergedCategories` / `undefined: DefaultCategories`.

- [ ] **Step 3: Add the helpers**

In `internal/features/expenses/expenses.go`, add `"strings"` is already imported. Append these functions (near the bottom, after `TotalBetween` or alongside the repository methods):

```go
// DefaultCategories is the built-in expense category list, in the canonical
// order the combo box offers them. Learned categories from the DB are appended
// after these (see MergedCategories).
func DefaultCategories() []string {
	return []string{"Rent", "Electricity", "Salary", "Transport", "Water", "Maintenance", "Supplies", "Repairs"}
}

// DistinctCategories returns every category already used on an expense, so the
// combo box can suggest them next time without a category-management screen.
func (r *Repository) DistinctCategories(ctx context.Context) ([]string, error) {
	var rows []string
	err := r.q.SelectContext(ctx, &rows,
		`SELECT DISTINCT category FROM expenses WHERE category <> '' ORDER BY category`)
	return rows, err
}

// MergedCategories combines the built-in defaults with categories already used,
// preserving the defaults' canonical order first, then appending any DB category
// not already present (compared case-insensitively on the trimmed value) in the
// order given. Blank/whitespace DB rows are ignored.
func MergedCategories(distinct []string) []string {
	defaults := DefaultCategories()
	seen := make(map[string]bool, len(defaults)+len(distinct))
	for _, d := range defaults {
		seen[strings.ToLower(strings.TrimSpace(d))] = true
	}
	out := append([]string(nil), defaults...)
	for _, c := range distinct {
		key := strings.ToLower(strings.TrimSpace(c))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, strings.TrimSpace(c))
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/features/expenses/ -run TestMergedCategories -v`
Expected: PASS (all four).

- [ ] **Step 5: Commit**

```bash
git add internal/features/expenses/expenses.go internal/features/expenses/categories_test.go
git commit -m "feat(expenses): default + learned category helpers"
```

---

### Task 2: Shared category datalist + admin form uses merged categories

**Files:**
- Modify: `templates/fragments/admin/pickers.templ` (add `CategoryDatalist`)
- Modify: `templates/pages/admin/expenses.templ` (render datalist from slice; add `Categories` field)
- Modify: `internal/web/admin_more.go:369-395` (`ExpenseForm`, `ExpenseEditForm` pass merged categories)

**Interfaces:**
- Consumes: `expenses.MergedCategories`, `expenses.Repository.DistinctCategories` (Task 1).
- Produces:
  - `templ CategoryDatalist(id string, cats []string)` in package `adminfragments`.
  - `ExpenseFormData.Categories []string`.
  - `(*adminUI).expenseCategories(ctx) ([]string, error)` helper.

- [ ] **Step 1: Add the shared datalist fragment**

In `templates/fragments/admin/pickers.templ`, add:

```go
// CategoryDatalist renders an <datalist> of expense categories for an
// <input list="id">. Shared by the admin and cashier expense forms so both offer
// the same built-in ∪ learned category suggestions.
templ CategoryDatalist(id string, cats []string) {
	<datalist id={ id }>
		for _, c := range cats {
			<option value={ c }></option>
		}
	</datalist>
}
```

- [ ] **Step 2: Point the admin form at the slice**

In `templates/pages/admin/expenses.templ`:

Add the field to `ExpenseFormData` (after `Services []products.Product`):

```go
	// Categories are the built-in ∪ learned suggestions for the combo box.
	Categories []string
```

Replace the inline `<datalist id="expense-cats">…</datalist>` block (currently the six hardcoded `<option>`s) with:

```go
						@adminfragments.CategoryDatalist("expense-cats", d.Categories)
```

(The `<input name="category" … list="expense-cats" …>` line stays as-is.)

- [ ] **Step 3: Feed merged categories from the admin handlers**

In `internal/web/admin_more.go`, add a helper near the expense handlers:

```go
// expenseCategories returns the built-in ∪ already-used category list for the
// expense combo box.
func (a *adminUI) expenseCategories(ctx context.Context) ([]string, error) {
	distinct, err := expenses.NewRepository(a.db).DistinctCategories(ctx)
	if err != nil {
		return nil, err
	}
	return expenses.MergedCategories(distinct), nil
}
```

In `ExpenseForm`, after loading `svcs`, load categories and pass them:

```go
	cats, err := a.expenseCategories(c.Request().Context())
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.ExpenseForm(adminpages.ExpenseFormData{Sources: sources, Services: svcs, Categories: cats}))
```

In `ExpenseEditForm`, likewise after `svcs`:

```go
	cats, err := a.expenseCategories(c.Request().Context())
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.ExpenseForm(adminpages.ExpenseFormData{Expense: e, Services: svcs, Categories: cats}))
```

(`expenses` is already imported in `admin_more.go`.)

- [ ] **Step 4: Generate templates and build**

Run: `templ generate && go build ./...`
Expected: no errors.

- [ ] **Step 5: Verify the admin datalist merges DB categories**

Rebuild + restart is required only for live checks; the build passing plus this curl proves it. With the dev server running and an admin cookie in `$J`:

```bash
# Seed one non-default category via the admin API, then read the form.
curl -s -o /dev/null -b $J -X POST http://localhost:3000/admin/expenses \
  --data-urlencode "category=Bags" --data-urlencode "amount=100" \
  --data-urlencode "source=till:1"
curl -s -b $J -o /tmp/expform.html http://localhost:3000/admin/expenses/form
grep -o 'value="Bags"\|value="Electricity"\|value="Repairs"' /tmp/expform.html | sort -u
```
Expected: shows `value="Bags"`, `value="Electricity"`, `value="Repairs"` — defaults plus the learned "Bags". (If `till:1` has no open session the seed POST may 409; use any valid cash source from `/admin/expenses/form`.)

- [ ] **Step 6: Commit**

```bash
git add templates/fragments/admin/pickers.templ templates/fragments/admin/pickers_templ.go \
        templates/pages/admin/expenses.templ templates/pages/admin/expenses_templ.go \
        internal/web/admin_more.go
git commit -m "feat(expenses): admin category field learns from used categories"
```

---

### Task 3: Cashier expense page, handler, and routes

**Files:**
- Create: `internal/web/cashier_expenses.go`
- Create: `templates/pages/cashier/expenses.templ`
- Modify: `internal/web/web.go:242` (add cashier `/expenses` routes after the suppliers group)

**Interfaces:**
- Consumes: `(*cashierUI).cashierCashSources`, `(*cashierUI).counterSource`, `(*cashierUI).cashierSymbol`, `(*cashierUI).showChangePin`, `expenses.MergedCategories`, `expenses.NewRepository(...).DistinctCategories`, `expenses.CreateInput`, `Service.CreateInTx`, `cashflow.MoveTx`, `s.afterMoneyMove`/`s.printMoneyReceipt`, `adminfragments.LocationPicker`, `adminfragments.CategoryDatalist`.
- Produces:
  - `(*cashierUI).Expenses(c) error` — GET page.
  - `(*cashierUI).ExpenseRecord(c) error` — POST create.
  - `templ CashierExpense(d CashierExpenseData)` in `cashierpages`.
  - `CashierExpenseData{ CashierName, Role string; ShowChangePin bool; Symbol string; Sources []adminfragments.LocationChoice; Services []products.Product; Categories []string }`.

- [ ] **Step 1: Write the cashier expense page template**

Create `templates/pages/cashier/expenses.templ`:

```go
package cashierpages

import (
	"strconv"

	"karots-pos/internal/features/products"
	adminfragments "karots-pos/templates/fragments/admin"
	"karots-pos/templates/layouts"
)

// ====================== Expenses at the counter ======================

type CashierExpenseData struct {
	CashierName   string
	Role          string
	ShowChangePin bool
	Symbol        string
	Sources       []adminfragments.LocationChoice
	Services      []products.Product
	Categories    []string
}

// CashierExpense is a form-only page: a cashier records a running cost paid out
// of the drawer (a utility bill, bags, a repairman). No ledger is shown — the
// full expense list holds salaries and rent that cashiers must not see.
templ CashierExpense(d CashierExpenseData) {
	@layouts.Cashier("Expenses", d.CashierName, d.Role, "expenses", d.ShowChangePin) {
		<div class="h-full overflow-auto p-6">
			<div class="max-w-lg mx-auto">
				<h1 class="text-xl font-bold mb-1">Record an expense</h1>
				<p class="text-sm text-slate-500 mb-4">A bill, a quick purchase, or paying someone for a job. The cash leaves the source you pick and a receipt is printed.</p>
				if len(d.Sources) == 0 {
					<p class="text-sm text-amber-600 bg-amber-50 rounded-lg p-3">No till open and no cash locker you can use. Open your till first, or ask the owner to allow a locker.</p>
				} else {
					<form
						class="bg-white rounded-2xl shadow-sm p-6 space-y-4"
						hx-post="/cashier/expenses"
						hx-swap="none"
						hx-on::after-request="if(event.detail.successful) this.reset()"
					>
						<div>
							<label class="block text-sm font-medium mb-1">Category</label>
							<input name="category" required list="cexpense-cats" autofocus class="w-full border rounded-lg px-3 py-2" placeholder="e.g. Electricity"/>
							@adminfragments.CategoryDatalist("cexpense-cats", d.Categories)
						</div>
						<div>
							<label class="block text-sm font-medium mb-1">Amount</label>
							<input name="amount" type="number" step="0.01" min="0" required class="w-full border rounded-lg px-3 py-2"/>
						</div>
						<div>
							<label class="block text-sm font-medium mb-1">Description <span class="text-slate-400 font-normal">(optional)</span></label>
							<input name="description" class="w-full border rounded-lg px-3 py-2" placeholder="e.g. CEB monthly bill"/>
						</div>
						<div>
							<label class="block text-sm font-medium mb-1">Paid from</label>
							@adminfragments.LocationPicker("source", "— pick cash source —", d.Sources)
							<p class="text-xs text-slate-500 mt-1">Paying from your drawer needs your till open.</p>
						</div>
						if len(d.Services) > 0 {
							<div>
								<label class="block text-sm font-medium mb-1">For a service <span class="text-slate-400 font-normal">(optional)</span></label>
								<select name="service_product_id" class="w-full border rounded-lg px-3 py-2">
									<option value="0">— not for a service —</option>
									for _, sp := range d.Services {
										<option value={ strconv.FormatInt(sp.ID, 10) }>{ sp.Name }</option>
									}
								</select>
								<p class="text-xs text-slate-500 mt-1">e.g. paying a repairman for the photocopier — tags the cost to that service.</p>
							</div>
						}
						<div class="flex justify-end pt-2">
							<button type="submit" class="px-4 py-2 rounded-lg bg-emerald-600 text-white font-medium">Record expense</button>
						</div>
					</form>
				}
			</div>
		}
	}
}
```

- [ ] **Step 2: Write the cashier expense handler**

Create `internal/web/cashier_expenses.go`:

```go
package web

import (
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/expenses"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"
	cashierpages "karots-pos/templates/pages/cashier"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

// ============================ Expenses at the counter ============================
//
// A cashier is often alone when a running cost lands: a utility bill, bags that
// ran out, a repairman for the photocopier. These routes let them book it and pay
// it from their own drawer (or a cashier-allowed locker) — nowhere else. Gated by
// middleware.RequireSupplierAccess, the same counter-operations permission as
// suppliers: admins/managers always pass, a cashier only with the per-user flag.

// Expenses renders the form-only expense page. No ledger is shown to cashiers —
// the full list holds salaries and rent.
func (h *cashierUI) Expenses(c echo.Context) error {
	ctx := c.Request().Context()
	sources, err := h.cashierCashSources(ctx, middleware.CurrentUserID(c), middleware.CurrentUserName(c))
	if err != nil {
		return err
	}
	svcs, err := h.s.products.ListServices(ctx)
	if err != nil {
		return err
	}
	distinct, err := expenses.NewRepository(h.s.db).DistinctCategories(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, cashierpages.CashierExpense(cashierpages.CashierExpenseData{
		CashierName:   middleware.CurrentUserName(c),
		Role:          middleware.CurrentRole(c),
		ShowChangePin: h.showChangePin(c),
		Symbol:        h.cashierSymbol(ctx),
		Sources:       sources,
		Services:      svcs,
		Categories:    expenses.MergedCategories(distinct),
	}))
}

// ExpenseRecord books a counter expense and pays it out of the chosen cash source
// in ONE transaction, so the expense row, the drawer/locker debit and the receipt
// always commit together. The source is validated through counterSource: the
// cashier's till and cashier-flagged lockers only. Always dated today.
func (h *cashierUI) ExpenseRecord(c echo.Context) error {
	ctx := c.Request().Context()
	var in expenses.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	in.ExpenseDate = "" // cashiers can't backdate — parseCreate defaults to now
	if err := c.Validate(&in); err != nil {
		return err
	}
	src, err := h.counterSource(c, c.FormValue("source"))
	if err != nil {
		return err
	}
	userID := middleware.CurrentUserID(c)

	reason := strings.TrimSpace(in.Category)
	if in.Description != nil && strings.TrimSpace(*in.Description) != "" {
		reason += " - " + strings.TrimSpace(*in.Description)
	}
	var rec *cashflow.Receipt
	var expenseID int64
	err = appdb.WithTx(ctx, h.s.db, func(tx *sqlx.Tx) error {
		e, err := h.s.expenses.CreateInTx(ctx, tx, in, userID)
		if err != nil {
			return err
		}
		expenseID = e.ID
		r, err := h.s.cashflow.MoveTx(ctx, tx, cashflow.MoveInput{
			From:        src,
			To:          cashflow.External(),
			Amount:      e.Amount,
			Reason:      reason,
			ReceiptKind: "expense",
			Ref:         &cashflow.Ref{Kind: "expense", ID: e.ID},
			ActorID:     userID,
		})
		rec = r
		return err
	})
	if err != nil {
		return err
	}
	h.s.logAudit(c, audit.ActionCreate, "expense", strconv.FormatInt(expenseID, 10),
		"recorded expense paid from "+rec.FromLabel+" at the counter")

	msg := "Expense recorded — " + money.Display(rec.Amount)
	cfg, _ := h.s.settings.Get(ctx)
	if rec != nil && cfg != nil && cfg.AskToPrint {
		printURL := "/cashier/money-receipts/" + strconv.FormatInt(rec.ID, 10) + "/print"
		c.Response().Header().Set("HX-Trigger", response.PrintPrompt(msg+" · "+rec.ReceiptNo, printURL, false))
		return c.NoContent(200)
	}
	if rec != nil {
		h.s.printMoneyReceipt(ctx, rec)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast(msg, "success"))
	return c.NoContent(200)
}
```

- [ ] **Step 3: Register the routes**

In `internal/web/web.go`, immediately after the suppliers group block (after line `sg.POST("/products/wanted", cashier.ProductWantedCreate)`), add:

```go
	// Expenses at the counter — same counter-operations permission as suppliers.
	// A cashier can book a running cost (bill, bags, repairman) and pay it from
	// their till or a cashier-allowed locker; never the full expense ledger.
	xg := cg.Group("/expenses", middleware.RequireSupplierAccess())
	xg.GET("", cashier.Expenses)
	xg.POST("", cashier.ExpenseRecord)
```

- [ ] **Step 4: Generate templates and build**

Run: `templ generate && go build ./...`
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add internal/web/cashier_expenses.go templates/pages/cashier/expenses.templ \
        templates/pages/cashier/expenses_templ.go internal/web/web.go
git commit -m "feat(expenses): cashier can record an expense at the counter"
```

---

### Task 4: Cashier "Expenses" tab, palette entry, and broadened permission label

**Files:**
- Modify: `templates/layouts/cashier.templ:12-33` (palette) and `:54-56` (tab strip)
- Modify: `templates/pages/admin/users.templ:110-113` (broaden label copy)

**Interfaces:**
- Consumes: `middleware.MaySeeSuppliers`, `middleware.CanHandleSuppliersCtx` (existing).

- [ ] **Step 1: Add the palette entry**

In `templates/layouts/cashier.templ`, inside `cashierPalette`, extend the suppliers gate block:

```go
	if middleware.MaySeeSuppliers(role, canSuppliers) {
		out = append(out, paletteEntry{"/cashier/suppliers", "Suppliers", "Terminal"})
		out = append(out, paletteEntry{"/cashier/expenses", "Expenses", "Terminal"})
	}
```

- [ ] **Step 2: Add the tab**

In the `Cashier` templ tab strip, after the suppliers tab block:

```go
						if middleware.MaySeeSuppliers(role, middleware.CanHandleSuppliersCtx(ctx)) {
							@cashierTab("/cashier/suppliers", "Suppliers", active == "suppliers")
							@cashierTab("/cashier/expenses", "Expenses", active == "expenses")
						}
```

- [ ] **Step 3: Broaden the permission label**

In `templates/pages/admin/users.templ`, replace the label/hint spans for `can_handle_suppliers`:

```go
						<span class="text-sm font-medium">Counter operations (suppliers &amp; expenses)</span>
						<span class="block text-xs text-slate-500">Lets this person pay suppliers, take in deliveries, place orders, and record expenses from the till. They will see cost prices. Leave off for anyone who only sells.</span>
```

- [ ] **Step 4: Generate templates and build**

Run: `templ generate && go build ./...`
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add templates/layouts/cashier.templ templates/layouts/cashier_templ.go \
        templates/pages/admin/users.templ templates/pages/admin/users_templ.go
git commit -m "feat(expenses): cashier Expenses tab + broaden counter-ops permission label"
```

---

### Task 5: DB-guarded money test for the cashier expense flow

**Files:**
- Create: `internal/web/cashier_expense_money_test.go`

**Interfaces:**
- Consumes: `expenses.NewService`, `cashflow.NewService`, `expenses.CreateInput`, `cashflow.MoveInput`, the same seed/rollback pattern as `internal/web/supplier_money_test.go`.

**Note:** This test exercises the expense+move composition at the service layer (the exact calls the handler makes inside its tx), not the HTTP handler, so it needs no auth wiring. Read `internal/web/supplier_money_test.go` first and copy its DB-guard skeleton (env `DATABASE_URL` gate, `seedLockerBalance` helper, rollback `tx`). If a shared `seedLockerBalance`/DB-guard helper already exists in that file's package, reuse it rather than redefining.

- [ ] **Step 1: Write the test**

Create `internal/web/cashier_expense_money_test.go`. Mirror the structure of `supplier_money_test.go` (same package `web`, same `openTestDB`/skip guard and `seedLockerBalance` helper it uses — reuse them; do not redefine if present):

```go
package web

import (
	"context"
	"testing"

	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/expenses"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// TestCashierExpenseMovesMoney books an expense and its cash-out in one tx, then
// asserts the expense row, the receipt, and the locker debit all reconcile.
func TestCashierExpenseMovesMoney(t *testing.T) {
	db := openTestDB(t) // skips when DATABASE_URL is unset — see supplier_money_test.go
	ctx := context.Background()

	err := withRollback(db, func(tx *sqlx.Tx) error {
		lockerID := seedLockerBalance(t, tx, decimal.RequireFromString("1000"))

		exp := expenses.NewService(db)
		e, err := exp.CreateInTx(ctx, tx, expenses.CreateInput{
			Category: "Electricity", Amount: "250",
		}, 1)
		if err != nil {
			return err
		}

		cf := cashflow.NewService(db, nil)
		rec, err := cf.MoveTx(ctx, tx, cashflow.MoveInput{
			From:        cashflow.Locker(lockerID),
			To:          cashflow.External(),
			Amount:      e.Amount,
			Reason:      "Electricity",
			ReceiptKind: "expense",
			Ref:         &cashflow.Ref{Kind: "expense", ID: e.ID},
			ActorID:     1,
		})
		if err != nil {
			return err
		}

		// Receipt amount matches the expense.
		if !rec.Amount.Equal(decimal.RequireFromString("250")) {
			t.Fatalf("receipt amount = %s, want 250", rec.Amount)
		}
		// Locker balance dropped by exactly the expense.
		var bal decimal.Decimal
		if err := tx.GetContext(ctx, &bal,
			`SELECT balance FROM lockers WHERE id=$1`, lockerID); err != nil {
			return err
		}
		if !bal.Equal(decimal.RequireFromString("750")) {
			t.Fatalf("locker balance = %s, want 750", bal)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
}
```

**Before running:** open `internal/web/supplier_money_test.go` and confirm the exact names of the DB-guard helper (`openTestDB`), the rollback wrapper (`withRollback` or inline `BeginTxx`+`Rollback`), the `seedLockerBalance` signature and its balance-kind, and the `cashflow.Locker(id)` constructor. Adjust the calls above to match those exact names — do not invent helpers that don't exist. If `lockers.balance` is not the balance column name in this schema, use whatever `seedLockerBalance` and the ledger use.

- [ ] **Step 2: Run the test**

Run: `env $(grep -v '^#' .env | grep -v '^$' | xargs -d '\n') go test ./internal/web/ -run TestCashierExpenseMovesMoney -v`
Expected: PASS (or SKIP if the suite's DB guard is off — then run with the dev `DATABASE_URL` set as the other web DB tests do).

- [ ] **Step 3: Commit**

```bash
git add internal/web/cashier_expense_money_test.go
git commit -m "test(expenses): cashier expense books money and debits the source"
```

---

### Task 6: Live end-to-end verification

**Files:** none (manual verification against the running dev server).

- [ ] **Step 1: Rebuild and restart the dev server**

Static assets and templates are embedded, so a live check needs a fresh binary:

```bash
go build -o /tmp/claude-1000/pos ./cmd/server
pkill -f 'claude-1000/pos'
env $(grep -v '^#' .env | grep -v '^$' | xargs -d '\n') /tmp/claude-1000/pos &
```

- [ ] **Step 2: Confirm the permission gate**

With a cashier cookie that does NOT have the flag (`$C`):

```bash
curl -s -o /dev/null -w '%{http_code}\n' -b $C http://localhost:3000/cashier/expenses
```
Expected: `403`.

Grant the flag on that user (admin Users form or the API), re-login, and repeat.
Expected: `200`, and the page shows the form with a "Paid from" picker.

- [ ] **Step 3: Record an expense from the till and reconcile**

With the flagged cashier's till open, POST an expense from the drawer and confirm the money trail:

```bash
curl -s -o /dev/null -w '%{http_code}\n' -b $C -X POST http://localhost:3000/cashier/expenses \
  --data-urlencode "category=Supplies" --data-urlencode "amount=300" \
  --data-urlencode "description=bags" --data-urlencode "source=till:<UID>"
```
Expected: `200`. Then in the DB confirm one new `expenses` row (category "Supplies", 300, paid_by the cashier), one `money_receipts` CR- row for 300 kind "expense", and a matching drawer/ledger debit.

- [ ] **Step 4: Confirm the forbidden-source guard**

POST with a locker the cashier is NOT allowed (a `locker:N` where `cashier_access=false`):

```bash
curl -s -o /dev/null -w '%{http_code}\n' -b $C -X POST http://localhost:3000/cashier/expenses \
  --data-urlencode "category=Supplies" --data-urlencode "amount=300" \
  --data-urlencode "source=locker:<forbiddenID>"
```
Expected: `403` ("you can't take cash from there"), and NO new expense row and NO cash movement for it.

- [ ] **Step 5: Confirm the category combo learns**

```bash
curl -s -b $C -o /tmp/cexp.html http://localhost:3000/cashier/expenses
grep -o 'value="Supplies"\|value="Electricity"\|value="bags"' /tmp/cexp.html | sort -u
```
Expected: `value="Electricity"` and `value="Supplies"` present (defaults ∪ the just-used category). "bags" is a description, not a category, so it must NOT appear.

- [ ] **Step 6: Restore the dev DB baseline**

Delete the test expense rows and their receipts/ledger entries created during verification so the dev DB returns to its baseline (products/units/value unchanged), the same tidy-up discipline used after every live test.

---

## Self-Review

**Spec coverage:**
- Permission reuse + broadened label → Tasks 3 (routes use `RequireSupplierAccess`), 4 (label). ✓
- Cashier Expenses tab, form-only, no ledger → Tasks 3, 4. ✓
- Fields incl. relates-to-service, date forced today → Task 3 (`in.ExpenseDate = ""`, service dropdown). ✓
- Cash source restricted via `counterSource` → Task 3 + guard test Task 6 step 4. ✓
- Atomic money path (CreateInTx + MoveTx + audit + print) → Task 3, test Task 5. ✓
- Smart category (defaults ∪ DB, deduped, both forms) → Task 1 (helpers), 2 (admin), 3 (cashier). ✓
- Testing (unit merge, DB-guarded money, forbidden source, live) → Tasks 1, 5, 6. ✓

**Placeholder scan:** `<UID>`/`<forbiddenID>` in Task 6 are live runtime values chosen at verification time, not code placeholders. No TBD/TODO in code steps.

**Type consistency:** `MergedCategories([]string) []string`, `DistinctCategories(ctx) ([]string, error)`, `DefaultCategories() []string`, `CategoryDatalist(id string, cats []string)`, `ExpenseFormData.Categories`, `CashierExpenseData` fields, `Expenses`/`ExpenseRecord` handlers — all referenced consistently across tasks. Task 5 explicitly instructs verifying helper names against `supplier_money_test.go` before running (the one place names could drift from an unseen file).
