# Cashier reset button + consumable found-at-till — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a one-press "Reset to menu" (clear search, jump to top-level menu, focus scanner) with an F1 shortcut, and extend found-at-till so a service line's short consumable (copy-job paper) can be sold with a confirm instead of being blocked.

**Architecture:** Part A is pure front-end — a `reset()` method on the POS Alpine component, a toolbar button, and an F1 key case; the cart is never touched. Part B mirrors the existing product found-at-till onto the *consumable* branch of the sale: the server honours `AllowOversell` when a component count is short (correcting it up via `FoundAtTill`), tagging the refusal with a machine-readable code so the till can react with a confirm prompt and retry.

**Tech Stack:** Go 1.x + Echo + sqlx (server), templ + Alpine.js (front-end), PostgreSQL 17. Single `CGO_ENABLED=0` binary with `go:embed` — static assets are embedded, so front-end changes need `make build` + restart to appear.

## Global Constraints

- **Assets are embedded:** after any change to `static/js/app.js` or a `.templ` file, run `make build` (regenerates templ + rebuilds the binary) and restart the server before it takes effect. Run `make css` only if new Tailwind utility classes were introduced.
- **Never commit `static/css/tailwind.css`** (generated). Leave it out of commits.
- **Commit to `main`** (owner's working style); no feature branch unless asked.
- **Dev DB is disposable:** `pos_db` via `DATABASE_URL` in `.env`; DB-guarded tests seed + delete their own rows. Restore to baseline after live E2E.
- **Found-at-till is confirm-gated, not flag-gated** — same philosophy as the shipped product path: the human overrides an imperfect count via a one-time OK, no per-user permission.
- **On-hand must never go negative** — `FoundAtTill` corrects the count *up* to cover the line, then the guarded decrement lands it at 0.
- Commit-message trailer: `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`.

---

## File Structure

- `static/js/app.js` — POS Alpine component: new `reset()` (Part A) + F1 case in `onKey()` (Part A); `apiFetch` gains `err.code`; `checkout` posts silently and reacts to `CONSUMABLE_SHORT`; new `consumablePrompt` state + `approveConsumable()`/`cancelConsumable()`; `newSale()` clears it (Part B client).
- `templates/pages/cashier/pos.templ` — toolbar "⟲ Menu (F1)" button (Part A); consumable-short confirm modal mirroring the oversell modal (Part B client).
- `internal/apperr/errors.go` — `ConflictCode(code, msg)` helper (Part B server).
- `internal/features/sales/service.go` — component-consume loop honours `AllowOversell` (Part B server).
- `internal/features/sales/consumable_oversell_db_test.go` — new DB-guarded tests (Part B server).

---

### Task 1: Cashier "Reset to menu" button + F1 shortcut (Part A)

Pure front-end, no server change. The repo has **no automated JS/Alpine test harness**, so this task is verified by `make build` + live check, not a unit test. That is expected — do not scaffold a JS test runner.

**Files:**
- Modify: `static/js/app.js` (add `reset()` method; add `case "F1"` in `onKey()`)
- Modify: `templates/pages/cashier/pos.templ` (toolbar button, ~line 105 area beside "+ Quick item")

**Interfaces:**
- Consumes: existing `loadGroupsTop()`, `focusEl(ref)`, `onKey(e)`, and state `search`, `menuMode`, `amountNode`, `detailHtml` on the POS Alpine component.
- Produces: `reset()` method (no args, no return) usable from templ (`x-on:click="reset()"`) and from `onKey`.

- [ ] **Step 1: Add `reset()` to the POS Alpine component**

In `static/js/app.js`, add this method next to `backGroup()` (~line 680):

```javascript
    // Reset the catalog back to the top menu and refocus the scanner — a
    // one-press "start over" for browsing. NAVIGATION ONLY: the cart, customer,
    // payments, and discount are deliberately untouched (use newSale() for that),
    // so an accidental press never loses an in-progress sale.
    reset() {
      this.search = "";
      this.amountNode = null;
      this.detailHtml = "";
      this.menuMode = "cards";
      this.loadGroupsTop();
      this.focusEl("scanInput");
    },
```

- [ ] **Step 2: Add the F1 key case**

In `onKey(e)` (~line 507), add a case alongside the other F-keys (before the closing of the `switch`):

```javascript
        case "F1":
          e.preventDefault(); // stop the browser help panel opening
          this.reset();
          return;
```

Leave the `palette-open` guard at the top of `onKey` and every other case unchanged.

- [ ] **Step 3: Add the toolbar button**

In `templates/pages/cashier/pos.templ`, in the catalog toolbar `<div class="flex gap-3 mb-4 shrink-0">` (~line 85), add the button immediately after the "+ Quick item" button (~line 105–108), before the `for _, a := range plugin.PosActions()` loop:

```html
						<button
							type="button"
							x-on:click="reset()"
							title="Back to the top menu and focus the scanner (F1)"
							class="shrink-0 px-3 py-2.5 rounded-lg border border-slate-300 text-slate-700 font-medium text-sm hover:bg-slate-50"
						>⟲ Menu (F1)</button>
```

- [ ] **Step 4: Build**

Run: `make build`
Expected: templ regenerates and the binary compiles with no errors.

- [ ] **Step 5: Live verify (manual)**

Restart the server, open the cashier till, then:
- Drill into a product group, then press **F1** → lands at the top menu, search box empty, cursor in the scan field.
- Type in Search to show results, click **⟲ Menu (F1)** → results cleared, top menu shown, scanner focused.
- Enter a plugin amount/detail step (e.g. Reload & Bills), press **F1** → returns to top menu.
- Confirm F1 does **not** open the browser help panel.
- Add an item to the cart, then press F1 → **cart is unchanged**.

- [ ] **Step 6: Commit**

```bash
git add static/js/app.js templates/pages/cashier/pos.templ templates/pages/cashier/pos_templ.go
git commit -m "feat(till): Reset to menu button + F1 (clear search, top menu, focus scanner)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: Server — found-at-till for service-line consumables (Part B server)

TDD with DB-guarded tests mirroring `internal/features/sales/oversell_db_test.go`. Requires `DATABASE_URL` (in `.env`); the tests skip if unset.

**Files:**
- Modify: `internal/apperr/errors.go` (add `ConflictCode`)
- Modify: `internal/features/sales/service.go` (component loop ~line 351–376)
- Create: `internal/features/sales/consumable_oversell_db_test.go`

**Interfaces:**
- Consumes: `stkRepo.DecrementGuarded(ctx, productID, qty) (bool, error)`, `stkRepo.GetQuantity(ctx, productID) (decimal.Decimal, error)`, `stkRepo.ProductCost(ctx, productID) (decimal.Decimal, error)`, `stkRepo.FoundAtTill(ctx, productID, batchID int64, qty, cost decimal.Decimal, userID int64) error`, `stkRepo.DepleteFEFO(...)`; local vars in `Service.Create`: `it` (`ItemInput`, has `AllowOversell`), `p` (product), `cashierID`.
- Produces: `apperr.ConflictCode(code, msg string) *apperr.AppError`; the error code string `"CONSUMABLE_SHORT"` (consumed by Task 3's client).

- [ ] **Step 1: Add the `ConflictCode` helper (write it, it is trivial and used by the test)**

In `internal/apperr/errors.go`, add below `Conflict`:

```go
// ConflictCode is Conflict with a caller-chosen machine-readable code, so a
// client can react to a specific 409 without string-matching the message.
func ConflictCode(code, msg string) *AppError {
	return &AppError{Code: code, Message: msg, Status: 409}
}
```

- [ ] **Step 2: Write the failing DB-guarded tests**

Create `internal/features/sales/consumable_oversell_db_test.go`. It seeds a **service** product plus a zero-stock **consumable** product, sells the service with the consumable as a component, and asserts the two behaviours (approve corrects up + sells; no-approval refuses with the coded conflict). Reuses the `salesTestDB` / `mustSales` helpers from `oversell_db_test.go` (same package).

```go
package sales

import (
	"context"
	"strings"
	"testing"

	"karots-pos/internal/apperr"

	"github.com/shopspring/decimal"
)

// Proves the found-at-till path for a SERVICE line's consumable (the copy-job
// paper case): the service itself holds no stock, but its component (paper) is
// short. Service.Create commits its own tx, so this seeds throwaway rows and
// deletes them afterwards. The dev DB is disposable.

func seedServiceAndConsumable(t *testing.T, conn interface {
	GetContext(context.Context, interface{}, string, ...interface{}) error
}) {
	t.Helper()
}

func TestConsumableFoundAtTillCorrectsUpAndSells(t *testing.T) {
	conn := salesTestDB(t)
	defer conn.Close()
	ctx := context.Background()

	var categoryID, unitID, cashierID, sinceSaleID, serviceID, paperID int64
	mustSales(t, conn.GetContext(ctx, &categoryID, `SELECT id FROM categories LIMIT 1`))
	mustSales(t, conn.GetContext(ctx, &unitID, `SELECT id FROM units LIMIT 1`))
	mustSales(t, conn.GetContext(ctx, &cashierID, `SELECT id FROM users LIMIT 1`))
	mustSales(t, conn.GetContext(ctx, &sinceSaleID, `SELECT COALESCE(MAX(id),0) FROM sales`))
	mustSales(t, conn.GetContext(ctx, &serviceID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price, is_service)
		VALUES ('TEST copy service', $1, $2, 0, 5, true) RETURNING id`, categoryID, unitID))
	mustSales(t, conn.GetContext(ctx, &paperID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price)
		VALUES ('TEST paper', $1, $2, 2, 0) RETURNING id`, categoryID, unitID))

	defer func() {
		conn.ExecContext(ctx, `DELETE FROM sales WHERE id > $1`, sinceSaleID)                              //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM stock_movements WHERE product_id IN ($1,$2)`, serviceID, paperID) //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM stock_batches WHERE product_id IN ($1,$2)`, serviceID, paperID)   //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM stock WHERE product_id IN ($1,$2)`, serviceID, paperID)           //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM products WHERE id IN ($1,$2)`, serviceID, paperID)                //nolint:errcheck
	}()

	svc := NewService(conn)

	// Paper is at 0 on hand; approve the oversell on the service line.
	_, err := svc.Create(ctx, CreateInput{
		SaleType: "retail",
		Items: []ItemInput{{
			ProductID:     serviceID,
			Quantity:      "1",
			PriceOverride: "5",
			AllowOversell: true,
			Components:    []ServiceComponent{{ProductID: paperID, Quantity: "1"}},
		}},
		Payments: []PaymentInput{{Method: "cash", Amount: "5"}},
	}, cashierID)
	mustSales(t, err)

	var paperOnHand decimal.Decimal
	mustSales(t, conn.GetContext(ctx, &paperOnHand, `SELECT quantity FROM stock WHERE product_id = $1`, paperID))
	if !paperOnHand.Equal(decimal.Zero) {
		t.Errorf("paper on hand = %s, want 0 (found +1 then consumed -1)", paperOnHand)
	}

	var adjustCount int
	mustSales(t, conn.GetContext(ctx, &adjustCount, `SELECT count(*) FROM stock_movements WHERE product_id = $1 AND type = 'adjust' AND quantity > 0`, paperID))
	if adjustCount != 1 {
		t.Errorf("paper found (+adjust) movements = %d, want 1", adjustCount)
	}
}

func TestConsumableWithoutApprovalRefusesWithCode(t *testing.T) {
	conn := salesTestDB(t)
	defer conn.Close()
	ctx := context.Background()

	var categoryID, unitID, cashierID, serviceID, paperID int64
	mustSales(t, conn.GetContext(ctx, &categoryID, `SELECT id FROM categories LIMIT 1`))
	mustSales(t, conn.GetContext(ctx, &unitID, `SELECT id FROM units LIMIT 1`))
	mustSales(t, conn.GetContext(ctx, &cashierID, `SELECT id FROM users LIMIT 1`))
	mustSales(t, conn.GetContext(ctx, &serviceID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price, is_service)
		VALUES ('TEST copy service blocked', $1, $2, 0, 5, true) RETURNING id`, categoryID, unitID))
	mustSales(t, conn.GetContext(ctx, &paperID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price)
		VALUES ('TEST paper blocked', $1, $2, 2, 0) RETURNING id`, categoryID, unitID))
	defer func() {
		conn.ExecContext(ctx, `DELETE FROM stock_movements WHERE product_id IN ($1,$2)`, serviceID, paperID) //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM stock_batches WHERE product_id IN ($1,$2)`, serviceID, paperID)   //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM stock WHERE product_id IN ($1,$2)`, serviceID, paperID)           //nolint:errcheck
		conn.ExecContext(ctx, `DELETE FROM products WHERE id IN ($1,$2)`, serviceID, paperID)                //nolint:errcheck
	}()

	svc := NewService(conn)
	_, err := svc.Create(ctx, CreateInput{
		SaleType: "retail",
		Items: []ItemInput{{
			ProductID:     serviceID,
			Quantity:      "1",
			PriceOverride: "5",
			AllowOversell: false,
			Components:    []ServiceComponent{{ProductID: paperID, Quantity: "1"}},
		}},
		Payments: []PaymentInput{{Method: "cash", Amount: "5"}},
	}, cashierID)
	if err == nil {
		t.Fatal("a short consumable without approval should be refused")
	}
	ae, ok := apperr.As(err)
	if !ok || ae.Code != "CONSUMABLE_SHORT" {
		t.Errorf("error = %v, want an *AppError with code CONSUMABLE_SHORT", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "consumable") {
		t.Errorf("message = %q, want it to mention the consumable", err.Error())
	}
}
```

Note: delete the unused `seedServiceAndConsumable` stub before finishing — it is only here to show it is NOT needed; do not include it. (Keeping the file to the two test funcs above.)

- [ ] **Step 3: Run the tests to verify they fail**

Run: `set -x DATABASE_URL "postgres://pos_user:pos_password@localhost:5432/pos_db?sslmode=disable"; go test ./internal/features/sales/ -run 'Consumable' -v`
(fish shell — `set -x` exports for this command's scope.)
Expected: FAIL — `TestConsumableWithoutApprovalRefusesWithCode` fails because today's code returns a plain `Conflict` (code `CONFLICT`, not `CONSUMABLE_SHORT`); `TestConsumableFoundAtTillCorrectsUpAndSells` fails because the short consumable is refused even with `AllowOversell: true`.

- [ ] **Step 4: Implement — honour `AllowOversell` in the component loop**

In `internal/features/sales/service.go`, replace the `!ok` refusal inside the component loop (the block at ~line 359–365 that currently reads):

```go
					ok, err := stkRepo.DecrementGuarded(ctx, comp.ProductID, cq)
					if err != nil {
						return apperr.Internal("failed to update stock", err)
					}
					if !ok {
						return apperr.Conflict("insufficient stock for a consumable used by " + p.Name)
					}
```

with:

```go
					ok, err := stkRepo.DecrementGuarded(ctx, comp.ProductID, cq)
					if err != nil {
						return apperr.Internal("failed to update stock", err)
					}
					if !ok {
						// The consumable count is short. Without an override, refuse —
						// but with a machine-readable code so the till can offer a
						// found-at-till confirm (the paper is on the shelf; the count
						// was just wrong), mirroring the product path below.
						if !it.AllowOversell {
							return apperr.ConflictCode("CONSUMABLE_SHORT",
								"insufficient stock for a consumable used by "+p.Name)
						}
						onHand, err := stkRepo.GetQuantity(ctx, comp.ProductID)
						if err != nil {
							return apperr.Internal("failed to read stock", err)
						}
						compCost, err := stkRepo.ProductCost(ctx, comp.ProductID)
						if err != nil {
							return apperr.Internal("failed to read cost", err)
						}
						// Correct the count up to cover this line (batchID 0 — consumables
						// aren't lot-picked at the till), then the guarded decrement must
						// succeed, so on-hand lands at 0, never negative.
						if err := stkRepo.FoundAtTill(ctx, comp.ProductID, 0, cq.Sub(onHand), compCost, cashierID); err != nil {
							return apperr.Internal("failed to correct stock", err)
						}
						ok, err = stkRepo.DecrementGuarded(ctx, comp.ProductID, cq)
						if err != nil {
							return apperr.Internal("failed to update stock", err)
						}
						if !ok {
							return apperr.Internal("stock correction did not cover the consumable", nil)
						}
					}
```

The `DepleteFEFO(comp.ProductID, cq)` call immediately after is unchanged — it still books the true consumed cost as the line COGS.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `set -x DATABASE_URL "postgres://pos_user:pos_password@localhost:5432/pos_db?sslmode=disable"; go test ./internal/features/sales/ -run 'Consumable' -v`
Expected: PASS for both new tests.

- [ ] **Step 6: Full check**

Run: `go vet ./... ; and set -x DATABASE_URL "postgres://pos_user:pos_password@localhost:5432/pos_db?sslmode=disable"; and go test ./internal/features/sales/ ./internal/features/stock/ ./internal/apperr/`
Expected: no vet errors; existing sales/stock tests (including the product `Oversell` tests) still PASS — the `AllowOversell: false` path is unchanged except for the error code, and no existing test asserts that code.

- [ ] **Step 7: Commit**

```bash
git add internal/apperr/errors.go internal/features/sales/service.go internal/features/sales/consumable_oversell_db_test.go
git commit -m "feat(sales): found-at-till for service-line consumables (copy-job paper)

A short consumable now honours AllowOversell like a real product line:
correct the count up via FoundAtTill, then decrement. Without approval it
returns a CONSUMABLE_SHORT-coded conflict so the till can prompt.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: Client — reactive consumable-short confirm prompt (Part B client)

No automated JS test harness; verified by `make build` + live E2E. Depends on Task 2's `CONSUMABLE_SHORT` code.

**Files:**
- Modify: `static/js/app.js` (`apiFetch` attaches `err.code`; `checkout` posts silently + reacts; `consumablePrompt` state + `approveConsumable()`/`cancelConsumable()`; `newSale()` clears it)
- Modify: `templates/pages/cashier/pos.templ` (confirm modal mirroring the oversell modal)

**Interfaces:**
- Consumes: `apiFetch("POST", "/api/sales", payload, options)`; error code `"CONSUMABLE_SHORT"` from Task 2; existing `checkout`, `newSale`, `toast`, cart items carrying `components` and `allow_oversell`.
- Produces: `consumablePrompt` state, `approveConsumable()`, `cancelConsumable()` used by the templ modal.

- [ ] **Step 1: Attach the error code in `apiFetch`**

In `static/js/app.js`, in `apiFetch` where the error is built (~line 111), add the code line:

```javascript
    const err = new Error(msg);
    err.status = res.status;
    err.code = json && json.error && json.error.code;
    throw err;
```

- [ ] **Step 2: Add `consumablePrompt` state + approve/cancel handlers**

Next to the existing `oversellPrompt` block (~line 1606), add:

```javascript
    // --- consumable found-at-till (a job's material reads short) ---
    // Reactive, unlike the product oversell prompt: a service line carries MAX
    // stock and its consumables are hidden inside `components`, so a short paper
    // count is only knowable once the server tries. The sale POST comes back with
    // a CONSUMABLE_SHORT code; we confirm, then flag every service line that
    // carries components and retry.
    consumablePrompt: null,
    approveConsumable() {
      for (const it of this.cart) {
        if (Array.isArray(it.components) && it.components.length) it.allow_oversell = true;
      }
      this.consumablePrompt = null;
      this.checkout(false); // re-enter; tender re-validates, then posts with the flags set
    },
    cancelConsumable() {
      this.consumablePrompt = null;
    },
```

Re-entering with `checkout(false)` (rather than `true`) is deliberate: the consumable shortage surfaces *after* the tender checks already passed on the first attempt, so re-running them is harmless and keeps a single code path. Mirrors how `approveOversell()` re-enters `checkout`.

- [ ] **Step 3: Make the sale POST silent and react to the code**

In `checkout`, change the sale POST call (~line 1768) from:

```javascript
        const json = await apiFetch("POST", "/api/sales", payload);
```

to:

```javascript
        const json = await apiFetch("POST", "/api/sales", payload, { silent: true });
```

Then replace the existing `catch (_) { /* toast already shown */ }` (~line 1783) with a handler that shows the consumable prompt for the coded error and toasts everything else itself (now that the POST is silent):

```javascript
      } catch (e) {
        // A job's material read short. Offer a found-at-till confirm, but only
        // while some service-with-components line hasn't been approved yet — once
        // all are flagged, a repeat CONSUMABLE_SHORT is a real error, so fall
        // through to the toast and never loop.
        if (e && e.code === "CONSUMABLE_SHORT") {
          const pending = this.cart.some(
            (it) => Array.isArray(it.components) && it.components.length && !it.allow_oversell
          );
          if (pending) {
            this.consumablePrompt = true;
            return; // finally still runs (busy=false); prompt drives the retry
          }
        }
        toast((e && e.message) || "Sale failed", "error");
      } finally {
        this.busy = false;
      }
```

- [ ] **Step 4: Clear the prompt in `newSale()`**

In `newSale()` (~line 1799), add alongside `this.oversellPrompt = null;`:

```javascript
      this.consumablePrompt = null;
```

- [ ] **Step 5: Add the confirm modal**

In `templates/pages/cashier/pos.templ`, immediately after the `oversellPrompt` modal (`</div>` closing it, ~line 764), add:

```html
			<div x-show="consumablePrompt" x-cloak x-on:keydown.escape.window="cancelConsumable()" class="fixed inset-0 bg-black/40 flex items-center justify-center z-50 p-4">
				<div class="bg-white rounded-2xl shadow-xl w-full max-w-sm p-6 space-y-3">
					<h3 class="text-lg font-semibold text-amber-700">A material reads zero</h3>
					<p class="text-sm text-slate-600">
						The count shows none of a material this job needs (e.g. paper), but you have it on hand.
					</p>
					<p class="text-xs text-slate-500">Proceed? The stock count is corrected up to match, so on-hand ends at zero — not negative.</p>
					<div class="flex gap-2 pt-1">
						<button type="button" x-on:click="cancelConsumable()" class="flex-1 px-4 py-2.5 rounded-lg border font-semibold">Go back</button>
						<button type="button" x-on:click="approveConsumable()" class="flex-1 px-4 py-2.5 rounded-lg bg-amber-600 text-white font-semibold">Proceed</button>
					</div>
				</div>
			</div>
```

- [ ] **Step 6: Build**

Run: `make build`
Expected: templ regenerates, binary compiles.

- [ ] **Step 7: Live E2E verify**

Restart the server. Set a copy paper product's on-hand to 0 (e.g. via admin stock adjust, or SQL on the dev DB). Then at the till:
- Ring a Print & Copy job that uses that paper → checkout → the "A material reads zero" prompt appears.
- Click **Proceed** → sale completes; verify the paper's on-hand is **0** (not negative) and a positive `adjust` movement ("found at till …") was recorded; receipt is correct.
- Repeat, click **Go back** → no sale, cart intact.
- Regression: a job with paper **in stock** completes with **no** prompt; a normal product oversell still shows its own proactive prompt and works.

SQL helpers (dev DB):
```bash
docker exec pos_db psql -U pos_user -d pos_db -c "UPDATE stock SET quantity = 0 WHERE product_id = <paperID>;"
docker exec pos_db psql -U pos_user -d pos_db -c "SELECT quantity FROM stock WHERE product_id = <paperID>;"
```

- [ ] **Step 8: Commit**

```bash
git add static/js/app.js templates/pages/cashier/pos.templ templates/pages/cashier/pos_templ.go
git commit -m "feat(till): confirm + retry when a copy job's consumable reads zero

Reacts to the server's CONSUMABLE_SHORT: prompt once, flag the service lines,
retry. Confirm-gated, cart preserved.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

- [ ] **Step 9: Restore dev DB baseline**

Undo any manual stock edits made for the E2E (or note the found-at-till already corrected the paper back up to 0). Delete stray TEST rows if any remain.

---

## Self-Review

**Spec coverage:**
- Part A reset (menu+search+focus, keep cart) → Task 1 ✓
- Part A F1 shortcut with preventDefault → Task 1 Step 2 ✓
- Part A visible button → Task 1 Step 3 ✓
- Part B server: honour AllowOversell on components, FoundAtTill up, retry → Task 2 Step 4 ✓
- Part B server: distinct error code (not string-match) → `ConflictCode("CONSUMABLE_SHORT", …)` Task 2 ✓
- Part B server: COGS unchanged via DepleteFEFO → unchanged, noted Task 2 Step 4 ✓
- Part B client: reactive prompt on the code, flag service-with-components lines, retry once, loop-safe → Task 3 Steps 2–3 ✓
- Part B client: newSale clears prompt → Task 3 Step 4 ✓
- Confirm-gated not flag-gated → no per-user flag anywhere ✓
- Verification items (build/vet/test, live E2E, regression, restore DB) → covered across tasks ✓

**Placeholder scan:** No TBD/TODO/"handle edge cases" left. The only stub (`seedServiceAndConsumable`) is explicitly flagged for deletion in Task 2 Step 2. Fixed the `_consumableConfirmed` wobble in Task 3 Step 2 by settling on `this.checkout(false)`.

**Type consistency:** `reset()`, `consumablePrompt`, `approveConsumable()`, `cancelConsumable()` named identically across app.js and pos.templ. `ConflictCode(code, msg)` and code string `"CONSUMABLE_SHORT"` consistent between Task 2 (produce) and Task 3 (consume). Stock repo signatures (`GetQuantity`, `ProductCost`, `FoundAtTill`, `DecrementGuarded`) match the source.
