# Targeted Old-Debt Payments & Editing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an admin add to, correct, and pay down a customer's/supplier's old (pre-system opening) debt as a distinct target, without ever re-entering the whole figure — cashier flows unchanged.

**Architecture:** Reuse the existing `opening_unlinked`/`outstanding_balance` columns. Add one shared repo method `PayOpening` (customers + suppliers) that reduces the old debt directly, wire it into the two admin pay handlers as a selectable target, and give the shared opening-edit modal an Add/Set-exact toggle. No migration.

**Tech Stack:** Go, sqlx + PostgreSQL, Echo, templ, HTMX + Alpine.js, shopspring/decimal.

**Spec:** `docs/superpowers/specs/2026-08-30-targeted-old-debt-payments-design.md`

## Global Constraints

- **No DB migration** — every operation reuses existing columns (`opening_balance`, `opening_unlinked`, `outstanding_balance`).
- **Admin only.** Cashier handlers (`cashier_suppliers.go` `SupplierPayForm`/`SupplierPayAtCounter`, `cashier.go` `CreditPay`) and their templates MUST NOT change behaviour — verify they still pay the whole/auto-allocate as before.
- **A payment leaves `opening_balance` (gross) untouched.** Only corrections (`AdjustOpening`) move the gross figure.
- **Overpay is rejected, never silently capped** — an old-debt payment above `opening_unlinked` returns a validation error so the receipt always equals cash received.
- DB-backed repo tests follow `internal/features/suppliers/opening_test.go`: skip when `DATABASE_URL` unset, create + `DELETE` own row so the dev DB is left clean.
- Money is `decimal.Decimal`; parse user input with `money.Parse`. After editing any `.templ`, run `templ generate`; after CSS class changes run `make css`.

---

### Task 1: `customers.PayOpening` repo method

**Files:**
- Modify: `internal/features/customers/customers.go` (add method near `AddBalance` ~line 170)
- Test: `internal/features/customers/opening_pay_test.go` (create)

**Interfaces:**
- Produces: `func (r *Repository) PayOpening(ctx context.Context, id int64, amt decimal.Decimal) error`

- [ ] **Step 1: Write the failing test**

```go
package customers

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	appdb "karots-pos/internal/db"

	"github.com/shopspring/decimal"
)

// TestPayOpening proves paying the old debt down reduces outstanding and
// opening_unlinked together, leaves the linked part alone, and never goes below
// zero. Creates and hard-deletes its own customer row.
func TestPayOpening(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	conn, err := appdb.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := context.Background()
	svc := NewService(conn)
	repo := NewRepository(conn)

	name := fmt.Sprintf("payopening-test-%d", time.Now().UnixNano())
	phone := fmt.Sprintf("%d", time.Now().UnixNano()) // unique, dedup guard requires phone
	cust, err := svc.Create(ctx, CreateInput{Name: name, Phone: &phone, OpeningBalance: "10000"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `DELETE FROM customers WHERE id = $1`, cust.ID)

	check := func(step, wantOut, wantUnlinked, wantLinked string) {
		t.Helper()
		c, gerr := svc.Get(ctx, cust.ID)
		if gerr != nil {
			t.Fatalf("%s: %v", step, gerr)
		}
		if got := c.OutstandingBalance.StringFixed(2); got != wantOut {
			t.Errorf("%s: outstanding = %s, want %s", step, got, wantOut)
		}
		if got := c.OpeningUnlinked.StringFixed(2); got != wantUnlinked {
			t.Errorf("%s: opening_unlinked = %s, want %s", step, got, wantUnlinked)
		}
		if got := c.LinkedBalance().StringFixed(2); got != wantLinked {
			t.Errorf("%s: linked = %s, want %s", step, got, wantLinked)
		}
	}

	// A 5,000 credit sale: linked rises, opening untouched.
	if err := repo.AddBalance(ctx, cust.ID, decimal.RequireFromString("5000")); err != nil {
		t.Fatal(err)
	}
	check("after sale", "15000.00", "10000.00", "5000.00")

	// Pay 4,000 against the OLD debt specifically: opening drops to 6,000, linked
	// stays at 5,000 (unlike AddBalance, which would settle linked first).
	if err := repo.PayOpening(ctx, cust.ID, decimal.RequireFromString("4000")); err != nil {
		t.Fatal(err)
	}
	check("after pay opening", "11000.00", "6000.00", "5000.00")

	// Paying more than the remaining opening clamps opening at 0 (guard lives in
	// the service; the repo itself must not go negative).
	if err := repo.PayOpening(ctx, cust.ID, decimal.RequireFromString("99999")); err != nil {
		t.Fatal(err)
	}
	check("after overpay clamp", "5000.00", "0.00", "5000.00")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `DATABASE_URL="$DATABASE_URL" go test ./internal/features/customers/ -run TestPayOpening -v`
Expected: FAIL — `repo.PayOpening undefined`.

- [ ] **Step 3: Write minimal implementation**

Add after `AddBalance` in `customers.go`:

```go
// PayOpening reduces the old (opening) debt directly: outstanding_balance and
// opening_unlinked each drop by amt (clamped at zero), leaving the gross
// opening_balance and the linked (transactional) part untouched. Contrast
// AddBalance, which settles the linked part first. Callers must guard against
// overpaying the opening; the clamp here is a safety net, not the policy.
func (r *Repository) PayOpening(ctx context.Context, id int64, amt decimal.Decimal) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE customers SET
			outstanding_balance = GREATEST(outstanding_balance - $1, 0),
			opening_unlinked    = GREATEST(opening_unlinked    - $1, 0)
		WHERE id = $2`, amt, id)
	return err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `DATABASE_URL="$DATABASE_URL" go test ./internal/features/customers/ -run TestPayOpening -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/features/customers/customers.go internal/features/customers/opening_pay_test.go
git commit -m "feat(customers): PayOpening reduces old debt directly"
```

---

### Task 2: Customer payment can target old debt (`ApplyToOpening`)

**Files:**
- Modify: `internal/features/customers/customers.go` — `PaymentInput` (~line 65), `RecordPaymentTx` (~line 536)
- Test: `internal/features/customers/opening_pay_test.go` (add a test)

**Interfaces:**
- Consumes: `Repository.PayOpening` (Task 1).
- Produces: `PaymentInput.ApplyToOpening bool` (form tag `apply_to_opening`); `RecordPaymentTx` honours it and rejects an old-debt payment above `opening_unlinked`.

- [ ] **Step 1: Write the failing test**

Add to `opening_pay_test.go`:

```go
// TestRecordPaymentTargetsOpening proves an admin payment flagged ApplyToOpening
// pays the old debt (not linked-first), and that overpaying it is rejected.
func TestRecordPaymentTargetsOpening(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	conn, err := appdb.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := context.Background()
	svc := NewService(conn)
	repo := NewRepository(conn)

	name := fmt.Sprintf("paytarget-test-%d", time.Now().UnixNano())
	phone := fmt.Sprintf("%d", time.Now().UnixNano())
	cust, err := svc.Create(ctx, CreateInput{Name: name, Phone: &phone, OpeningBalance: "10000"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `DELETE FROM customers WHERE id = $1`, cust.ID)
	if err := repo.AddBalance(ctx, cust.ID, decimal.RequireFromString("5000")); err != nil {
		t.Fatal(err)
	} // outstanding 15000, opening 10000, linked 5000

	// Pay 3,000 against the old debt: opening 10000 -> 7000, linked stays 5000.
	if err := svc.RecordPayment(ctx, cust.ID,
		PaymentInput{Amount: "3000", Method: "cash", ApplyToOpening: true}, 0); err != nil {
		t.Fatal(err)
	}
	c, _ := svc.Get(ctx, cust.ID)
	if got := c.OpeningUnlinked.StringFixed(2); got != "7000.00" {
		t.Fatalf("opening = %s, want 7000.00", got)
	}
	if got := c.LinkedBalance().StringFixed(2); got != "5000.00" {
		t.Fatalf("linked = %s, want 5000.00", got)
	}

	// Overpaying the old debt (7,000 left) is rejected.
	if err := svc.RecordPayment(ctx, cust.ID,
		PaymentInput{Amount: "8000", Method: "cash", ApplyToOpening: true}, 0); err == nil {
		t.Fatal("expected rejection paying more than the old debt")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `DATABASE_URL="$DATABASE_URL" go test ./internal/features/customers/ -run TestRecordPaymentTargetsOpening -v`
Expected: FAIL — `unknown field ApplyToOpening`.

- [ ] **Step 3: Write minimal implementation**

In `PaymentInput` add the field:

```go
type PaymentInput struct {
	Amount    string  `json:"amount"    form:"amount"    validate:"required"`
	Method    string  `json:"method"    form:"method"`
	Reference *string `json:"reference" form:"reference"`
	Note      *string `json:"note"      form:"note"`
	// ApplyToOpening routes the payment against the old (opening) debt instead of
	// the transactional balance. Admin-only; the cashier flow leaves it false.
	ApplyToOpening bool `json:"apply_to_opening" form:"apply_to_opening"`
}
```

In `RecordPaymentTx`, after `before := cust.OutstandingBalance` / `after := before.Sub(amt)` and before the balance write, replace the single `AddBalance` call with a branch:

```go
	if in.ApplyToOpening {
		if amt.GreaterThan(cust.OpeningUnlinked) {
			return nil, apperr.Validation("payment exceeds the old debt; use Current credit for the rest")
		}
		if err := r.PayOpening(ctx, id, amt); err != nil {
			return nil, apperr.Internal("failed to record payment", err)
		}
	} else if err := r.AddBalance(ctx, id, amt.Neg()); err != nil {
		return nil, apperr.Internal("failed to record payment", err)
	}
```

(Leave the `customer_payments` INSERT and `PaymentResult` exactly as-is — `balance_before/after` still come from `outstanding_balance`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `DATABASE_URL="$DATABASE_URL" go test ./internal/features/customers/ -run 'TestPayOpening|TestRecordPaymentTargetsOpening' -v`
Expected: PASS (both).

- [ ] **Step 5: Commit**

```bash
git add internal/features/customers/customers.go internal/features/customers/opening_pay_test.go
git commit -m "feat(customers): payment can target the old debt"
```

---

### Task 3: `suppliers.PayOpening` repo method

**Files:**
- Modify: `internal/features/suppliers/suppliers.go` (add near `AddBalance` ~line 174)
- Test: `internal/features/suppliers/opening_pay_test.go` (create)

**Interfaces:**
- Produces: `func (r *Repository) PayOpening(ctx context.Context, id int64, amt decimal.Decimal) error`

- [ ] **Step 1: Write the failing test**

```go
package suppliers

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	appdb "karots-pos/internal/db"

	"github.com/shopspring/decimal"
)

// TestSupplierPayOpening: paying the old debt down drops outstanding +
// opening_unlinked together, leaves linked alone, never goes below zero.
func TestSupplierPayOpening(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	conn, err := appdb.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := context.Background()
	svc := NewService(conn)
	repo := NewRepository(conn)

	name := fmt.Sprintf("sup-payopening-%d", time.Now().UnixNano())
	sup, err := svc.Create(ctx, CreateInput{Name: name, OpeningBalance: "10000"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `DELETE FROM suppliers WHERE id = $1`, sup.ID)

	if err := repo.AddBalance(ctx, sup.ID, decimal.RequireFromString("5000")); err != nil {
		t.Fatal(err)
	} // outstanding 15000, opening 10000, linked 5000

	if err := repo.PayOpening(ctx, sup.ID, decimal.RequireFromString("4000")); err != nil {
		t.Fatal(err)
	}
	s, _ := svc.Get(ctx, sup.ID)
	if got := s.OpeningUnlinked.StringFixed(2); got != "6000.00" {
		t.Fatalf("opening = %s, want 6000.00", got)
	}
	if got := s.LinkedBalance().StringFixed(2); got != "5000.00" {
		t.Fatalf("linked = %s, want 5000.00", got)
	}

	if err := repo.PayOpening(ctx, sup.ID, decimal.RequireFromString("99999")); err != nil {
		t.Fatal(err)
	}
	s, _ = svc.Get(ctx, sup.ID)
	if got := s.OpeningUnlinked.StringFixed(2); got != "0.00" {
		t.Fatalf("opening after overpay = %s, want 0.00", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `DATABASE_URL="$DATABASE_URL" go test ./internal/features/suppliers/ -run TestSupplierPayOpening -v`
Expected: FAIL — `repo.PayOpening undefined`.

- [ ] **Step 3: Write minimal implementation**

Add after `AddBalance` in `suppliers.go`:

```go
// PayOpening reduces the old (opening) debt directly: outstanding_balance and
// opening_unlinked each drop by amt (clamped at zero), leaving the gross
// opening_balance and the linked part untouched. Callers guard against
// overpaying; the clamp here is a safety net.
func (r *Repository) PayOpening(ctx context.Context, id int64, amt decimal.Decimal) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE suppliers SET
			outstanding_balance = GREATEST(outstanding_balance - $1, 0),
			opening_unlinked    = GREATEST(opening_unlinked    - $1, 0)
		WHERE id = $2`, amt, id)
	return err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `DATABASE_URL="$DATABASE_URL" go test ./internal/features/suppliers/ -run TestSupplierPayOpening -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/features/suppliers/suppliers.go internal/features/suppliers/opening_pay_test.go
git commit -m "feat(suppliers): PayOpening reduces old debt directly"
```

---

### Task 4: Supplier payment splits an old-debt portion (`PayInput.Opening`)

**Files:**
- Modify: `internal/features/supplierpay/supplierpay.go` — `PayInput` (~line 50), `validatePay` (~line 142), `PayTx` (~line 188)
- Test: `internal/features/supplierpay/opening_pay_test.go` (create)

**Interfaces:**
- Consumes: `suppliers.Repository.PayOpening` (Task 3); `suppliers.Repository.FindByID`.
- Produces: `PayInput.Opening decimal.Decimal`; `PayTx` applies it via `PayOpening`, rejects it above the supplier's `opening_unlinked`, and includes it in the recorded total.

- [ ] **Step 1: Write the failing test**

```go
package supplierpay

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/suppliers"

	"github.com/shopspring/decimal"
)

// TestPayOpeningPortion: a payment carrying an Opening amount reduces the
// supplier's old debt (not linked-first) and records the full total; overpaying
// the opening is rejected.
func TestPayOpeningPortion(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	conn, err := appdb.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := context.Background()
	supSvc := suppliers.NewService(conn)
	supRepo := suppliers.NewRepository(conn)
	paySvc := NewService(conn)

	name := fmt.Sprintf("sp-open-%d", time.Now().UnixNano())
	sup, err := supSvc.Create(ctx, suppliers.CreateInput{Name: name, OpeningBalance: "8000"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `DELETE FROM supplier_payments WHERE supplier_id = $1`, sup.ID)
	defer conn.ExecContext(ctx, `DELETE FROM suppliers WHERE id = $1`, sup.ID)
	if err := supRepo.AddBalance(ctx, sup.ID, decimal.RequireFromString("2000")); err != nil {
		t.Fatal(err)
	} // outstanding 10000, opening 8000, linked 2000

	// Pay 3,000 against the old debt only (no invoice allocations).
	if _, err := paySvc.Pay(ctx, sup.ID, PayInput{Method: "cash", Opening: decimal.RequireFromString("3000")}, 0); err != nil {
		t.Fatal(err)
	}
	s, _ := supSvc.Get(ctx, sup.ID)
	if got := s.OpeningUnlinked.StringFixed(2); got != "5000.00" {
		t.Fatalf("opening = %s, want 5000.00", got)
	}
	if got := s.LinkedBalance().StringFixed(2); got != "2000.00" {
		t.Fatalf("linked = %s, want 2000.00", got)
	}

	// Overpaying the old debt (5,000 left) is rejected.
	if _, err := paySvc.Pay(ctx, sup.ID, PayInput{Method: "cash", Opening: decimal.RequireFromString("6000")}, 0); err == nil {
		t.Fatal("expected rejection paying more than the old debt")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `DATABASE_URL="$DATABASE_URL" go test ./internal/features/supplierpay/ -run TestPayOpeningPortion -v`
Expected: FAIL — `unknown field Opening`.

- [ ] **Step 3: Write minimal implementation**

Add `Opening` to `PayInput`:

```go
type PayInput struct {
	Method      string
	Reference   string
	Note        string
	Allocations []Alloc
	Unallocated decimal.Decimal
	// Opening pays down the supplier's old (pre-system) debt directly, separate
	// from invoice allocations and the unallocated advance. Admin-only.
	Opening decimal.Decimal
}
```

In `validatePay`, include it in the total (after the allocations loop, before the positivity check):

```go
	if in.Opening.IsNegative() {
		return "", decimal.Zero, apperr.Validation("old-debt amount must not be negative")
	}
	total = total.Add(in.Opening)
```

In `PayTx`, after loading `sup` and before `supRepo.AddBalance`, split the opening portion off. Replace the final balance drop:

```go
	// Old-debt portion pays the opening down directly (not linked-first).
	if in.Opening.IsPositive() {
		if in.Opening.GreaterThan(sup.OpeningUnlinked) {
			return nil, apperr.Validation("payment exceeds the old debt; use the invoice or advance fields for the rest")
		}
		if err := supRepo.PayOpening(ctx, sup.ID, in.Opening); err != nil {
			return nil, apperr.Internal("failed to pay old debt", err)
		}
	}
	// Everything else (invoice allocations + advance) drops the aggregate,
	// settling the linked part first as before.
	if rest := total.Sub(in.Opening); rest.IsPositive() {
		if err := supRepo.AddBalance(ctx, sup.ID, rest.Neg()); err != nil {
			return nil, apperr.Internal("failed to update supplier balance", err)
		}
	}
```

(Delete the old unconditional `supRepo.AddBalance(ctx, sup.ID, total.Neg())` line.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `DATABASE_URL="$DATABASE_URL" go test ./internal/features/supplierpay/ -v`
Expected: PASS (new test + existing `supplierpay_test.go`/`settle_test.go`).

- [ ] **Step 5: Commit**

```bash
git add internal/features/supplierpay/supplierpay.go internal/features/supplierpay/opening_pay_test.go
git commit -m "feat(supplierpay): pay old debt as a distinct portion"
```

---

### Task 5: Admin supplier pay form — old-debt line

**Files:**
- Modify: `internal/web/supplier_pay_shared.go` — `parseAllocations` (~line 113)
- Modify: `templates/pages/admin/suppliers.templ` — `SupplierPaymentForm` templ + its `SupplierPayData` struct
- Modify: `internal/web/admin_more.go` — `SupplierPayForm` passes `opening_unlinked` (already loads the supplier)

**Interfaces:**
- Consumes: `supplierpay.PayInput.Opening` (Task 4).
- Produces: admin supplier pay POST reads `pay_opening` into `PayInput.Opening`; the form renders an old-debt line when `opening_unlinked > 0`.

- [ ] **Step 1: Read `pay_opening` in `parseAllocations`**

In `supplier_pay_shared.go`, after the `advance`/`amount` block sets `in.Unallocated`, add:

```go
	if raw := strings.TrimSpace(c.FormValue("pay_opening")); raw != "" {
		amt, err := money.Parse(raw)
		if err != nil || amt.IsNegative() {
			return in, apperr.Validation("invalid old-debt amount")
		}
		in.Opening = amt
	}
```

Because the **cashier** form never renders `pay_opening`, its `PayInput.Opening` stays zero — no cashier behaviour change.

- [ ] **Step 2: Pass the opening figure to the admin form**

In `admin_more.go` `SupplierPayForm`, the supplier `s` is already loaded. Add its opening to the `SupplierPayData` struct (add an `Opening decimal.Decimal` field to `SupplierPayData` in the templ package, set `Opening: s.OpeningUnlinked`). The `Symbol` is already available.

- [ ] **Step 3: Render the old-debt line in the admin supplier pay form**

In the `SupplierPaymentForm` templ, above the invoice-allocation rows, add (admin form only):

```templ
if d.Opening.IsPositive() {
	<div class="flex items-center justify-between gap-2 py-1 border-b border-slate-200">
		<label for="pay_opening">Old debt (before system): { d.Symbol }{ money.Display(d.Opening) }</label>
		<input type="number" step="0.01" min="0" max={ d.Opening.String() }
			id="pay_opening" name="pay_opening" placeholder="0.00"
			class="w-28 rounded border border-slate-300 px-2 py-1 text-right"/>
	</div>
}
```

- [ ] **Step 4: Generate + build**

Run: `templ generate && go build ./...`
Expected: builds clean.

- [ ] **Step 5: Manual verification (running server + emulator)**

1. Give a test supplier an opening balance (Suppliers → row → adjust opening) and a credit purchase.
2. Admin → Suppliers → Pay: confirm the "Old debt (before system)" line appears; pay part of it; confirm the supplier's opening drops by exactly that, linked unchanged, a `CR-` receipt printed, and the cash source reduced.
3. **Cashier regression:** open the till supplier-pay dialog — confirm NO old-debt line and paying still allocates as before.

- [ ] **Step 6: Commit**

```bash
git add internal/web/supplier_pay_shared.go internal/web/admin_more.go templates/pages/admin/
git commit -m "feat(web): admin supplier pay can target old debt"
```

---

### Task 6: Admin customer pay form — Current vs Old debt target

**Files:**
- Modify: `internal/web/admin_more.go` — `CustomerPayForm` passes opening; `CustomerPay` already binds `ApplyToOpening` via `c.Bind`
- Modify: `templates/pages/admin/customers.templ` — `CustomerPaymentForm` templ + its `CustomerPayData` struct

**Interfaces:**
- Consumes: `customers.PaymentInput.ApplyToOpening` (Task 2).
- Produces: admin customer pay form renders a Current/Old-debt choice when `opening_unlinked > 0`; the POST binds `apply_to_opening`.

- [ ] **Step 1: Pass the opening figure to the form**

In `admin_more.go` `CustomerPayForm`, `cust` is already loaded. Add `Opening decimal.Decimal` to `CustomerPayData` and set `Opening: cust.OpeningUnlinked`.

- [ ] **Step 2: Render the target choice**

In the `CustomerPaymentForm` templ, add (only when there is an old debt):

```templ
if d.Opening.IsPositive() {
	<fieldset class="flex gap-4 text-sm py-1">
		<label class="flex items-center gap-1">
			<input type="radio" name="apply_to_opening" value="false" checked/> Current credit
		</label>
		<label class="flex items-center gap-1">
			<input type="radio" name="apply_to_opening" value="true"/> Old debt ({ d.Symbol }{ money.Display(d.Opening) })
		</label>
	</fieldset>
}
```

The `bool` form-binds from `"true"`/`"false"`. When there is no old debt the field is absent → `ApplyToOpening` is false (today's behaviour).

- [ ] **Step 3: Generate + build**

Run: `templ generate && go build ./...`
Expected: builds clean.

- [ ] **Step 4: Manual verification**

1. Give a test customer an opening balance + a credit sale.
2. Admin → Customers → Pay: confirm the Current/Old radio appears; pay against Old debt; confirm opening drops, linked unchanged, a `DP-` receipt printed, cash landed in the chosen locker.
3. Try paying more than the old debt with Old selected → expect the validation error.
4. **Cashier regression:** the till credit-collection dialog shows no target choice and pays the whole as before.

- [ ] **Step 5: Commit**

```bash
git add internal/web/admin_more.go templates/pages/admin/
git commit -m "feat(web): admin customer pay can target old debt"
```

---

### Task 7: Opening-edit modal — Add amount / Set exact toggle

**Files:**
- Modify: `templates/pages/admin/components.templ` — `OpeningAdjustForm` + `openingXData` (~lines 33-61)

**Interfaces:**
- Consumes: nothing new — still POSTs `opening` (absolute) to the existing route; `AdjustOpening` is unchanged.
- Produces: the modal computes the posted `opening` from an Add/Set-exact toggle.

- [ ] **Step 1: Add the toggle + computed post value (Alpine)**

In `openingXData`, seed a `mode: 'add'` and an `entry: ''` (the amount the user types) alongside the existing `opening`/current state. Add a hidden input whose value is the computed absolute new opening:

```templ
<div class="flex gap-4 text-sm">
	<label class="flex items-center gap-1"><input type="radio" x-model="mode" value="add"/> Add amount</label>
	<label class="flex items-center gap-1"><input type="radio" x-model="mode" value="set"/> Set exact total</label>
</div>
<input type="number" step="0.01" x-model="entry" placeholder="0.00"
	class="w-full rounded border border-slate-300 px-2 py-1"/>
<input type="hidden" name="opening"
	:value="mode === 'add' ? (Number(current) + Number(entry || 0)).toFixed(2) : (Number(entry || 0)).toFixed(2)"/>
<p class="text-sm text-slate-600">New old debt:
	<span x-text="(mode === 'add' ? (Number(current) + Number(entry||0)) : Number(entry||0)).toFixed(2)"></span>
</p>
```

where `current` is the Alpine-seeded current `opening_unlinked` (from `d.Opening`). Remove/replace the old single absolute `name="opening"` input so only the hidden computed field posts it. Keep the existing "New total outstanding" preview if present, retargeting it to the computed value.

- [ ] **Step 2: Generate + build**

Run: `templ generate && go build ./...`
Expected: builds clean.

- [ ] **Step 3: Manual verification**

1. Suppliers (and Customers) → adjust opening modal.
2. **Add amount:** current old debt 5,000, add 2,000 → after save the old debt (and outstanding) rose by 2,000; linked unchanged.
3. **Set exact:** set 1,000 → old debt becomes 1,000; outstanding shifted by the delta; linked unchanged.
4. Confirm the audit log still records before→after.

- [ ] **Step 4: Commit**

```bash
git add templates/pages/admin/components.templ templates/pages/admin/components_templ.go
git commit -m "feat(web): add-amount vs set-exact toggle for old debt editing"
```

---

### Task 8: Full-suite green + vet

**Files:** none (verification task)

- [ ] **Step 1: Vet + build**

Run: `go vet ./... && go build ./...`
Expected: clean.

- [ ] **Step 2: Full test suite**

Run: `DATABASE_URL="$DATABASE_URL" go test ./...`
Expected: PASS (or pre-existing skips only). Confirm `customers`, `suppliers`, `supplierpay`, and `web` supplier/customer money tests all pass.

- [ ] **Step 3: Final commit (if vet/format produced changes)**

```bash
git add -A
git commit -m "chore: vet + generated output for old-debt payments" || echo "nothing to commit"
```

---

## Self-Review notes

- **Spec coverage:** Pay-down (Tasks 1-6), Add amount + Set exact (Task 7), overpay rejection (Tasks 2, 4), no-migration (all), cashier-frozen (Tasks 5/6 regression steps), gross `opening_balance` untouched on payment (Tasks 1/3 SQL). Covered.
- **Type consistency:** `PayOpening(ctx, id int64, amt decimal.Decimal) error` identical in both repos; `PayInput.Opening` and `PaymentInput.ApplyToOpening` referenced consistently across web tasks.
- **Templ field names** (`SupplierPayData.Opening`, `CustomerPayData.Opening`) are added in Tasks 5/6 where first used, in `templates/pages/admin/suppliers.templ` and `customers.templ` respectively; `OpeningAdjustForm` (Task 7) is in `components.templ`. The cashier form `templates/pages/cashier/suppliers.templ` is deliberately left alone.
