# Till Counter Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let cashiers complete three sales the system currently blocks — an item the count shows short (correct the count up and sell), a credit sale over the limit (override or raise the limit, behind a per-user flag), and stop duplicate credit customers (phone required + reuse on match).

**Architecture:** Three independent features sharing one new per-user permission flag. Section 1 (found-at-till) is confirm-gated only. Sections 2 (credit override + inline limit edit) are gated by a new `can_manage_credit` user flag that mirrors the existing `can_handle_suppliers` wiring exactly. Section 3 (dedup) tightens `customers.Service.Create`. Server enforces every trust boundary; the till UI only shows/sends what the server already permits.

**Tech Stack:** Go + Echo + sqlx + Goose migrations + templ + Alpine.js/HTMX + Tailwind, PostgreSQL. Single `CGO_ENABLED=0` binary with `go:embed` (so `static/css/tailwind.css` must be regenerated via `make css` and committed when template classes change).

## Global Constraints

- Commits go directly to `main`; trailer `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`; never commit `.claude/settings.local.json`. The user pushes to origin (sandbox has no GitHub creds).
- After any change to template classes, run `make css` and commit `static/css/tailwind.css`.
- Run `make build` (regenerates templ + css) before `go test ./...` and `go vet ./...`.
- DB-guarded tests skip when `DATABASE_URL` is unset and roll their tx back (mirror `internal/web/supplier_money_test.go` / `internal/features/stock/batch_price_test.go`).
- The new flag column name is **`can_manage_credit`** (DB), **`CanManageCredit`** (Go), everywhere — do not vary it.
- Stock on-hand must **never** go negative in normal selling; the found-at-till path corrects the count up so it lands at exactly 0.
- The client never sends a price for stocked goods; the server derives it. The over-limit flag from the client is advisory only and re-checked server-side.
- All work + E2E on the disposable dev `pos_db`.

---

### Task 1: Add the `can_manage_credit` per-user flag (migration + full plumbing)

Adds the flag end-to-end (DB → middleware → admin Users form) with no behaviour change yet. Mirrors `can_handle_suppliers` exactly.

**Files:**
- Create: `migrations/0057_manage_credit_at_till.sql`
- Modify: `internal/middleware/flags.go` (add field + getters + rule)
- Modify: `internal/middleware/auth.go:90` (set the ctx value)
- Modify: `internal/web/web.go:127-137` (load the column in userValidator)
- Modify: `internal/features/auth/model.go` (User + CreateUserInput + UpdateUserInput)
- Modify: `internal/features/auth/service.go:161,210` (pass through CreateUser/UpdateUser)
- Modify: `internal/features/auth/repository.go:55-72` (INSERT/UPDATE columns)
- Modify: `templates/pages/admin/users.templ:104-108` (checkbox)
- Test: `internal/middleware/flags_test.go` (extend)

**Interfaces:**
- Produces: `middleware.CanManageCredit(c echo.Context) bool`, `middleware.CanManageCreditCtx(ctx context.Context) bool`, `middleware.MayManageCredit(role string, flag bool) bool`, `middleware.UserFlags{ CanHandleSuppliers, CanManageCredit bool }`, `auth.User.CanManageCredit bool`.

- [ ] **Step 1: Write the migration**

Create `migrations/0057_manage_credit_at_till.sql`:
```sql
-- +goose Up
-- can_manage_credit marks a cashier trusted to override a customer's credit
-- limit for a single sale and to raise a customer's stored credit limit from the
-- till. Off by default; admins and managers may always do so regardless.
ALTER TABLE users ADD COLUMN can_manage_credit boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE users DROP COLUMN can_manage_credit;
```

- [ ] **Step 2: Verify the migration round-trips**

Run: `goose -dir migrations postgres "$DATABASE_URL" up && goose -dir migrations postgres "$DATABASE_URL" down && goose -dir migrations postgres "$DATABASE_URL" up`
(Or the project's `make migrate` equivalent — check the Makefile.)
Expected: clean up → down → up, no errors.

- [ ] **Step 3: Extend `UserFlags` and add getters/rule in `flags.go`**

In `internal/middleware/flags.go`, add to the `UserFlags` struct (after `CanHandleSuppliers`):
```go
	// CanManageCredit lets a cashier override a customer's credit limit for one
	// sale and raise a stored credit limit from the till. Meaningless for admins
	// and managers, who may always do so.
	CanManageCredit bool
```
Add the const beside `ctxCanSuppliers`:
```go
const ctxCanManageCredit = "can_manage_credit"
```
Add the three functions (mirror `CanHandleSuppliers*`/`MaySeeSuppliers`):
```go
// CanManageCredit reports the raw per-user flag for the current request.
func CanManageCredit(c echo.Context) bool {
	b, _ := c.Get(ctxCanManageCredit).(bool)
	return b
}

// CanManageCreditCtx is CanManageCredit for a bare context, for templates.
func CanManageCreditCtx(ctx context.Context) bool {
	f, _ := ctx.Value(ctxFlagsKey).(UserFlags)
	return f.CanManageCredit
}

// MayManageCredit is the full rule: admins and managers always may; a cashier
// may only with the flag.
func MayManageCredit(role string, flag bool) bool {
	return role == "admin" || role == "manager" || flag
}
```

- [ ] **Step 4: Set the ctx value in `auth.go`**

In `internal/middleware/auth.go`, after line 90 (`c.Set(ctxCanSuppliers, flags.CanHandleSuppliers)`):
```go
			c.Set(ctxCanManageCredit, flags.CanManageCredit)
```

- [ ] **Step 5: Load the column in `web.go` userValidator**

In `internal/web/web.go:128-136`, extend the row struct and SELECT:
```go
		var row struct {
			Active             bool `db:"is_active"`
			CanHandleSuppliers bool `db:"can_handle_suppliers"`
			CanManageCredit    bool `db:"can_manage_credit"`
		}
		if err := db.GetContext(ctx, &row,
			`SELECT is_active, can_handle_suppliers, can_manage_credit FROM users WHERE id = $1`, userID); err != nil {
			return middleware.UserFlags{}, false
		}
		return middleware.UserFlags{
			CanHandleSuppliers: row.CanHandleSuppliers,
			CanManageCredit:    row.CanManageCredit,
		}, row.Active
```

- [ ] **Step 6: Thread through the auth model**

In `internal/features/auth/model.go`: add to `User` struct (after `CanHandleSuppliers bool` at ~:27):
```go
	// CanManageCredit lets a cashier override/raise credit limits at the till.
	CanManageCredit bool `db:"can_manage_credit" json:"can_manage_credit"`
```
Add to both `CreateUserInput` and `UpdateUserInput` (after their `CanHandleSuppliers string` field):
```go
	// CanManageCredit arrives as an HTML checkbox — see CanHandleSuppliers.
	CanManageCredit string `json:"can_manage_credit" form:"can_manage_credit" validate:"omitempty"`
```

- [ ] **Step 7: Pass through service + repository**

In `internal/features/auth/service.go:161`, extend the `CreateUser` repo call to pass `checkboxOn(in.CanManageCredit)` as a new trailing arg; and `:210` for `UpdateUser` likewise.
In `internal/features/auth/repository.go`:
- `Create` (:55): add `canCredit bool` param; add `can_manage_credit` to the INSERT column list and a `$N` placeholder + arg.
- `Update` (:69): add `canCredit bool` param; add `can_manage_credit = $N` to the SET list + arg (renumber the trailing `WHERE id` placeholder).

- [ ] **Step 8: Add the admin Users checkbox**

In `templates/pages/admin/users.templ`, duplicate the supplier checkbox block (`:104-108`) below it:
```templ
					<input
						type="checkbox"
						name="can_manage_credit"
						checked?={ u != nil && u.CanManageCredit }
						class="..."/> <!-- copy the exact classes + surrounding label from the supplier row -->
```
Give it a label like "Manage credit at till (override limit, raise credit limit)".

- [ ] **Step 9: Extend the flags test**

In `internal/middleware/flags_test.go`, add a `TestMayManageCredit` mirroring the suppliers test (admin/manager true regardless; cashier follows flag), and assert `CanManageCreditCtx` survives into request context (mirror the existing `Ctx` test).

- [ ] **Step 10: Build + test + commit**

Run: `make build && go vet ./... && go test ./internal/middleware/... ./internal/features/auth/...`
Expected: PASS.
```bash
git add migrations/0057_manage_credit_at_till.sql internal/middleware/ internal/web/web.go internal/features/auth/ templates/pages/admin/users.templ
git commit -m "feat(users): add can_manage_credit per-user flag (plumbing only)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: Credit over-limit override (server)

`CheckTender` learns to allow an over-limit account line when the caller is trusted; the sales API re-checks the flag before honouring the client's request.

**Files:**
- Modify: `internal/features/sales/tender.go:46-66` (CheckTender signature)
- Modify: `internal/features/sales/service.go:109` (CreateInput field), `:457` (call site)
- Modify: `internal/features/sales/api.go:20-33` (sanitize flag server-side)
- Test: `internal/features/sales/tender_test.go`

**Interfaces:**
- Consumes: `middleware.CanManageCredit(c)`, `middleware.MayManageCredit(role, flag)` (Task 1).
- Produces: `CheckTender(t Tender, total decimal.Decimal, hasCustomer bool, availableCredit decimal.Decimal, allowOverLimit bool) error`; `CreateInput.AllowOverLimit bool` (json `allow_over_limit`).

- [ ] **Step 1: Write failing tests**

In `internal/features/sales/tender_test.go`, add:
```go
func TestCheckTenderAllowsOverLimitWhenApproved(t *testing.T) {
	tn := Tender{Paid: decimal.Zero, OnAccount: td("700")}
	if err := CheckTender(tn, td("700"), true, td("300"), true); err != nil {
		t.Errorf("an approved over-limit sale should pass: %v", err)
	}
}

func TestCheckTenderStillRejectsOverLimitWithoutApproval(t *testing.T) {
	tn := Tender{Paid: decimal.Zero, OnAccount: td("700")}
	if err := CheckTender(tn, td("700"), true, td("300"), false); err == nil {
		t.Error("over-limit without approval must still be refused")
	}
}

func TestCheckTenderApprovalDoesNotWaiveShortfall(t *testing.T) {
	// Approval only waives the limit — a shortfall (unpaid) is still an error.
	tn := Tender{Paid: td("500"), OnAccount: decimal.Zero}
	if err := CheckTender(tn, td("1200"), true, td("9999"), true); err == nil {
		t.Error("a shortfall must be refused even with over-limit approval")
	}
}
```
Also update every existing `CheckTender(...)` call in this test file to pass a trailing `false`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/features/sales/ -run TestCheckTender -v`
Expected: compile error / FAIL (signature mismatch).

- [ ] **Step 3: Update `CheckTender`**

In `internal/features/sales/tender.go`, change the signature and guard:
```go
func CheckTender(t Tender, total decimal.Decimal, hasCustomer bool, availableCredit decimal.Decimal, allowOverLimit bool) error {
```
Wrap the limit check (currently the `if t.OnAccount.GreaterThan(availableCredit)` block) so it is skipped when approved:
```go
	if !allowOverLimit && t.OnAccount.GreaterThan(availableCredit) {
		return apperr.Conflict("credit limit exceeded (available " +
			money.Display(availableCredit) + ")")
	}
```

- [ ] **Step 4: Add the CreateInput field + call site**

In `internal/features/sales/service.go`, add to `CreateInput` (after `Notes`):
```go
	// AllowOverLimit permits an account line past the customer's credit limit.
	// The web layer sets this only for a user with can_manage_credit — the
	// service trusts it as already-authorised.
	AllowOverLimit bool `json:"allow_over_limit"`
```
Update the call at `:457`:
```go
		if err := CheckTender(tender, total, in.CustomerID != nil, available, in.AllowOverLimit); err != nil {
```

- [ ] **Step 5: Sanitize the flag in the API handler**

In `internal/features/sales/api.go` `Create` (after `c.Validate(&in)`):
```go
	// The over-limit approval is honoured only for a user the role/flag permits;
	// never trust the client alone.
	if in.AllowOverLimit && !middleware.MayManageCredit(middleware.CurrentRole(c), middleware.CanManageCredit(c)) {
		in.AllowOverLimit = false
	}
```
(Confirm `middleware.CurrentRole` exists; if the helper is named differently, use the same accessor `users.templ`/handlers use for the role.)

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/features/sales/ -run TestCheckTender -v`
Expected: PASS.

- [ ] **Step 7: Commit**

Run: `go vet ./... && make build`
```bash
git add internal/features/sales/
git commit -m "feat(sales): allow an over-limit credit sale when the cashier is authorised

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: Inline credit-limit edit (server)

A cashier-reachable endpoint that updates **only** `credit_limit`, gated by `can_manage_credit`. The admin `PUT /api/customers/:id` stays admin/manager-only.

**Files:**
- Modify: `internal/features/customers/customers.go` (repo `SetCreditLimit`, service `SetCreditLimit`, API handler `SetLimit`, route registration ~:719-726)
- Test: `internal/features/customers/customers_credit_test.go` (create; DB-guarded)

**Interfaces:**
- Consumes: `middleware.RequireRole` (existing), the new flag (Task 1).
- Produces: `Repository.SetCreditLimit(ctx, id int64, limit decimal.Decimal) error`; `Service.SetCreditLimit(ctx, id int64, limit string) error`; route `PATCH /api/customers/:id/credit-limit`.

- [ ] **Step 1: Write a failing DB-guarded test**

Create `internal/features/customers/customers_credit_test.go`. Mirror the DB test harness in `internal/features/stock/batch_price_test.go` (skip if `DATABASE_URL` unset; open a tx; roll back). Test that after `Service.SetCreditLimit(ctx, id, "5000")` the reloaded customer's `CreditLimit` equals `5000`, and that a negative or unparseable value returns a validation error and leaves the limit unchanged.

- [ ] **Step 2: Run to verify it fails**

Run: `DATABASE_URL=$DATABASE_URL go test ./internal/features/customers/ -run TestSetCreditLimit -v`
Expected: FAIL (method not defined).

- [ ] **Step 3: Add the repo method**

In `internal/features/customers/customers.go`, beside `Update`:
```go
// SetCreditLimit updates only a customer's credit limit (the till's inline
// raise). Unlike Update it never touches name/phone/address.
func (r *Repository) SetCreditLimit(ctx context.Context, id int64, limit decimal.Decimal) error {
	res, err := r.q.ExecContext(ctx,
		`UPDATE customers SET credit_limit = $1 WHERE id = $2 AND is_active = true`, limit, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
```

- [ ] **Step 4: Add the service method**

In the `Service` block (beside `Update` at :293):
```go
// SetCreditLimit validates and applies a new credit limit (till inline edit).
func (s *Service) SetCreditLimit(ctx context.Context, id int64, limit string) error {
	v, err := money.Parse(limit)
	if err != nil || v.IsNegative() {
		return apperr.Validation("credit limit must be a non-negative amount")
	}
	err = s.repo.SetCreditLimit(ctx, id, v)
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFound("customer")
	}
	if err != nil {
		return apperr.Internal("failed to update credit limit", err)
	}
	return nil
}
```

- [ ] **Step 5: Add the API handler + route**

In `customers.go`, add a handler beside `Update` (:683):
```go
// CreditLimitInput is the narrow till payload for an inline limit change.
type CreditLimitInput struct {
	CreditLimit string `json:"credit_limit" validate:"required"`
}

func (h *APIHandler) SetLimit(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in CreditLimitInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := h.svc.SetCreditLimit(c.Request().Context(), id, in.CreditLimit); err != nil {
		return err
	}
	return response.NoContent(c)
}
```
In `RegisterAPI` (:719), register with the credit gate (admins/managers always; cashiers only with the flag):
```go
	g.PATCH("/:id/credit-limit", api.SetLimit, middleware.RequireManageCredit())
```
Add `RequireManageCredit()` to `internal/middleware/flags.go`, mirroring `RequireSupplierAccess()`:
```go
// RequireManageCredit gates the till credit-limit endpoint. Must run after JWTAuth.
func RequireManageCredit() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			role, _ := c.Get(ctxRole).(string)
			if !MayManageCredit(role, CanManageCredit(c)) {
				return apperr.Forbidden("you're not set up to change credit limits — ask the owner")
			}
			return next(c)
		}
	}
}
```

- [ ] **Step 6: Run to verify it passes + a 403 check**

Run: `DATABASE_URL=$DATABASE_URL go test ./internal/features/customers/ -run TestSetCreditLimit -v && make build && go vet ./...`
Expected: PASS. (403 behaviour is proven live in Task 9.)

- [ ] **Step 7: Commit**
```bash
git add internal/features/customers/ internal/middleware/flags.go
git commit -m "feat(customers): cashier-accessible credit-limit endpoint behind can_manage_credit

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 4: Credit at the till (UI)

Show the two credit powers to flagged cashiers: approve an over-limit account line, and raise the limit inline. Both already enforced server-side, so this is purely surfacing.

**Files:**
- Modify: `templates/pages/cashier/pos.templ:6-14` (POSData field), `:24` (pass to `pos(...)`), `:689` (override confirm), customer panel (~:222-236) (limit-edit control)
- Modify: `internal/web/cashier.go:94-117` (populate the new POSData field)
- Modify: `static/js/app.js:265` (`pos(...)` signature), `:1625` (payload `allow_over_limit`), `confirmPutOnAccount` (~app.js credit-prompt), add `saveCreditLimit()`
- Regenerate: `static/css/tailwind.css`

**Interfaces:**
- Consumes: `middleware.CanManageCreditCtx(ctx)`, the `PATCH /api/customers/:id/credit-limit` endpoint, `CreateInput.AllowOverLimit`.
- Produces: `POSData.CanManageCredit bool`; JS `pos()` arg `canManageCredit`.

- [ ] **Step 1: Add the POSData field + render**

In `templates/pages/cashier/pos.templ` `POSData`, add `CanManageCredit bool`. In `internal/web/cashier.go` `POS()`, set it from `middleware.CanManageCredit(c)`:
```go
		CanManageCredit: middleware.CanManageCredit(c),
```
(Add the field to the `POSData{...}` literal at :109-117.)

- [ ] **Step 2: Pass the flag into Alpine**

In `pos.templ:24`, append the bool to the `pos(...)` call:
```templ
			x-data={ "pos('" + d.Symbol + "', '" + d.DefaultSaleType + "', " + jsBool(d.AskToPrint) + ", " + plugin.CashierMenuRootsJSON() + ", " + plugin.DrawerSectionsJSON() + ", " + jsBool(d.CanManageCredit) + ")" }
```
In `static/js/app.js:265`, add the trailing param and store it:
```js
function pos(symbol, defaultType, askToPrint, pluginRoots, drawerSections, canManageCredit) {
```
In the returned object's state, add: `canManageCredit: !!canManageCredit,`.

- [ ] **Step 3: Send `allow_over_limit` in the checkout payload**

In `static/js/app.js` checkout `payload` (~:1625), add after `discount_type`:
```js
          allow_over_limit: !!this._overLimitApproved,
```
Add `_overLimitApproved: false` to state; reset it to `false` after each completed sale (beside the existing post-sale resets).

- [ ] **Step 4: Over-limit approval in the credit prompt**

In `confirmPutOnAccount` (the credit-prompt handler used by `pos.templ:689`), when the on-account amount exceeds the selected customer's available credit:
- If `this.canManageCredit`: show an inline "Exceeds limit by Rs X — approve & continue" confirm; on approve set `this._overLimitApproved = true` and proceed.
- Else: keep today's behaviour (blocked; prompt to reduce or take cash).
Compute available credit client-side from the loaded customer (`credit_limit - outstanding_balance`), purely to decide whether to show the approve step — the server remains authoritative.

- [ ] **Step 5: Inline credit-limit edit control**

In the customer panel (near the customer chooser, `pos.templ:222-236`), add — shown only `x-show="canManageCredit && customerId"` — a small "Adjust limit" button opening a tiny inline input + Save. Wire `x-on:click` to a new `saveCreditLimit()`:
```js
    async saveCreditLimit() {
      const v = String(this.creditLimitEdit || "").trim();
      if (v === "" || Number(v) < 0) { toast("Enter a valid limit", "error"); return; }
      await apiFetch("PATCH", `/api/customers/${this.customerId}/credit-limit`, { credit_limit: v });
      await this.loadCustomers();               // refresh the cached limit
      this.showLimitEdit = false;
      toast("Credit limit updated", "success");
    },
```
Add `showLimitEdit: false, creditLimitEdit: "",` to state.

- [ ] **Step 6: Regenerate CSS, build, smoke-check**

Run: `make css && make build && go vet ./...`
Expected: builds clean; no templ errors.

- [ ] **Step 7: Commit**
```bash
git add templates/pages/cashier/pos.templ internal/web/cashier.go static/js/app.js static/css/tailwind.css
git commit -m "feat(till): credit over-limit approval + inline credit-limit edit for authorised cashiers

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 5: Found-at-till (server)

When a confirmed sale line is short, correct the count **up** to cover it (a positive `adjust` movement noted "found at till — count corrected before sale"), then deplete normally. On-hand ends at 0; never negative.

**Files:**
- Modify: `internal/features/stock/batches.go` (add `FoundAtTill`)
- Modify: `internal/features/sales/service.go:59-80` (ItemInput field), `:372-398` (oversell path)
- Test: `internal/features/stock/found_at_till_test.go` (create; DB-guarded), `internal/features/sales/oversell_db_test.go` (create; DB-guarded)

**Interfaces:**
- Consumes: existing `Repository.GetQuantity`, `Increment`, `InsertMovement`, `InsertBatch`, `productCost`, `DepleteFEFO`, `DepleteBatch`, `MoveAdjust`.
- Produces: `Repository.FoundAtTill(ctx, productID, batchID int64, qty, cost decimal.Decimal, userID int64) error`; `ItemInput.AllowOversell bool` (json `allow_oversell`).

- [ ] **Step 1: Write a failing DB-guarded test for `FoundAtTill`**

Create `internal/features/stock/found_at_till_test.go` (harness like `batch_price_test.go`). Seed a product with on-hand 0 and no live batch. Call `repo.FoundAtTill(ctx, productID, 0, dec("1"), dec("10"), userID)`. Assert: on-hand (`GetQuantity`) is now 1; a `stock_batches` row exists with `qty_remaining=1`, `source='found'`; a `stock_movements` row exists with `type='adjust'`, `quantity=1`, note = "found at till — count corrected before sale". Second case: seed a batch with `qty_remaining=2`, call `FoundAtTill(ctx, productID, thatBatchID, dec("1"), dec("10"), userID)` and assert that batch's `qty_remaining` is now 3 (topped up, not a new lot).

- [ ] **Step 2: Run to verify it fails**

Run: `DATABASE_URL=$DATABASE_URL go test ./internal/features/stock/ -run TestFoundAtTill -v`
Expected: FAIL (method not defined).

- [ ] **Step 3: Implement `FoundAtTill`**

In `internal/features/stock/batches.go`:
```go
// FoundAtTill raises on-hand by qty because the goods physically existed but the
// count was short. It is the honest alternative to letting stock go negative: it
// tops up the named lot (or opens a "found" lot when batchID is 0), bumps the
// fast stock mirror, and records a positive adjust movement the owner can see.
func (r *Repository) FoundAtTill(ctx context.Context, productID, batchID int64, qty, cost decimal.Decimal, userID int64) error {
	if err := r.Increment(ctx, productID, qty); err != nil {
		return err
	}
	if batchID > 0 {
		if _, err := r.q.ExecContext(ctx,
			`UPDATE stock_batches SET qty_remaining = qty_remaining + $1 WHERE id = $2`, qty, batchID); err != nil {
			return err
		}
	} else {
		if _, err := r.InsertBatch(ctx, NewBatch{
			ProductID: productID, Quantity: qty, CostPrice: cost, Source: "found",
		}); err != nil {
			return err
		}
	}
	note := "found at till — count corrected before sale"
	return r.InsertMovement(ctx, MovementInput{
		ProductID: productID,
		Type:      MoveAdjust,
		Quantity:  qty,          // positive: stock coming in
		UserID:    userID,
		Note:      &note,
		Cost:      cost.Mul(qty),
	})
}
```
Confirm `"found"` is an accepted value for `stock_batches.source` (check the column type / any CHECK/enum in the migration that created `stock_batches` and `0052`). If `source` is a constrained enum, use an existing accepted value like `"adjust"` instead and adjust the test.

- [ ] **Step 4: Add the ItemInput field**

In `internal/features/sales/service.go` `ItemInput` (after `BatchID`):
```go
	// AllowOversell lets this line sell more than the count shows: the customer
	// is holding stock that was never counted. The server corrects the count up
	// (FoundAtTill) before depleting, so on-hand lands at 0, never negative.
	// Confirm-gated at the till; ignored for is_service products.
	AllowOversell bool `json:"allow_oversell"`
```

- [ ] **Step 5: Write a failing sale-level DB test**

Create `internal/features/sales/oversell_db_test.go` (DB-guarded, roll back). Seed a product with on-hand 0. Building a `CreateInput` with one line `AllowOversell: true, Quantity: "1"` and cash payment: assert `Create` succeeds, the reloaded on-hand is 0, and two movements exist for the product — one `adjust` (+1, the note) and one `sell` (-1). Second test: the same line with `AllowOversell: false` returns an "insufficient stock" error and leaves on-hand at 0 with no movements.

- [ ] **Step 6: Run to verify it fails**

Run: `DATABASE_URL=$DATABASE_URL go test ./internal/features/sales/ -run TestOversell -v`
Expected: FAIL (allow_oversell not honoured yet).

- [ ] **Step 7: Wire the oversell path into the sale**

In `internal/features/sales/service.go`, in the non-service branch (currently :372-398), replace the `DecrementGuarded` → `!ok` refusal with:
```go
			if !p.IsService {
				ok, err := stkRepo.DecrementGuarded(ctx, p.ID, qty)
				if err != nil {
					return apperr.Internal("failed to update stock", err)
				}
				if !ok {
					if !it.AllowOversell {
						return apperr.Conflict(fmt.Sprintf("insufficient stock for %s", p.Name))
					}
					// The goods exist; the count was short. Correct up to cover the
					// line (topping up the picked lot, or opening a found lot), then
					// the guarded decrement must now succeed.
					onHand, err := stkRepo.GetQuantity(ctx, p.ID)
					if err != nil {
						return apperr.Internal("failed to read stock", err)
					}
					short := qty.Sub(onHand)
					batchID := int64(0)
					if pickedBatch != nil {
						batchID = pickedBatch.ID
					}
					if err := stkRepo.FoundAtTill(ctx, p.ID, batchID, short, p.CostPrice, cashierID); err != nil {
						return apperr.Internal("failed to correct stock", err)
					}
					ok, err = stkRepo.DecrementGuarded(ctx, p.ID, qty)
					if err != nil {
						return apperr.Internal("failed to update stock", err)
					}
					if !ok {
						return apperr.Internal("stock correction did not cover the sale", nil)
					}
				}
				if pickedBatch != nil {
					cost, err = stkRepo.DepleteBatch(ctx, pickedBatch.ID, qty)
				} else {
					cost, err = stkRepo.DepleteFEFO(ctx, p.ID, qty)
				}
				if err != nil {
					return apperr.Internal("failed to deplete batches", err)
				}
				if cost.IsZero() {
					cost = p.CostPrice
				}
			}
```
(Keep the surrounding `cost`/`unitPrice` logic intact — only the guard+correction is new.)

- [ ] **Step 8: Run both DB test files**

Run: `DATABASE_URL=$DATABASE_URL go test ./internal/features/stock/ ./internal/features/sales/ -run 'FoundAtTill|Oversell' -v`
Expected: PASS.

- [ ] **Step 9: Full unit suite + build + commit**

Run: `make build && go vet ./... && go test ./...`
Expected: PASS.
```bash
git add internal/features/stock/batches.go internal/features/stock/found_at_till_test.go internal/features/sales/
git commit -m "feat(sales): found-at-till — correct the count up and sell instead of blocking

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 6: Found-at-till (till UI)

Make 0-stock items sellable with a one-tap confirm; send `allow_oversell` per line.

**Files:**
- Modify: `templates/pages/cashier/pos.templ:159-160` (remove hard disable; keep the "0 left" chip)
- Modify: `static/js/app.js` `addToCart` (~:1002), checkout payload (~:1625)
- Regenerate: `static/css/tailwind.css`

**Interfaces:**
- Consumes: `ItemInput.AllowOversell` (Task 5).
- Produces: cart lines carry `allow_oversell: true` when added from 0 stock.

- [ ] **Step 1: Un-grey 0-stock items**

In `templates/pages/cashier/pos.templ:160`, remove the `x-bind:disabled="!p.is_service && Number(p.stock_qty) <= 0"` attribute so the button is tappable. Leave the "Stock: …" line (:170) as-is (it already shows the count).

- [ ] **Step 2: Confirm + flag in `addToCart`**

In `static/js/app.js` `addToCart(p, lot, opts)`, before pushing a new line, when the item is short and not already confirmed:
```js
      if (!p.is_service && Number(p.stock_qty) <= 0 && !(opts && opts.oversellOK)) {
        if (!confirm("Stock shows 0 for " + p.name + " — sell anyway?")) return;
        opts = Object.assign({}, opts, { oversellOK: true });
      }
```
When building the pushed line object, add `allow_oversell: !!(opts && opts.oversellOK),`. On the merge-existing branch, set `existing.allow_oversell = existing.allow_oversell || !!(opts && opts.oversellOK);`.
(If the shop prefers a styled modal over `confirm()`, reuse the `showQuickItem`/`pricePick` overlay pattern — but a native confirm is acceptable for the first cut and keeps the rush fast.)

- [ ] **Step 3: Send it in the payload**

In the checkout `payload.items.map` (~:1632), add:
```js
            allow_oversell: !!it.allow_oversell,
```

- [ ] **Step 4: Regenerate CSS, build**

Run: `make css && make build`
Expected: clean build.

- [ ] **Step 5: Commit**
```bash
git add templates/pages/cashier/pos.templ static/js/app.js static/css/tailwind.css
git commit -m "feat(till): sell a 0-stock item with a confirm (found-at-till)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 7: No duplicate customers (server)

Phone becomes required; creating with a phone that matches an active customer returns that customer instead of a new row.

**Files:**
- Modify: `internal/features/customers/customers.go:45-53` (Phone required), `:264-278` (Create dedup + normalize), `:668-681` (Create handler surfaces "existed")
- Test: `internal/features/customers/customers_dedup_test.go` (create; DB-guarded)

**Interfaces:**
- Consumes: existing `Repository.FindByPhone` (:137).
- Produces: `Service.Create` returns the existing active customer (no new row) when the normalized phone matches; blank phone rejected by validation. Create response includes whether it was reused (e.g. a `201` for new vs `200` for existing, or a boolean field — see Step 4).

- [ ] **Step 1: Write failing DB-guarded tests**

Create `internal/features/customers/customers_dedup_test.go`. Test A: create a customer with phone "0771234567"; create again with the same phone (any name) → the second call returns the **same** `id`, and a count of active customers with that phone is exactly 1. Test B: `Service.Create` with a blank/nil phone returns a validation error.

- [ ] **Step 2: Run to verify it fails**

Run: `DATABASE_URL=$DATABASE_URL go test ./internal/features/customers/ -run TestCreateDedup -v`
Expected: FAIL (duplicate created / blank accepted).

- [ ] **Step 3: Require phone + normalize**

In `internal/features/customers/customers.go`, change `CreateInput.Phone` validation from `omitempty` to required:
```go
	Phone       *string `json:"phone" form:"phone" validate:"required,min=4,max=15"`
```
Add a small normalizer near the top of the file:
```go
// normalizePhone trims and strips spaces/dashes so "077-123 4567" and
// "0771234567" are treated as the same customer.
func normalizePhone(p string) string {
	r := strings.NewReplacer(" ", "", "-", "")
	return r.Replace(strings.TrimSpace(p))
}
```

- [ ] **Step 4: Dedup in `Service.Create`**

Rewrite `Service.Create` (:264) to look up by normalized phone first:
```go
func (s *Service) Create(ctx context.Context, in CreateInput) (*Customer, bool, error) {
	limit, err := money.Parse(in.CreditLimit)
	if err != nil || limit.IsNegative() {
		return nil, false, apperr.Validation("credit limit must be a non-negative amount")
	}
	opening, err := parseOpening(in.OpeningBalance)
	if err != nil {
		return nil, false, err
	}
	if in.Phone == nil || strings.TrimSpace(*in.Phone) == "" {
		return nil, false, apperr.Validation("a phone number is required")
	}
	phone := normalizePhone(*in.Phone)
	if existing, ferr := s.repo.FindByPhone(ctx, phone); ferr == nil {
		return existing, true, nil // reuse — do not create a duplicate
	} else if !errors.Is(ferr, sql.ErrNoRows) {
		return nil, false, apperr.Internal("failed to check for an existing customer", ferr)
	}
	c, err := s.repo.Create(ctx, strings.TrimSpace(in.Name), &phone, in.Address, limit, opening)
	if err != nil {
		return nil, false, apperr.Internal("failed to create customer", err)
	}
	return c, false, nil
}
```
Update every caller of `Service.Create` in the codebase to the new 3-value signature (grep `\.Create(ctx` in `internal/features/customers` and `internal/web`; the admin `CustomerCreate` handler and the CSV import path if it calls `Create` — note the importer uses `Import`/upsert, likely unaffected). Where a caller ignores the flag, use `_`.

- [ ] **Step 5: Surface "existed" from the API handler**

In `APIHandler.Create` (:668), return `200 OK` with the customer when reused and `201 Created` when new, so the till can tell them apart:
```go
	cust, existed, err := h.svc.Create(c.Request().Context(), in)
	if err != nil {
		return err
	}
	if existed {
		return response.OK(c, cust)
	}
	return response.Created(c, cust)
```

- [ ] **Step 6: Run to verify pass + full build**

Run: `DATABASE_URL=$DATABASE_URL go test ./internal/features/customers/ -run TestCreateDedup -v && make build && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**
```bash
git add internal/features/customers/ internal/web/
git commit -m "feat(customers): require phone and reuse an existing customer on phone match

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 8: No duplicate customers (till UI)

Make phone required in the add-customer modal and tell the cashier when an existing customer was reused.

**Files:**
- Modify: `templates/pages/cashier/pos.templ:474-486` (phone Required)
- Modify: `static/js/app.js` `addCustomer` (~:907)
- Regenerate: `static/css/tailwind.css`

**Interfaces:**
- Consumes: the create endpoint's 200-vs-201 signal (Task 7).

- [ ] **Step 1: Phone required in the modal**

In `templates/pages/cashier/pos.templ` (~:479), change the phone label/placeholder from "Optional" to "Required" and add `required` to the input (or rely on the JS guard in Step 2).

- [ ] **Step 2: Guard + reuse message in `addCustomer`**

In `static/js/app.js` `addCustomer` (:907), require the phone and detect reuse via the HTTP status. `apiFetch` returns the parsed body; extend the guard:
```js
    async addCustomer() {
      if (!this.newCustomer.name.trim()) { toast("Enter a customer name", "error"); return; }
      if (!this.newCustomer.phone.trim()) { toast("Enter a phone number", "error"); return; }
      const res = await apiFetch("POST", "/api/customers", {
        name: this.newCustomer.name.trim(),
        phone: this.newCustomer.phone.trim(),
        credit_limit: String(this.newCustomer.credit_limit || "0"),
      }, { returnStatus: true });          // see Step 3
      await this.loadCustomers();
      this.customerId = String(res.data.id);
      this.showAddCustomer = false;
      this.newCustomer = { name: "", phone: "", credit_limit: "" };
      toast(res.status === 200 ? "Customer already exists — using them" : "Customer added", "success");
    },
```

- [ ] **Step 3: Let `apiFetch` expose the status (only if needed)**

Inspect `apiFetch` in `static/js/app.js`. If it already returns the status, use it directly and drop the `{ returnStatus: true }` option. If not, add an opt-in that returns `{ data, status }` when `opts.returnStatus` is set, without changing existing callers. Keep the change minimal and backward-compatible.

- [ ] **Step 4: Regenerate CSS, build**

Run: `make css && make build`
Expected: clean build.

- [ ] **Step 5: Commit**
```bash
git add templates/pages/cashier/pos.templ static/js/app.js static/css/tailwind.css
git commit -m "feat(till): require phone when adding a customer; reuse existing on match

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 9: Full verification + live E2E

**Files:** none (verification only; a follow-up commit only if a fix is needed).

- [ ] **Step 1: Full build + vet + unit tests**

Run: `make build && go vet ./... && go test ./...`
Expected: all PASS.

- [ ] **Step 2: DB-guarded tests**

Run: `DATABASE_URL=$DATABASE_URL go test ./internal/features/stock/ ./internal/features/sales/ ./internal/features/customers/ -v`
Expected: PASS (not skipped).

- [ ] **Step 3: Migration round-trip on dev DB**

Run: `goose ... up && goose ... down && goose ... up` for `0057`.
Expected: clean.

- [ ] **Step 4: Live E2E on the dev server**

Start the dev server. As an admin, grant `can_manage_credit` to one test cashier and leave another without it. Then:
1. **Found-at-till:** find/scan a product showing 0 stock → confirm "sell anyway" → complete a cash sale. Verify: sale succeeds; Stock & Movements shows a `+` adjust noted "found at till — count corrected before sale" and a `sell`; on-hand is 0 (not negative).
2. **Multi-batch found:** a product with a short live lot at a distinct price → "which price?" prompt → pick it → oversell by 1 → verify the top-up lands on that lot at that price and the receipt shows that price.
3. **Over-limit (flagged):** push a customer over their limit → approve → sale posts to account; the debt shows in Receipts/Customer statement.
4. **Inline limit (flagged):** raise the customer's limit at the till → a normal on-account sale now passes without approval.
5. **Unflagged cashier:** both credit powers are absent in the UI; hitting `PATCH /api/customers/:id/credit-limit` and a sale with `allow_over_limit:true` are refused (403 / limit still enforced).
6. **Dedup:** add a customer with a new phone; add again with the same phone → till says "Customer already exists — using them"; only one customer exists. Blank phone is rejected.
7. **Regression:** a normal in-stock sale, and an in-limit credit sale, behave exactly as before.

- [ ] **Step 5: Restore the dev DB to baseline**

Undo the test data (remove test sales/customers or `make reset-demo` per project convention). Leave `pos_db` running.

- [ ] **Step 6: Confirm tailwind.css committed**

Run: `git status` — ensure `static/css/tailwind.css` is committed (no unstaged regeneration diff). If dirty: `make css`, then commit.

---

## Self-Review

**Spec coverage:**
- Section 1 (found-at-till, correct up, never negative, distinct-note movement) → Tasks 5, 6. ✓
- Section 1 batch interaction (which-price prompt fires; short chosen batch topped up at its price; 0-stock → product price, no prompt) → Task 5 (Step 7 uses `pickedBatch`), Task 9 Step 4.2. ✓ (The "which price?" prompt is unchanged shipped behaviour; found-top-up targets the picked lot.)
- Section 2 override-this-sale → Tasks 2, 4. ✓
- Section 2 inline limit edit (cashier endpoint, admin PUT untouched) → Tasks 3, 4. ✓
- Section 3 phone required + reuse on match → Tasks 7, 8. ✓
- Section 4 per-user flag, server-verified, gates both credit powers → Task 1 (plumbing), Tasks 2/3 (enforcement). ✓
- Admin Stock Adjust already batch-aware (no change) → confirmed in spec; no task needed. ✓

**Placeholder scan:** No TBD/TODO; every code step has real code. The only deliberate "inspect then adapt" steps are Task 5 Step 3 (verify `source` accepts `"found"`), Task 2 Step 5 (confirm `middleware.CurrentRole` name), and Task 8 Step 3 (`apiFetch` status) — each states exactly what to check and the fallback.

**Type consistency:** `can_manage_credit`/`CanManageCredit` used uniformly. `FoundAtTill(ctx, productID, batchID int64, qty, cost decimal.Decimal, userID int64)` matches its caller in Task 5 Step 7. `CheckTender(..., allowOverLimit bool)` matches call site and tests. `Service.Create` new 3-value return `(*Customer, bool, error)` is propagated to all callers (Task 7 Step 4) and the handler (Step 5).
