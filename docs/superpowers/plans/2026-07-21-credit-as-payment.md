# Credit as a Payment Type Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop credit sales hiding behind "retail" — make credit a payment line the cashier deliberately confirms, and show it in the receipts lists.

**Architecture:** `sale_type` shrinks to a price list (`retail`/`wholesale`). Credit becomes a payment line using the `credit` value already present in the `payment_method` enum. The sale service splits tender into money received and money owed, and derives `status` from the owed part alone, so type and status can no longer disagree. The till resolves any mismatch through a confirmation prompt before posting.

**Tech Stack:** Go 1.x, Echo, sqlx, PostgreSQL 17, goose, Templ, HTMX, Alpine.js, Tailwind, shopspring/decimal.

## Global Constraints

- **Leave these three files uncommitted, always:** `static/css/tailwind.css`, `cmd/server/enabled_plugins.go`, `.claude/settings.local.json`. Never `git add` them.
- **Never `git add` generated `*_templ.go` files** — they are gitignored.
- **Run `make css`** after adding any new Tailwind utility class; CSS is embedded in the binary, so rebuild and restart before trusting the browser.
- **Web-layer cycle rule:** feature packages (`internal/features/...`) never import `templates/...`.
- **Every migration reversible.** A `-- +goose Down` that re-imposes a narrower constraint must first delete or convert violating rows.
- **Nothing may force recounting stock or re-entering products.**
- Commit directly to `main`. End every commit message with:
  `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`
- Dev login for live checks: phone `0000000001`, PIN `2273`.
- Baseline the dev DB must still show after any live testing: **618 products, 15,008 units, Rs 2,087,632.50, 0 sales, 180 categories.** Delete any test rows you create.
- There is **no JavaScript test runner** in this repo. Client-side pure functions are verified by evaluating them in a real browser via Playwright, not by a unit-test file.

---

### Task 1: Split tender into money and debt

The sale service currently sums every payment line into one `paid` figure. An "On account" line is not money, so it must be counted separately or a debt gets reported as a completed cash sale.

**Files:**
- Create: `internal/features/sales/tender.go`
- Test: `internal/features/sales/tender_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `sales.MethodOnAccount = "credit"` (const)
  - `sales.Tender{Paid, OnAccount decimal.Decimal}`
  - `sales.SplitTender(methods []string, amounts []decimal.Decimal) Tender`
  - `sales.CheckTender(t Tender, total decimal.Decimal, hasCustomer bool, availableCredit decimal.Decimal) error` returning `nil`, or an `apperr` describing what is wrong.

- [ ] **Step 1: Write the failing test**

Create `internal/features/sales/tender_test.go`:

```go
package sales

import (
	"testing"

	"github.com/shopspring/decimal"
)

func td(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestSplitTenderSeparatesAccountFromMoney(t *testing.T) {
	got := SplitTender(
		[]string{"cash", "credit", "card"},
		[]decimal.Decimal{td("500"), td("700"), td("100")},
	)
	if !got.Paid.Equal(td("600")) {
		t.Errorf("Paid = %s, want 600", got.Paid)
	}
	if !got.OnAccount.Equal(td("700")) {
		t.Errorf("OnAccount = %s, want 700", got.OnAccount)
	}
}

func TestCheckTenderAcceptsExactCash(t *testing.T) {
	tn := Tender{Paid: td("1200"), OnAccount: decimal.Zero}
	if err := CheckTender(tn, td("1200"), false, decimal.Zero); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckTenderAcceptsOverpaymentInCash(t *testing.T) {
	tn := Tender{Paid: td("2000"), OnAccount: decimal.Zero}
	if err := CheckTender(tn, td("1200"), false, decimal.Zero); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// The bug this whole change exists to kill: money missing and nobody named.
func TestCheckTenderRejectsAShortfallWithNoAccountLine(t *testing.T) {
	tn := Tender{Paid: td("500"), OnAccount: decimal.Zero}
	if err := CheckTender(tn, td("1200"), true, td("9999")); err == nil {
		t.Error("a short-paid sale was accepted")
	}
}

func TestCheckTenderAcceptsPartCashPartAccount(t *testing.T) {
	tn := Tender{Paid: td("500"), OnAccount: td("700")}
	if err := CheckTender(tn, td("1200"), true, td("5000")); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckTenderRequiresACustomerForAnAccountLine(t *testing.T) {
	tn := Tender{Paid: td("500"), OnAccount: td("700")}
	if err := CheckTender(tn, td("1200"), false, decimal.Zero); err == nil {
		t.Error("an account line with no customer was accepted")
	}
}

func TestCheckTenderEnforcesTheCreditLimit(t *testing.T) {
	tn := Tender{Paid: decimal.Zero, OnAccount: td("700")}
	if err := CheckTender(tn, td("700"), true, td("300")); err == nil {
		t.Error("borrowing past the credit limit was accepted")
	}
}

// You cannot hand back cash against money that was never paid.
func TestCheckTenderRejectsChangeOnACreditSale(t *testing.T) {
	tn := Tender{Paid: td("1000"), OnAccount: td("700")}
	if err := CheckTender(tn, td("1200"), true, td("5000")); err == nil {
		t.Error("an over-covered credit sale was accepted")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/features/sales/ -run Tender -v`
Expected: FAIL — `undefined: SplitTender`, `undefined: Tender`, `undefined: CheckTender`

- [ ] **Step 3: Write minimal implementation**

Create `internal/features/sales/tender.go`:

```go
package sales

import (
	"karots-pos/internal/apperr"
	"karots-pos/internal/money"

	"github.com/shopspring/decimal"
)

// MethodOnAccount is the payment method that settles a sale without money
// changing hands. It is shown to cashiers as "On account".
const MethodOnAccount = "credit"

// Tender is a sale's payment split into what was actually received and what the
// customer now owes. They are kept apart because an on-account line is a debt,
// not money: counting it as paid would report a debt as a completed cash sale,
// and would inflate the drawer's expected balance.
type Tender struct {
	Paid      decimal.Decimal
	OnAccount decimal.Decimal
}

// SplitTender sorts parallel method/amount lists into the two figures.
func SplitTender(methods []string, amounts []decimal.Decimal) Tender {
	t := Tender{Paid: decimal.Zero, OnAccount: decimal.Zero}
	for i, m := range methods {
		if i >= len(amounts) {
			break
		}
		if m == MethodOnAccount {
			t.OnAccount = t.OnAccount.Add(amounts[i])
		} else {
			t.Paid = t.Paid.Add(amounts[i])
		}
	}
	return t
}

// CheckTender validates a split against the bill.
//
// A shortfall is refused rather than silently becoming credit — that silence
// was the defect: a credit sale would be recorded as an ordinary retail one and
// the debt was invisible in the receipts list. The till resolves a shortfall
// through its confirmation prompt and posts an explicit on-account line, so
// this is the backstop rather than the cashier's experience of it.
func CheckTender(t Tender, total decimal.Decimal, hasCustomer bool, availableCredit decimal.Decimal) error {
	covered := t.Paid.Add(t.OnAccount)
	if covered.LessThan(total) {
		return apperr.Validation(money.Display(total.Sub(covered)) +
			" is unpaid — take the money, or put it on a customer's account")
	}
	if !t.OnAccount.IsPositive() {
		return nil
	}
	if !hasCustomer {
		return apperr.Validation("choose a customer to put this on account")
	}
	// No change against money that was never paid: a part-account sale must
	// land exactly on the total.
	if covered.GreaterThan(total) {
		return apperr.Validation("this sale is over-paid — reduce the amount on account")
	}
	if t.OnAccount.GreaterThan(availableCredit) {
		return apperr.Conflict("credit limit exceeded (available " +
			money.Display(availableCredit) + ")")
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/features/sales/ -run Tender -v`
Expected: PASS (8 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/features/sales/tender.go internal/features/sales/tender_test.go
git commit -m "feat(sales): split tender into money received and money owed

An on-account line is a debt, not money. Summing it with cash — which is what
the sale service does today — reports a debt as a completed cash sale and would
inflate the drawer's expected balance.

CheckTender refuses a shortfall rather than silently converting it to credit.
That silence is the defect being fixed: the sale was recorded as ordinary
retail and the debt was invisible in the receipts list.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: Use the split in the sale transaction

**Files:**
- Modify: `internal/features/sales/service.go:74` (payment method validation)
- Modify: `internal/features/sales/service.go:81` (sale type validation)
- Modify: `internal/features/sales/service.go:348-379` (the paid/status block)

**Interfaces:**
- Consumes: `SplitTender`, `CheckTender`, `Tender`, `MethodOnAccount` (Task 1).
- Produces: no new exported names. `status` becomes `"credit"` exactly when `OnAccount` is positive.

- [ ] **Step 1: Widen the payment method, narrow the sale type**

At `internal/features/sales/service.go:74`:

```go
	Method    string  `json:"method"    validate:"required,oneof=cash card online wallet credit"`
```

At `internal/features/sales/service.go:81`:

```go
	SaleType     string         `json:"sale_type"     validate:"required,oneof=retail wholesale"`
```

- [ ] **Step 2: Replace the paid/status block**

Replace the block that currently runs from `paid := decimal.Zero` through the
closing brace of the `else` that sets `status = "credit"` (service.go:348-379)
with:

```go
		methods := make([]string, 0, len(in.Payments))
		amounts := make([]decimal.Decimal, 0, len(in.Payments))
		for _, p := range in.Payments {
			amt, err := money.Parse(p.Amount)
			if err != nil || amt.IsNegative() {
				return apperr.Validation("payment amount is invalid")
			}
			methods = append(methods, p.Method)
			amounts = append(amounts, amt)
		}
		tender := SplitTender(methods, amounts)

		// The customer is only loaded when something is going on their account,
		// so an ordinary cash sale still costs no extra query.
		available := decimal.Zero
		var cust *customers.Customer
		if tender.OnAccount.IsPositive() && in.CustomerID != nil {
			c, err := custRepo.FindByID(ctx, *in.CustomerID)
			if err != nil {
				return apperr.Validation("selected customer not found")
			}
			cust = c
			available = c.AvailableCredit()
		}
		if err := CheckTender(tender, total, in.CustomerID != nil, available); err != nil {
			return err
		}

		status := "completed"
		change := tender.Paid.Add(tender.OnAccount).Sub(total)
		if tender.OnAccount.IsPositive() {
			if err := custRepo.AddBalance(ctx, cust.ID, tender.OnAccount); err != nil {
				return apperr.Internal("failed to update customer balance", err)
			}
			status = "credit"
			change = decimal.Zero
		}
```

Confirm `internal/features/sales/service.go` imports
`karots-pos/internal/features/customers`; add it if the file does not already
reference the package by name.

- [ ] **Step 3: Build and run the whole suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all pass.

- [ ] **Step 4: Commit**

```bash
git add internal/features/sales/service.go
git commit -m "fix(sales): derive credit status from the on-account line, not underpayment

Underpayment silently converted a sale to credit, so a debt was recorded as an
ordinary retail sale and never showed in the receipts list. Status now follows
the on-account tender alone, which is the only place credit can be chosen, so
sale type and status can no longer disagree.

sale_type stops accepting 'credit' — it was inert, only ever affecting pricing
and only for wholesale — and the payment method starts accepting it.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: Migrate the three places the credit sale type is stored

The dangerous one is `settings.default_sale_type`: it is offered on the Settings page as a shop-wide default and seeds every sale. Shipping Task 2 against a shop that had chosen it would reject **every** sale, leaving the till unusable behind a setting only a working till can reach.

**Files:**
- Create: `migrations/0050_credit_is_a_payment.sql`

**Interfaces:**
- Consumes: nothing.
- Produces: no `credit` remains in `sales.sale_type`, `held_sales.sale_type` or `settings.default_sale_type`.

- [ ] **Step 1: Write the migration**

Create `migrations/0050_credit_is_a_payment.sql`:

```sql
-- +goose Up
-- Credit stops being a sale type and becomes a payment method (the
-- payment_method enum already carries 'credit'). Every stored credit sale type
-- is rewritten to 'retail', the price list it always really was — sale_type
-- only ever affected pricing, and only for wholesale.
--
-- settings.default_sale_type is the one that matters. It seeds every sale at
-- the till, so leaving it as 'credit' while the API stops accepting that value
-- would reject every sale and strand the shop behind a setting only a working
-- till can reach.
UPDATE sales      SET sale_type = 'retail' WHERE sale_type = 'credit';
UPDATE held_sales SET sale_type = 'retail' WHERE sale_type = 'credit';
UPDATE settings   SET default_sale_type = 'retail' WHERE default_sale_type = 'credit';

-- +goose Down
-- Deliberately a no-op. Which rows were once 'credit' is not recoverable, and
-- inventing them would be worse than leaving them as the price list they are.
-- Re-accepting the value is a code concern, not a schema one: the enum label
-- was never dropped, so a rolled-back binary still writes and reads it.
SELECT 1;
```

- [ ] **Step 2: Apply it**

Run: `bash -c 'set -a; . ./.env; set +a; go run ./cmd/server -migrate' 2>&1 | tail -4`
Expected: `OK 0050_credit_is_a_payment.sql` and `successfully migrated database to version: 50`

- [ ] **Step 3: Verify nothing anywhere is typed credit**

Run:
```bash
docker compose exec -T postgres psql -U pos_user -d pos_db -t -A -c "
select 'sales='||count(*) from sales where sale_type='credit';
select 'held='||count(*) from held_sales where sale_type='credit';
select 'setting='||count(*) from settings where default_sale_type='credit';"
```
Expected: `sales=0`, `held=0`, `setting=0`

- [ ] **Step 4: Commit**

```bash
git add migrations/0050_credit_is_a_payment.sql
git commit -m "feat(db): rewrite every stored credit sale type to retail

Three tables store it. settings.default_sale_type is the dangerous one: it
seeds every sale at the till, so tightening the API against a shop that chose
it would reject every sale and strand them behind a setting only a working till
can reach. held_sales would strand a parked basket. All three are zero rows
here, which is exactly why it must not be left to chance elsewhere.

Down is a no-op: which rows were once credit is unrecoverable, and the enum
label was never dropped so a rolled-back binary still works.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 4: Remove credit from settings and the held-sale default

**Files:**
- Modify: `internal/features/settings/settings.go:63`
- Modify: `templates/pages/admin/settings.templ:65`
- Modify: `internal/web/cashier.go:99`

**Interfaces:**
- Consumes: nothing.
- Produces: `DefaultSaleType` accepts only `retail`/`wholesale`; the till falls back to `retail` for any other stored value.

- [ ] **Step 1: Narrow the settings validation**

At `internal/features/settings/settings.go:63`:

```go
	DefaultSaleType       string  `json:"default_sale_type" form:"default_sale_type" validate:"required,oneof=retail wholesale"`
```

- [ ] **Step 2: Drop the Credit option from the Settings page**

Delete this line from `templates/pages/admin/settings.templ` (line 65):

```templ
							@saleTypeOption("credit", d.S.DefaultSaleType)
```

- [ ] **Step 3: Make the till defensive about a stale stored value**

`internal/web/cashier.go:99` currently reads the configured default straight
through. Replace that assignment so an out-of-range stored value cannot reach
the till:

```go
		// A database that predates credit-as-a-payment may still hold 'credit'
		// here. Falling back keeps the till usable instead of seeding every
		// sale with a type the API now rejects.
		if cfg.DefaultSaleType == "retail" || cfg.DefaultSaleType == "wholesale" {
			defaultType = cfg.DefaultSaleType
		}
```

Read the surrounding lines first and keep the existing initialisation of
`defaultType` (it already defaults to `retail`).

- [ ] **Step 4: Build and test**

Run: `templ generate && go build ./... && go vet ./... && go test ./...`
Expected: all pass.

- [ ] **Step 5: Prove the defensive fallback works**

```bash
docker compose exec -T postgres psql -U pos_user -d pos_db -c \
  "UPDATE settings SET default_sale_type='credit';"
```
Rebuild, restart, load `/cashier`, and confirm the page renders with Retail
selected rather than erroring. Then restore:
```bash
docker compose exec -T postgres psql -U pos_user -d pos_db -c \
  "UPDATE settings SET default_sale_type='retail';"
```

- [ ] **Step 6: Commit**

```bash
git add internal/features/settings/settings.go templates/pages/admin/settings.templ internal/web/cashier.go
git commit -m "feat(settings): credit is no longer a default sale type

The Settings page offered Credit as the shop-wide default and the till seeds
every sale from it, so it has to go at the same time the API stops accepting
the value. The till also now falls back to retail for any out-of-range stored
value, so a database that missed the migration stays usable rather than
rejecting every sale.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 5: On-account tender and the confirmation prompt

**Files:**
- Modify: `templates/pages/cashier/pos.templ:206-208` (type buttons)
- Modify: `templates/pages/cashier/pos.templ:310-332` (tender rows)
- Modify: `static/js/app.js` (the `pos()` component)

**Interfaces:**
- Consumes: `POST /api/sales` accepting `method: "credit"` (Task 2).
- Produces: `tenderIssue(total, paid, onAccount, hasCustomer)` on the `pos()` component, returning `null` or `{kind: "shortfall"|"overcovered", amount: Number}`.

- [ ] **Step 1: Remove the Credit sale-type button**

Delete line 208 of `templates/pages/cashier/pos.templ`:

```templ
							<button type="button" x-on:click="saleType='credit'" x-bind:class="saleType==='credit' ? 'bg-indigo-600 text-white' : 'bg-white text-slate-600'" class="px-3 py-1.5 border-l">Credit</button>
```

- [ ] **Step 2: Add the On-account tender button**

In the tender row at `templates/pages/cashier/pos.templ`, after the Online
button (line 316), add:

```templ
										<button
											type="button"
											x-on:click="customerId && selectMethod(p, 'credit', $event)"
											x-bind:disabled="!customerId"
											x-bind:title="customerId ? 'Put this amount on the customer account' : 'Pick a customer first'"
											x-bind:class="p.method==='credit' ? 'bg-amber-600 text-white' : 'bg-white text-slate-600'"
											class="px-2 py-1 border-l disabled:opacity-40"
										>On account</button>
```

- [ ] **Step 3: Add the pure decision function and the prompt state**

In `static/js/app.js`, inside the object returned by `pos(...)`, add:

```js
    // --- credit confirmation ---
    // tenderIssue is the whole rule in one place, so the click handler stays a
    // click handler. Returns null when the tender is fine, otherwise what the
    // prompt should offer to do about it.
    tenderIssue(total, paid, onAccount, hasCustomer) {
      const t = Number(total) || 0;
      const p = Number(paid) || 0;
      const a = Number(onAccount) || 0;
      const covered = p + a;
      if (covered < t - 0.005) {
        return { kind: "shortfall", amount: Number((t - covered).toFixed(2)), hasCustomer: !!hasCustomer };
      }
      if (a > 0 && covered > t + 0.005) {
        return { kind: "overcovered", amount: Number(a.toFixed(2)), hasCustomer: !!hasCustomer };
      }
      return null;
    },
    creditPrompt: null,
    paidTotal() {
      return this.payments
        .filter((p) => p.method !== "credit")
        .reduce((s, p) => s + (Number(p.amount) || 0), 0);
    },
    accountTotal() {
      return this.payments
        .filter((p) => p.method === "credit")
        .reduce((s, p) => s + (Number(p.amount) || 0), 0);
    },
    // Confirming the shortfall prompt adds the on-account line itself, so a
    // genuine credit sale is one extra tap rather than a manual tender entry.
    confirmPutOnAccount() {
      const amt = this.creditPrompt.amount;
      this.creditPrompt = null;
      this.payments.push({ method: "credit", amount: amt, reference: "" });
      this.checkout(true);
    },
    confirmRemoveFromAccount() {
      this.creditPrompt = null;
      this.payments = this.payments.filter((p) => p.method !== "credit");
      this.checkout(true);
    },
    cancelCreditPrompt() {
      this.creditPrompt = null;
    },
```

- [ ] **Step 4: Gate checkout on the prompt**

Find `async checkout(` in `static/js/app.js` and change its signature to
`async checkout(confirmed)`. Immediately after the existing `if (this.busy) return;`
guard at the top of the function, insert:

```js
      // Raise the prompt rather than posting a tender that does not add up.
      // `confirmed` is set by the prompt's own buttons so it cannot loop.
      if (!confirmed) {
        const issue = this.tenderIssue(
          this.total(), this.paidTotal(), this.accountTotal(), this.customerId,
        );
        if (issue) {
          this.creditPrompt = issue;
          return;
        }
      }
```

If the component's grand total is not `this.total()`, use whatever the template
binds for the payable total — search `pos.templ` for the TOTAL line and match it.

- [ ] **Step 5: Add the prompt markup**

In `templates/pages/cashier/pos.templ`, immediately before the closing tag of
the POS root element, add:

```templ
			<!-- Credit confirmation. Deliberately focuses NOTHING: a prompt that
			     Enter can dismiss is no better than the silent conversion it
			     replaces. -->
			<div x-show="creditPrompt" x-cloak class="fixed inset-0 bg-black/40 flex items-center justify-center z-50 p-4">
				<div class="bg-white rounded-2xl shadow-xl w-full max-w-sm p-6 space-y-3">
					<template x-if="creditPrompt && creditPrompt.kind === 'shortfall'">
						<div class="space-y-3">
							<h3 class="text-lg font-semibold" x-text="sym + ' ' + money(creditPrompt.amount) + ' is not paid'"></h3>
							<p class="text-sm text-slate-600">
								Put it on
								<span class="font-medium" x-text="customerLabel() || 'the customer'"></span>'s account?
							</p>
							<p class="text-sm text-amber-700" x-show="!creditPrompt.hasCustomer" x-cloak>
								Pick a customer first, or go back and take the money.
							</p>
							<div class="flex gap-2 pt-1">
								<button type="button" x-on:click="cancelCreditPrompt()" class="flex-1 px-4 py-2.5 rounded-lg border font-semibold">Go back</button>
								<button type="button" x-on:click="confirmPutOnAccount()" x-bind:disabled="!creditPrompt.hasCustomer" class="flex-1 px-4 py-2.5 rounded-lg bg-amber-600 text-white font-semibold disabled:opacity-40">Put on account</button>
							</div>
						</div>
					</template>
					<template x-if="creditPrompt && creditPrompt.kind === 'overcovered'">
						<div class="space-y-3">
							<h3 class="text-lg font-semibold">Nothing left on account</h3>
							<p class="text-sm text-slate-600">
								The payments already cover this sale, but
								<span class="font-medium" x-text="sym + ' ' + money(creditPrompt.amount)"></span>
								is marked On account.
							</p>
							<div class="flex gap-2 pt-1">
								<button type="button" x-on:click="cancelCreditPrompt()" class="flex-1 px-4 py-2.5 rounded-lg border font-semibold">Go back</button>
								<button type="button" x-on:click="confirmRemoveFromAccount()" class="flex-1 px-4 py-2.5 rounded-lg bg-indigo-600 text-white font-semibold">Remove from account</button>
							</div>
						</div>
					</template>
				</div>
			</div>
```

- [ ] **Step 6: Show what the customer will owe**

Beneath the tender rows in `templates/pages/cashier/pos.templ`, add:

```templ
							<div class="text-sm text-amber-700" x-show="accountTotal() > 0" x-cloak>
								<span x-text="customerLabel() || 'Customer'"></span> will owe
								<span class="font-semibold" x-text="sym + ' ' + money(accountTotal())"></span>
							</div>
```

- [ ] **Step 7: Generate, build, restyle**

Run: `templ generate && go build ./... && go vet ./... && go test ./... && make css`
Expected: all pass. Rebuild the binary and restart the server.

- [ ] **Step 8: Verify the decision function in a real browser**

There is no JS test runner, so evaluate the pure function directly. Log in at
`/cashier` with Playwright and run:

```js
const pos = window.Alpine.$data(document.querySelector('[x-data^="pos("]'));
const cases = [
  [1200, 1200, 0, true,  null],
  [1200, 2000, 0, true,  null],
  [1200,  500, 700, true, null],
  [1200,  500, 0, true,  "shortfall"],
  [1200, 1000, 700, true, "overcovered"],
];
cases.map(([t,p,a,c,want]) => {
  const got = pos.tenderIssue(t,p,a,c);
  return { t,p,a, got: got && got.kind, want, ok: (got && got.kind) === want || (got === null && want === null) };
});
```
Expected: every row `ok: true`.

- [ ] **Step 9: Commit**

```bash
git add templates/pages/cashier/pos.templ static/js/app.js
git commit -m "feat(till): credit is a tender the cashier confirms, not a silent fallback

The Credit sale-type button is gone — it changed no behaviour — and On account
joins Cash, Card and Online, disabled until a customer is picked. Completing a
sale that does not add up raises a prompt instead of posting: confirming a
shortfall adds the on-account line itself, so a genuine credit sale is one
extra tap rather than a manual tender entry.

The prompt focuses nothing, unlike the till's other dialogs. A prompt that
Enter can dismiss is no better than the silent conversion it replaces.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 6: Show the debt in the receipts lists

This is the reported defect: both receipts lists show one **Type** column drawn from `sale_type` and carry no status, so a sale the customer still owes money on is listed as plain "Retail", indistinguishable from one paid in full.

**Files:**
- Create: `templates/fragments/admin/saleStatus.templ`
- Modify: `templates/pages/cashier/receipts.templ:76` and `:83`
- Modify: `templates/pages/admin/receipts.templ:94` and `:101`

**Interfaces:**
- Consumes: `sales.Sale.Status`.
- Produces: `adminfragments.SaleStatusBadge(status string)`.

- [ ] **Step 1: Create the shared badge**

Create `templates/fragments/admin/saleStatus.templ`:

```templ
package adminfragments

// SaleStatusBadge shows whether a sale is settled. The receipts lists used to
// show only the sale type, so a sale the customer still owed money on was
// listed as plain "Retail" — the debt was invisible exactly where someone would
// look for it.
templ SaleStatusBadge(status string) {
	switch status {
		case "credit":
			<span class="px-2 py-0.5 rounded-full text-xs bg-amber-100 text-amber-800 font-medium">On account</span>
		case "returned":
			<span class="px-2 py-0.5 rounded-full text-xs bg-rose-100 text-rose-700">Returned</span>
		case "partially_returned":
			<span class="px-2 py-0.5 rounded-full text-xs bg-rose-50 text-rose-600">Part returned</span>
		case "void":
			<span class="px-2 py-0.5 rounded-full text-xs bg-slate-200 text-slate-600">Void</span>
		default:
			<span class="text-xs text-slate-400">Paid</span>
	}
}
```

- [ ] **Step 2: Add the column to the cashier list**

In `templates/pages/cashier/receipts.templ`, change the header row (line 76) to
insert a Status header after Type:

```templ
					<tr><th class="px-4 py-2">Receipt</th><th class="px-4 py-2">Date</th><th class="px-4 py-2">Type</th><th class="px-4 py-2">Status</th><th class="px-4 py-2 text-right">Total</th><th class="px-4 py-2 text-right">Print</th></tr>
```

and add the cell immediately after the `s.SaleType` cell (line 83):

```templ
							<td class="px-4 py-2">@adminfragments.SaleStatusBadge(s.Status)</td>
```

Change the empty-state `colspan="5"` to `colspan="6"`, and ensure the file
imports `adminfragments "karots-pos/templates/fragments/admin"`.

- [ ] **Step 3: Add the column to the admin list**

In `templates/pages/admin/receipts.templ`, change the header row (line 94) the
same way:

```templ
					<tr><th class="px-4 py-2">Receipt</th><th class="px-4 py-2">Date</th><th class="px-4 py-2">Type</th><th class="px-4 py-2">Status</th><th class="px-4 py-2 text-right">Total</th><th class="px-4 py-2 text-right">Print</th></tr>
```

and add the cell after the `s.SaleType` cell (line 101):

```templ
							<td class="px-4 py-2">@adminfragments.SaleStatusBadge(s.Status)</td>
```

Change that table's empty-state `colspan` to `6` as well, and ensure the import
is present.

- [ ] **Step 4: Generate, build, restyle**

Run: `templ generate && go build ./... && go vet ./... && go test ./... && make css`
Expected: all pass. Rebuild and restart.

- [ ] **Step 5: Commit**

```bash
git add templates/fragments/admin/saleStatus.templ templates/pages/cashier/receipts.templ templates/pages/admin/receipts.templ
git commit -m "fix(receipts): show whether a sale is still owed

Both receipts lists showed a single Type column drawn from sale_type and no
status at all, so a sale the customer still owed money on was listed as plain
Retail — the debt was invisible exactly where someone would look for it. This
is what the owner meant by credit sales hiding behind retail.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 7: End-to-end verification and cleanup

**Files:** none modified — this task only proves the work and restores the database.

- [ ] **Step 1: Create a throwaway customer with a credit limit**

```bash
curl -s -c /tmp/cj -X POST http://localhost:3000/login -d 'phone=0000000001&pin=2273' -o /dev/null
curl -s -b /tmp/cj -X POST http://localhost:3000/admin/customers \
  -d 'name=ZZ Credit Test' -d 'phone=0770000001' -d 'credit_limit=5000'
```

- [ ] **Step 2: Run every tender case through the real API**

Substitute a real stocked `product_id` and the new `customer_id`. Expect the
stated outcome for each:

| Payments | Expect |
|---|---|
| `cash 1200` on a 1200 bill | 201, status `completed` |
| `cash 2000` on a 1200 bill | 201, status `completed`, change 800 |
| `cash 500` + `credit 700` | 201, status `credit`, balance +700 |
| `cash 500` alone | 422 naming the Rs 700 shortfall |
| `credit 700` with no customer | 422 asking for a customer |
| `credit 9000` (limit 5000) | 409 credit limit exceeded |
| `cash 1000` + `credit 700` on 1200 | 422 over-paid |
| `sale_type: "credit"` | 422 invalid sale type |

- [ ] **Step 3: Confirm the balance and the drawer**

```bash
docker compose exec -T postgres psql -U pos_user -d pos_db -c \
  "select name, balance from customers where name='ZZ Credit Test';"
```
Expected: `balance` equals exactly the sum of the on-account amounts posted —
not the cash.

- [ ] **Step 4: Confirm the receipts lists**

Load `/cashier/receipts/sales` and `/admin/receipts` and confirm the
part-credit sale shows **On account** while the cash sales show **Paid**.

- [ ] **Step 5: Confirm the prompt in a browser**

At `/cashier`: add an item, enter less cash than the total, press Complete sale.
Confirm the prompt appears, that pressing Enter does nothing, that **Put on
account** is disabled until a customer is picked, and that confirming completes
the sale with the balance on the customer.

- [ ] **Step 6: Delete everything the test created**

```bash
docker compose exec -T postgres psql -U pos_user -d pos_db -c "
BEGIN;
DELETE FROM payments WHERE sale_id IN (SELECT id FROM sales);
DELETE FROM sale_items WHERE sale_id IN (SELECT id FROM sales);
DELETE FROM sales;
DELETE FROM stock_movements WHERE reference_type='sale';
DELETE FROM customers WHERE name LIKE 'ZZ %';
COMMIT;"
```

Then restore any stock the test sales consumed and confirm the baseline:

```bash
docker compose exec -T postgres psql -U pos_user -d pos_db -t -A -c "
select 'products='||count(*) from products;
select 'units='||sum(quantity) from stock;
select 'value='||sum(s.quantity*p.cost_price) from stock s join products p on p.id=s.product_id;
select 'sales='||count(*) from sales;"
```
Expected: `products=618`, `units=15008.000000`, `value=2087632.50000000`, `sales=0`.

- [ ] **Step 7: Commit nothing**

This task changes no files. If `git status` shows anything beyond the three
always-local files, something was left behind — find it before finishing.

---

## Self-review notes

**Spec coverage.** `sale_type` to a price list (Tasks 2, 4, 5); credit as a
payment line (Tasks 1, 2, 5); prompt on shortfall and the mirror case (Task 5);
no change on a credit sale (Task 1); no new enum values (nothing to do);
migration of all three stores (Task 3); settings and held-sales handling (Tasks
3, 4); receipts lists (Task 6); every listed test (Tasks 1, 5, 7).

**Two deviations from the spec, both deliberate:**
1. The spec said the till would disable **Complete sale** on a shortfall. It
   does not — the button stays live and raises the prompt, which is the whole
   point of the owner's revision. The spec's own prompt section supersedes its
   earlier line; the plan follows the prompt.
2. `confirmHost` is not reused. It focuses its yes button
   (`static/js/app.js:158`), and this prompt must focus nothing. It also cannot
   show the amounts and the credit limit. A dedicated dialog is correct here.

**Signature change that breaks callers until its task finishes:**
`checkout()` gains a `confirmed` argument (Task 5). Any existing
`x-on:click="checkout()"` keeps working — a missing argument is `undefined`,
which is falsy, so the prompt path runs. No other call site needs editing.
