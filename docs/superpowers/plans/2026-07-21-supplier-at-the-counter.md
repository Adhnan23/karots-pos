# Supplier at the Counter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a trusted cashier pay a supplier, receive a delivery and place an order at the till — and fix the existing hole where marking a purchase paid moves no money.

**Architecture:** No new feature package. `purchases` gains `CreateTx`/`ReceiveTx` so the web layer can compose goods + payment + cash movement in one transaction (`purchases` cannot import `supplierpay` — `supplierpay` imports `purchases`). A per-user `can_handle_suppliers` flag rides the user-active query that already runs on every authenticated request, and reaches templates through the request context that `response.render` already hands to templ.

**Tech Stack:** Go 1.x, Echo, sqlx, Postgres 17, goose migrations, Templ, HTMX, Alpine.js, Tailwind, shopspring/decimal.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-21-supplier-at-the-counter-design.md`. Read it before starting.
- **Never `git add`** `static/css/tailwind.css`, `cmd/server/enabled_plugins.go`, `.claude/settings.local.json`. They stay dirty in every commit.
- **Never `git add` generated `*_templ.go` files** — they are gitignored. Run `make templ` to regenerate.
- Run `make css` after introducing any new Tailwind utility class. CSS is embedded in the binary: rebuild and restart before trusting the browser.
- Feature packages never import `templates/...`.
- Every migration reversible. A `-- +goose Down` that re-imposes a narrower constraint must first delete or convert violating rows.
- Commit directly to `main`. End every commit message with:
  `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`
- Supplier payment methods are **cash, card, online**. Never `bank`.
- Hidden system-admin login for manual testing: phone `0000000001`, PIN `2273`.
- The local `pos_db` is a **dev** database. Test freely; tidy up after.
- Run `make test` before every commit. It must be green.

## File Structure

**Created:**
- `migrations/0051_supplier_counter_access.sql` — both new columns.
- `internal/middleware/flags.go` — `UserFlags`, request-context stash, accessors, `RequireSupplierAccess`.
- `internal/middleware/flags_test.go` — the gate table test.
- `internal/web/supplier_pay_shared.go` — the one payment-composition helper admin and cashier both call.
- `internal/web/cashier_suppliers.go` — all cashier supplier handlers.
- `internal/web/supplier_money_test.go` — DB-guarded proof that money actually moves.
- `templates/pages/cashier/suppliers.templ` — the counter supplier screens.

**Modified:**
- `internal/features/auth/model.go`, `repository.go`, `service.go` — the user flag.
- `internal/features/lockers/lockers.go` — the locker flag + cashier filtering.
- `internal/features/purchases/service.go` — `CreateTx`/`ReceiveTx`, `PaidAmount` removed.
- `internal/middleware/auth.go` — widened validator, context stash.
- `internal/web/web.go` — validator query, route wiring.
- `internal/web/admin_more.go` — pay handler calls the shared helper.
- `internal/web/admin_purchases.go` — receive composes payment.
- `internal/web/cashier.go` — locker list filtering.
- `templates/pages/admin/users.templ`, `lockers.templ`, `purchases.templ` — the toggles and the receive payment block.
- `templates/layouts/cashier.templ` — the Suppliers tab.
- `static/js/app.js` — `grnReceive` posts payment fields.

---

### Task 1: Schema and model columns

Both columns land together because `auth.Repository.Create` uses `INSERT … RETURNING *` and `lockers.Repository.List` uses `SELECT l.*`. sqlx errors with "missing destination name" the moment a column exists without a matching struct field, so schema and structs must move in one commit or the build breaks.

**Files:**
- Create: `migrations/0051_supplier_counter_access.sql`
- Modify: `internal/features/auth/model.go:12-24`
- Modify: `internal/features/lockers/lockers.go:45-53`

**Interfaces:**
- Produces: `auth.User.CanHandleSuppliers bool` (db `can_handle_suppliers`); `lockers.Locker.CashierAccess bool` (db `cashier_access`).

- [ ] **Step 1: Write the migration**

Create `migrations/0051_supplier_counter_access.sql`:

```sql
-- +goose Up
-- Two independent permissions the shop had no way to express.
--
-- can_handle_suppliers marks a cashier trusted to pay suppliers, take in
-- deliveries and place orders at the till. It defaults to FALSE so no existing
-- cashier silently gains the ability — the owner opts each person in.
--
-- cashier_access marks a locker a cashier may move money into or out of. It
-- defaults to TRUE because that is exactly today's behaviour: the withdraw
-- dialog already lists every active locker. Switching it off on the owner's
-- safe is the new capability.
ALTER TABLE users   ADD COLUMN can_handle_suppliers boolean NOT NULL DEFAULT false;
ALTER TABLE lockers ADD COLUMN cashier_access       boolean NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE users   DROP COLUMN can_handle_suppliers;
ALTER TABLE lockers DROP COLUMN cashier_access;
```

- [ ] **Step 2: Add the struct fields**

In `internal/features/auth/model.go`, inside `type User struct`, after the `ReceiptPrinter` line:

```go
	CanHandleSuppliers bool `db:"can_handle_suppliers" json:"can_handle_suppliers"`
```

In `internal/features/lockers/lockers.go`, inside `type Locker struct`, after the `IsActive` line:

```go
	CashierAccess bool `db:"cashier_access" json:"cashier_access"`
```

- [ ] **Step 3: Apply and verify the migration round-trips**

```bash
make migrate
docker compose exec -T postgres psql -U pos_user -d pos_db -c "\d users"   | grep can_handle_suppliers
docker compose exec -T postgres psql -U pos_user -d pos_db -c "\d lockers" | grep cashier_access
```

Expected: both columns listed, `not null`, defaults `false` and `true` respectively.

Now prove `Down` works and re-apply:

```bash
go run ./cmd/goose -dir migrations postgres "$DATABASE_URL" down 2>/dev/null || \
  docker compose exec -T postgres psql -U pos_user -d pos_db -c \
  "ALTER TABLE users DROP COLUMN can_handle_suppliers; ALTER TABLE lockers DROP COLUMN cashier_access;"
make migrate
```

Expected: no error, columns present again.

- [ ] **Step 4: Verify the build and existing tests**

```bash
go build ./... && make test
```

Expected: build succeeds, all tests pass. A "missing destination name" error here means a struct field is misspelled against its column.

- [ ] **Step 5: Commit**

```bash
git add migrations/0051_supplier_counter_access.sql internal/features/auth/model.go internal/features/lockers/lockers.go
git commit -m "feat(db): per-user supplier access and per-locker cashier access

Neither permission was expressible: 'manager' unlocks the whole admin
panel, and the cashier withdraw dialog lists every locker including the
owner's safe.

can_handle_suppliers defaults false so nobody gains it silently.
cashier_access defaults true so today's behaviour is unchanged.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: The permission gate

The flag must reach two places: route middleware, and templates (to hide the nav tab). `response.render` calls `component.Render(c.Request().Context(), …)`, so stashing the flag in the **request** context makes it reachable from any template with no change to the eight `layouts.Cashier` call sites.

**Files:**
- Create: `internal/middleware/flags.go`
- Create: `internal/middleware/flags_test.go`
- Modify: `internal/middleware/auth.go:14-27` (the validator hook), `:76-85` (inside `JWTAuth`)
- Modify: `internal/web/web.go:123-129` (the validator query)

**Interfaces:**
- Consumes: `auth.User.CanHandleSuppliers` from Task 1.
- Produces:
  - `middleware.UserFlags{CanHandleSuppliers bool}`
  - `middleware.UserValidator func(ctx context.Context, userID int64) (UserFlags, bool)`
  - `middleware.CanHandleSuppliers(c echo.Context) bool`
  - `middleware.CanHandleSuppliersCtx(ctx context.Context) bool`
  - `middleware.RequireSupplierAccess() echo.MiddlewareFunc`

- [ ] **Step 1: Write the failing gate test**

Create `internal/middleware/flags_test.go`:

```go
package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// TestRequireSupplierAccess pins who may reach the supplier counter routes.
// Admins and managers always pass; a cashier passes only when the owner has
// switched the per-user flag on.
func TestRequireSupplierAccess(t *testing.T) {
	cases := []struct {
		name    string
		role    string
		flag    bool
		allowed bool
	}{
		{"admin without flag", "admin", false, true},
		{"manager without flag", "manager", false, true},
		{"plain cashier", "cashier", false, false},
		{"trusted cashier", "cashier", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/cashier/suppliers", nil)
			req = req.WithContext(context.WithValue(req.Context(), ctxFlagsKey, UserFlags{CanHandleSuppliers: tc.flag}))
			c := e.NewContext(req, httptest.NewRecorder())
			c.Set(ctxRole, tc.role)
			c.Set(ctxCanSuppliers, tc.flag)

			called := false
			h := RequireSupplierAccess()(func(echo.Context) error {
				called = true
				return nil
			})
			err := h(c)

			if tc.allowed && (err != nil || !called) {
				t.Fatalf("expected the request through, got err=%v called=%v", err, called)
			}
			if !tc.allowed && (err == nil || called) {
				t.Fatalf("expected a refusal, got err=%v called=%v", err, called)
			}
		})
	}
}

// TestCanHandleSuppliersCtx proves the flag survives into the request context,
// which is what templates read to decide whether to draw the Suppliers tab.
func TestCanHandleSuppliersCtx(t *testing.T) {
	base := context.Background()
	if CanHandleSuppliersCtx(base) {
		t.Fatal("a bare context must not grant supplier access")
	}
	withFlag := context.WithValue(base, ctxFlagsKey, UserFlags{CanHandleSuppliers: true})
	if !CanHandleSuppliersCtx(withFlag) {
		t.Fatal("the stashed flag was not read back")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

```bash
go test ./internal/middleware/ -run 'Supplier' -v
```

Expected: FAIL — `undefined: ctxFlagsKey`, `undefined: UserFlags`, `undefined: RequireSupplierAccess`.

- [ ] **Step 3: Write the flags file**

Create `internal/middleware/flags.go`:

```go
package middleware

import (
	"context"

	"karots-pos/internal/apperr"

	"github.com/labstack/echo/v4"
)

// UserFlags carries per-user permissions that are not expressible as a role.
// It is a plain struct so this package keeps no dependency on the auth feature.
type UserFlags struct {
	// CanHandleSuppliers lets a cashier pay suppliers, take in deliveries and
	// place orders from the till. Meaningless for admins and managers, who may
	// always do so.
	CanHandleSuppliers bool
}

// ctxKey is unexported so nothing outside this package can collide with it.
type ctxKey struct{ name string }

// ctxFlagsKey stashes UserFlags in the *request* context (not just the echo
// context) because response.render hands the request context to templ. That is
// how a template asks whether to draw the Suppliers tab without every cashier
// page having to thread another parameter through its data struct.
var ctxFlagsKey = ctxKey{"user_flags"}

const ctxCanSuppliers = "can_handle_suppliers"

// CanHandleSuppliers reports the flag for the current request. Admins and
// managers are NOT covered here — this is the raw per-user flag.
func CanHandleSuppliers(c echo.Context) bool {
	b, _ := c.Get(ctxCanSuppliers).(bool)
	return b
}

// CanHandleSuppliersCtx is CanHandleSuppliers for a bare context, for templates.
func CanHandleSuppliersCtx(ctx context.Context) bool {
	f, _ := ctx.Value(ctxFlagsKey).(UserFlags)
	return f.CanHandleSuppliers
}

// MaySeeSuppliers is the full rule: an admin or manager always may; a cashier
// may only with the flag. Used by both the route gate and the nav tab.
func MaySeeSuppliers(role string, flag bool) bool {
	return role == "admin" || role == "manager" || flag
}

// RequireSupplierAccess gates the supplier counter routes. Must run after
// JWTAuth, which is what puts the role and flag in scope.
func RequireSupplierAccess() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			role, _ := c.Get(ctxRole).(string)
			if !MaySeeSuppliers(role, CanHandleSuppliers(c)) {
				return apperr.Forbidden("you're not set up to deal with suppliers — ask the owner")
			}
			return next(c)
		}
	}
}
```

- [ ] **Step 4: Widen the validator hook**

In `internal/middleware/auth.go`, replace the `UserValidator` type and its doc comment (lines 14-17) with:

```go
// UserValidator reports whether the user behind a verified token may still act —
// i.e. the account still exists and is active — and returns their per-user
// flags. It is injected so this package keeps no dependency on the auth feature.
type UserValidator func(ctx context.Context, userID int64) (UserFlags, bool)
```

In the same file, replace the validator call inside `JWTAuth` (lines 76-78) and the `c.Set` block below it with:

```go
			var flags UserFlags
			if userValidator != nil {
				f, ok := userValidator(c.Request().Context(), claims.UserID)
				if !ok {
					return apperr.Unauthorized("your account is no longer active — please sign in again")
				}
				flags = f
			}
			c.Set(ctxUserID, claims.UserID)
			c.Set(ctxRole, claims.Role)
			c.Set(ctxName, claims.Name)
			c.Set(ctxMustChangePin, claims.MustChangePin)
			c.Set(ctxLocked, claims.Locked)
			c.Set(ctxCanSuppliers, flags.CanHandleSuppliers)
			// Templates read the flags from the request context (see flags.go).
			c.SetRequest(c.Request().WithContext(
				context.WithValue(c.Request().Context(), ctxFlagsKey, flags)))
			return next(c)
```

- [ ] **Step 5: Update the one validator implementation**

In `internal/web/web.go`, replace the `middleware.SetUserValidator(...)` call (lines 123-129) with:

```go
	middleware.SetUserValidator(func(ctx context.Context, userID int64) (middleware.UserFlags, bool) {
		var row struct {
			Active             bool `db:"is_active"`
			CanHandleSuppliers bool `db:"can_handle_suppliers"`
		}
		if err := db.GetContext(ctx, &row,
			`SELECT is_active, can_handle_suppliers FROM users WHERE id = $1`, userID); err != nil {
			return middleware.UserFlags{}, false
		}
		return middleware.UserFlags{CanHandleSuppliers: row.CanHandleSuppliers}, row.Active
	})
```

Leave the surrounding comment, extending its final sentence to note that the same lookup now also carries the supplier flag, so revoking it takes effect on the next request rather than at next login.

- [ ] **Step 6: Run the tests**

```bash
go build ./... && go test ./internal/middleware/ -run 'Supplier' -v && make test
```

Expected: both new tests PASS, whole suite green.

- [ ] **Step 7: Commit**

```bash
git add internal/middleware/flags.go internal/middleware/flags_test.go internal/middleware/auth.go internal/web/web.go
git commit -m "feat(auth): gate supplier work on a per-user flag

Every authenticated request already ran a user-active lookup, so the
flag rides in that same query — no extra round trip, and revoking it
takes effect on the next click instead of at next login.

The flag is also stashed in the request context, which response.render
already hands to templ, so a template can ask about it without eight
cashier pages threading another parameter through.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: The toggle on the user form

**Files:**
- Modify: `internal/features/auth/model.go:49-66` (both input structs)
- Modify: `internal/features/auth/repository.go:55-81` (Create, Update)
- Modify: `internal/features/auth/service.go:142`, `:191`
- Modify: `templates/pages/admin/users.templ:97-101`, `:116-130`

**Interfaces:**
- Consumes: `auth.User.CanHandleSuppliers` (Task 1).
- Produces: `auth.CreateUserInput.CanHandleSuppliers string`, `auth.UpdateUserInput.CanHandleSuppliers string` (HTML checkbox form values — `"on"` when ticked, absent when not).

- [ ] **Step 1: Add the field to both inputs**

In `internal/features/auth/model.go`, add to **both** `CreateUserInput` and `UpdateUserInput`, after `ReceiptPrinter`:

```go
	// CanHandleSuppliers arrives as an HTML checkbox: "on" when ticked, absent
	// otherwise. Parsed with checkboxOn rather than a bool so an unticked box
	// reliably means false instead of a bind error.
	CanHandleSuppliers string `json:"can_handle_suppliers" form:"can_handle_suppliers" validate:"omitempty"`
```

Add this helper at the bottom of the same file:

```go
// checkboxOn reads an HTML checkbox value. Browsers omit unticked boxes and
// send "on" for ticked ones; JSON clients may send "true".
func checkboxOn(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "1", "yes":
		return true
	}
	return false
}
```

Add `"strings"` to that file's imports if absent.

- [ ] **Step 2: Persist it**

In `internal/features/auth/repository.go`, change `Create`'s signature and SQL:

```go
func (r *Repository) Create(ctx context.Context, name string, phone *string, role, pinHash, receiptPrinter string, mustChange, canSuppliers bool) (*User, error) {
	var u User
	err := r.db.GetContext(ctx, &u,
		`INSERT INTO users (name, phone, role, pin_hash, must_change_pin, receipt_printer, can_handle_suppliers)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING *`, name, phone, role, pinHash, mustChange, receiptPrinter, canSuppliers)
	if err != nil {
		return nil, err
	}
	return &u, nil
}
```

And `Update`:

```go
func (r *Repository) Update(ctx context.Context, id int64, name string, phone *string, role, receiptPrinter string, canSuppliers bool) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE users SET name = $1, phone = $2, role = $3, receipt_printer = $4, can_handle_suppliers = $5
		 WHERE id = $6 AND is_system = false`,
		name, phone, role, receiptPrinter, canSuppliers, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
```

- [ ] **Step 3: Pass it through the service**

In `internal/features/auth/service.go` line 142, append the new argument:

```go
	u, err := s.repo.Create(ctx, in.Name, &in.Phone, in.Role, string(hash), in.ReceiptPrinter, s.forcePinChange(ctx), checkboxOn(in.CanHandleSuppliers))
```

And line 191:

```go
	err := s.repo.Update(ctx, id, in.Name, &in.Phone, in.Role, in.ReceiptPrinter, checkboxOn(in.CanHandleSuppliers))
```

- [ ] **Step 4: Add the checkbox to the form**

In `templates/pages/admin/users.templ`, after the receipt-printer `</div>` (line 101), insert:

```html
			<div>
				<label class="flex items-start gap-3">
					<input
						type="checkbox"
						name="can_handle_suppliers"
						checked?={ u != nil && u.CanHandleSuppliers }
						class="mt-1 w-4 h-4"
					/>
					<span>
						<span class="text-sm font-medium">Can deal with suppliers</span>
						<span class="block text-xs text-slate-500">Lets this person pay suppliers, take in deliveries and place orders from the till. They will see cost prices. Leave off for anyone who only sells.</span>
					</span>
				</label>
			</div>
```

- [ ] **Step 5: Build, generate templates, test**

```bash
make templ && go build ./... && make test
```

Expected: all green.

- [ ] **Step 6: Verify end to end against the running server**

```bash
make migrate && go build -o /tmp/pos ./cmd/server
env $(grep -v '^#' .env | grep -v '^$' | xargs -d '\n') /tmp/pos &
sleep 3
curl -s -c /tmp/j.txt -X POST http://localhost:3000/login -d "phone=0000000001&pin=2273" -o /dev/null
curl -s -b /tmp/j.txt -X POST http://localhost:3000/admin/users \
  -d "name=Plan Test&phone=0777000111&role=cashier&pin=4321&can_handle_suppliers=on" -o /dev/null -w "%{http_code}\n"
docker compose exec -T postgres psql -U pos_user -d pos_db -c \
  "SELECT name, role, can_handle_suppliers FROM users WHERE phone='0777000111';"
```

Expected: `201`, and the row shows `can_handle_suppliers = t`.

Then update the same user with the box unticked (omit the field entirely, as a browser would) and confirm it flips to `f`:

```bash
UID=$(docker compose exec -T postgres psql -U pos_user -d pos_db -tAc "SELECT id FROM users WHERE phone='0777000111'")
curl -s -b /tmp/j.txt -X PUT http://localhost:3000/admin/users/$UID \
  -d "name=Plan Test&phone=0777000111&role=cashier" -o /dev/null -w "%{http_code}\n"
docker compose exec -T postgres psql -U pos_user -d pos_db -c \
  "SELECT can_handle_suppliers FROM users WHERE phone='0777000111';"
```

Expected: `200`, and `f`. **This is the important half** — an unticked checkbox must revoke, not silently keep the old value.

Leave this user in place; later tasks use it. Note its id.

- [ ] **Step 7: Commit**

```bash
git add internal/features/auth/model.go internal/features/auth/repository.go internal/features/auth/service.go templates/pages/admin/users.templ
git commit -m "feat(users): a 'can deal with suppliers' switch on the user form

Off for everyone until the owner ticks it. Unticking revokes: the box is
read with checkboxOn, so the value a browser omits means false rather
than 'leave as it was'.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 4: Locker access for cashiers

**Files:**
- Modify: `internal/features/lockers/lockers.go` — `CreateInput`, `UpdateInput`, `Repository.Create`, `Repository.Update`, `Service.Create`, `Service.Update`, new `Repository.ListForCashier`, new `Service.ListForCashier`
- Modify: `internal/web/cashier.go:419-434` (`CashierLockers`)
- Modify: `templates/pages/admin/lockers.templ` (the locker form)

**Interfaces:**
- Consumes: `lockers.Locker.CashierAccess` (Task 1).
- Produces: `lockers.Service.ListForCashier(ctx context.Context) ([]Locker, error)` — active lockers with `cashier_access = true`, ordered by name.

- [ ] **Step 1: Add the filtered query**

In `internal/features/lockers/lockers.go`, after `Repository.List`:

```go
// ListForCashier lists the active lockers a cashier may move money into or out
// of. Separate from List rather than a flag on it so an admin screen can never
// accidentally inherit the restriction — the admin picker must keep showing
// every locker.
func (r *Repository) ListForCashier(ctx context.Context) ([]Locker, error) {
	var rows []Locker
	err := r.q.SelectContext(ctx, &rows, `
		SELECT l.*, COALESCE(le.bal, 0) AS balance
		FROM lockers l
		LEFT JOIN (
			SELECT locker_id, SUM(balance_delta) AS bal
			FROM locker_ledger GROUP BY locker_id
		) le ON le.locker_id = l.id
		WHERE l.is_active = true AND l.cashier_access = true
		ORDER BY l.name`)
	return rows, err
}
```

And after `Service.List`:

```go
func (s *Service) ListForCashier(ctx context.Context) ([]Locker, error) {
	rows, err := s.repo.ListForCashier(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to list lockers", err)
	}
	return rows, nil
}
```

- [ ] **Step 2: Accept the flag on create and update**

Add to `CreateInput` and `UpdateInput`:

```go
	CashierAccess string `form:"cashier_access" json:"cashier_access"`
```

Change `Repository.Create`:

```go
func (r *Repository) Create(ctx context.Context, name, kind string, allowNeg, cashierAccess bool) (*Locker, error) {
	var l Locker
	err := r.q.GetContext(ctx, &l, `
		INSERT INTO lockers (name, kind, allow_negative, cashier_access)
		VALUES ($1, $2, $3, $4) RETURNING *, 0::numeric AS balance`,
		name, kind, allowNeg, cashierAccess)
	if err != nil {
		return nil, err
	}
	return &l, nil
}
```

Change `Repository.Update`:

```go
func (r *Repository) Update(ctx context.Context, id int64, name, kind string, allowNeg, isActive, cashierAccess bool) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE lockers SET name=$1, kind=$2, allow_negative=$3, is_active=$4, cashier_access=$5 WHERE id=$6`,
		name, kind, allowNeg, isActive, cashierAccess, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
```

In `Service.Create`, change the `repo.Create` call to `repo.Create(ctx, name, kind, allowNeg, truthy(in.CashierAccess))`.

In `Service.Update`, change the `s.repo.Update` call to end `…, truthy(in.AllowNegative), truthy(in.IsActive), truthy(in.CashierAccess))`.

- [ ] **Step 3: Filter the cashier's locker list**

In `internal/web/cashier.go`, in `CashierLockers`, change the one listing line:

```go
	rows, err := h.s.lockers.ListForCashier(ctx)
```

and extend the function's doc comment to say it lists only lockers the owner has marked usable by cashiers.

- [ ] **Step 4: Add the switch to the admin locker form**

In `templates/pages/admin/lockers.templ`, find the `allow_negative` checkbox and add immediately after its wrapper `</div>`:

```html
			<div>
				<label class="flex items-start gap-3">
					<input type="checkbox" name="cashier_access" checked?={ l == nil || l.CashierAccess } class="mt-1 w-4 h-4"/>
					<span>
						<span class="text-sm font-medium">Cashiers can use this</span>
						<span class="block text-xs text-slate-500">Lets cashiers move money into or out of it from the till. Turn off for a locker only you should touch.</span>
					</span>
				</label>
			</div>
```

`l == nil ||` makes a **new** locker default to ticked, matching the column default.

If the form's locker variable is not named `l`, use whatever the surrounding template uses.

- [ ] **Step 5: Build and test**

```bash
make templ && go build ./... && make test
```

Expected: green.

- [ ] **Step 6: Verify the filtering live**

```bash
go build -o /tmp/pos ./cmd/server && pkill -f '/tmp/pos'; env $(grep -v '^#' .env | grep -v '^$' | xargs -d '\n') /tmp/pos & sleep 3
curl -s -c /tmp/j.txt -X POST http://localhost:3000/login -d "phone=0000000001&pin=2273" -o /dev/null
curl -s -b /tmp/j.txt -X POST http://localhost:3000/admin/lockers -d "name=Plan Drawer Safe&kind=safe&cashier_access=on&opening_balance=0" -o /dev/null -w "%{http_code}\n"
curl -s -b /tmp/j.txt -X POST http://localhost:3000/admin/lockers -d "name=Plan Owner Safe&kind=safe&opening_balance=0" -o /dev/null -w "%{http_code}\n"
echo "--- what a cashier sees ---"
curl -s -b /tmp/j.txt http://localhost:3000/cashier/lockers
```

Expected: "Plan Drawer Safe" present, "Plan Owner Safe" absent.

Clean up:

```bash
docker compose exec -T postgres psql -U pos_user -d pos_db -c \
  "DELETE FROM locker_ledger WHERE locker_id IN (SELECT id FROM lockers WHERE name LIKE 'Plan %'); DELETE FROM lockers WHERE name LIKE 'Plan %';"
```

- [ ] **Step 7: Commit**

```bash
git add internal/features/lockers/lockers.go internal/web/cashier.go templates/pages/admin/lockers.templ
git commit -m "feat(lockers): hide owner-only lockers from the till

The withdraw dialog listed every active locker, so a cashier could move
money into the owner's safe. ListForCashier is a separate query rather
than a flag on List, so an admin screen can never inherit the filter.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 5: Lift the purchase transactions and remove `PaidAmount`

Behaviour-preserving refactor plus one deliberate removal. After this task nothing can mark a purchase paid — Task 6 restores paying through the real path. **Tasks 5 and 6 must land together in working order**; do not leave the tree between them overnight without finishing.

**Files:**
- Modify: `internal/features/purchases/service.go` — `CreateInput`, `ReceiveInput`, `Create`, `Receive`
- Modify: `internal/web/admin_purchases.go:23-37` (`PurchaseEntryCreate`)

**Interfaces:**
- Produces:
  - `purchases.CreateTx(ctx context.Context, tx *sqlx.Tx, in CreateInput, userID int64) (*Detail, error)` — package-level function; inserts a **received** purchase, books stock, batches, movements, pricing, and adds the **full** total to the supplier's balance.
  - `purchases.ReceiveTx(ctx context.Context, tx *sqlx.Tx, id int64, in ReceiveInput, userID int64) (*Detail, error)` — same for receiving an existing draft.
  - `Service.Create` and `Service.Receive` keep their signatures and now just wrap those in `appdb.WithTx`.
  - `CreateInput.PaidAmount` and `ReceiveInput.PaidAmount` **no longer exist**.

- [ ] **Step 1: Write the failing test**

Create `internal/features/purchases/receive_test.go`:

```go
package purchases

import "testing"

// TestInputsCarryNoPaidAmount is a compile-time guard with teeth: it fails to
// build if anyone reintroduces a paid-amount field on the purchase inputs.
//
// That field used to mark an invoice paid and clear the supplier's balance
// while moving no money at all — no payment row, no receipt, no cash out of any
// drawer. Paying now goes through supplierpay + cashflow in the web layer,
// which this package cannot reach (supplierpay imports purchases).
func TestInputsCarryNoPaidAmount(t *testing.T) {
	var ci CreateInput
	var ri ReceiveInput
	assertNoField(t, ci, "PaidAmount")
	assertNoField(t, ri, "PaidAmount")
}
```

Add the helper in the same file:

```go
import "reflect"

func assertNoField(t *testing.T, v any, field string) {
	t.Helper()
	if _, ok := reflect.TypeOf(v).FieldByName(field); ok {
		t.Fatalf("%T still has a %s field — paying must go through supplierpay so the cash actually moves",
			v, field)
	}
}
```

Merge the two `import` blocks into one containing `reflect` and `testing`.

- [ ] **Step 2: Run it and watch it fail**

```bash
go test ./internal/features/purchases/ -run PaidAmount -v
```

Expected: FAIL — `CreateInput still has a PaidAmount field`.

- [ ] **Step 3: Remove the field and lift the transactions**

In `internal/features/purchases/service.go`:

Delete the `PaidAmount` line from `CreateInput` and from `ReceiveInput`.

Rewrite `Create` as a thin wrapper plus a package-level `CreateTx`. Take the **entire existing body** of the `appdb.WithTx` closure in `Create` and move it into `CreateTx`, with these changes: `paid` becomes `decimal.Zero` (drop the `money.Parse(in.PaidAmount)` block), `receivedStatus(paid, total)` becomes `receivedStatus(decimal.Zero, total)`, and `total.Sub(paid)` in the `applyReceivedLines` call becomes `total`:

```go
// CreateTx records a delivery that had no prior order: a purchase already in
// received state, with stock, batches, movements and pricing applied, and the
// full total added to the supplier's balance.
//
// Paying is deliberately not part of this. It happens in the web layer, which
// composes CreateTx with supplierpay.PayTx and cashflow.MoveTx in one
// transaction — this package cannot import either (supplierpay imports it).
func CreateTx(ctx context.Context, tx *sqlx.Tx, in CreateInput, userID int64) (*Detail, error) {
	discount, err := money.Parse(in.Discount)
	if err != nil || discount.IsNegative() {
		return nil, apperr.Validation("discount must be a non-negative amount")
	}
	dueDate, err := parseDate(in.DueDate)
	if err != nil {
		return nil, err
	}

	repo := NewRepository(tx)
	stk := stock.NewRepository(tx)
	sup := suppliers.NewRepository(tx)

	lines, subtotal, err := parseLines(in.Items)
	if err != nil {
		return nil, err
	}
	total := subtotal.Sub(discount)
	if total.IsNegative() {
		return nil, apperr.Validation("discount exceeds purchase subtotal")
	}

	purchaseID, err := repo.InsertPurchase(ctx, purchaseRow{
		SupplierID: in.SupplierID, InvoiceNo: in.InvoiceNo, Status: receivedStatus(decimal.Zero, total),
		Subtotal: subtotal, Discount: discount, Total: total, Paid: decimal.Zero,
		DueDate: dueDate, ReceivedBy: userID, Notes: in.Notes,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if err := applyReceivedLines(ctx, repo, stk, sup, purchaseID, in.SupplierID, lines, total, userID); err != nil {
		return nil, err
	}
	return loadDetailTx(ctx, repo, purchaseID)
}

// Create is CreateTx in its own transaction.
func (s *Service) Create(ctx context.Context, in CreateInput, userID int64) (*Detail, error) {
	var detail *Detail
	err := appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		d, err := CreateTx(ctx, tx, in, userID)
		if err != nil {
			return err
		}
		detail = d
		return nil
	})
	if err != nil {
		return nil, err
	}
	return detail, nil
}
```

`Service.loadDetail` takes a `*Repository`, so extract its body into a package-level `loadDetailTx(ctx context.Context, repo *Repository, id int64) (*Detail, error)` and have the method call it. This is needed because `CreateTx` and `ReceiveTx` have no receiver.

Apply exactly the same treatment to `Receive` → `ReceiveTx`, dropping its `money.Parse(in.PaidAmount)` block, using `receivedStatus(decimal.Zero, total)` in `repo.UpdateHeader`, passing `Paid: decimal.Zero`, and passing `total` (not `total.Sub(paid)`) to `applyReceivedLines`. Keep the draft-status check, the `KeepRemainder` block and everything else byte-for-byte.

- [ ] **Step 4: Fix the one caller that set a paid amount**

In `internal/web/admin_purchases.go`, `PurchaseEntryCreate` binds `purchases.CreateInput` and posts `paid_amount: "0"` from `static/js/app.js:1787`. Removing the struct field makes the JSON key ignored, so no Go change is needed there — but delete the now-meaningless `paid_amount: "0",` line from `app.js:1787` so the payload stops lying about what it sets.

- [ ] **Step 5: Run the tests**

```bash
go build ./... && go test ./internal/features/purchases/ -v && make test
```

Expected: the guard test PASSES, everything else green.

- [ ] **Step 6: Verify receiving still books goods and now owes the full amount**

```bash
go build -o /tmp/pos ./cmd/server && pkill -f '/tmp/pos'; env $(grep -v '^#' .env | grep -v '^$' | xargs -d '\n') /tmp/pos & sleep 3
curl -s -c /tmp/j.txt -X POST http://localhost:3000/login -d "phone=0000000001&pin=2273" -o /dev/null
curl -s -b /tmp/j.txt -X POST http://localhost:3000/admin/suppliers -d "name=PLAN Supplier&credit_days=30" -o /dev/null
SID=$(docker compose exec -T postgres psql -U pos_user -d pos_db -tAc "SELECT id FROM suppliers WHERE name='PLAN Supplier'")
curl -s -b /tmp/j.txt -X POST http://localhost:3000/admin/purchases -H "Content-Type: application/json" \
  -d "{\"supplier_id\":$SID,\"discount\":\"0\",\"items\":[{\"product_id\":1,\"quantity\":\"10\",\"cost_price\":\"100\",\"selling_price\":\"150\"}]}" -o /dev/null -w "%{http_code}\n"
POID=$(docker compose exec -T postgres psql -U pos_user -d pos_db -tAc "SELECT id FROM purchases ORDER BY id DESC LIMIT 1")
curl -s -b /tmp/j.txt -X POST http://localhost:3000/admin/purchases/$POID/receive -H "Content-Type: application/json" \
  -d '{"invoice_no":"PLAN-1","discount":"0","keep_remainder":false,"items":[{"product_id":1,"quantity":"10","ordered_qty":"10","cost_price":"100","selling_price":"150","expiry_date":""}]}' -o /dev/null -w "%{http_code}\n"
docker compose exec -T postgres psql -U pos_user -d pos_db -c \
  "SELECT p.status, p.total, p.paid_amount, s.outstanding_balance FROM purchases p JOIN suppliers s ON s.id=p.supplier_id WHERE p.id=$POID;"
```

Expected: status `received`, total `1000.00`, paid `0.00`, supplier owed `1000.00`. Stock for product 1 is up by 10.

Record `$SID` and `$POID` — Task 6 pays this invoice. Do not clean up yet.

- [ ] **Step 7: Commit**

```bash
git add internal/features/purchases/service.go internal/features/purchases/receive_test.go static/js/app.js
git commit -m "refactor(purchases): lift the transaction out, drop PaidAmount

CreateTx/ReceiveTx let the web layer compose goods + payment + cash
movement in one transaction. purchases still imports neither cashflow
nor supplierpay, and cannot — supplierpay imports it.

PaidAmount is gone from both inputs. It marked an invoice paid and
cleared the supplier's balance while moving no money whatsoever. A test
fails the build if anyone puts it back. Paying is restored through the
real path in the next commit.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 6: Pay for real — the shared helper, and the admin receive screen

**Files:**
- Create: `internal/web/supplier_pay_shared.go`
- Create: `internal/web/supplier_money_test.go`
- Modify: `internal/web/admin_more.go:172-265` (`SupplierPay` calls the helper)
- Modify: `internal/web/admin_purchases.go:134-153` (`PurchaseReceive` composes payment)
- Modify: `templates/pages/admin/purchases.templ:318-321`
- Modify: `static/js/app.js` — `grnReceive` (`:1805-1920`)

**Interfaces:**
- Consumes: `purchases.ReceiveTx`, `purchases.CreateTx` (Task 5); `supplierpay.PayTx`; `cashflow.MoveTx`; `parseLocation`.
- Produces:
  - `type payRequest struct { SupplierID int64; SupplierName string; In supplierpay.PayInput; Source cashflow.Location }`
  - `(s *Server) paySupplierTx(ctx context.Context, tx *sqlx.Tx, req payRequest, userID int64) (*supplierpay.Result, *cashflow.Receipt, error)`

- [ ] **Step 1: Write the failing money-trail test**

Create `internal/web/supplier_money_test.go`. It follows `internal/db/plugin_migrate_test.go`: skipped unless `DATABASE_URL` is set.

```go
package web

import (
	"context"
	"os"
	"testing"

	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/purchases"
	"karots-pos/internal/features/supplierpay"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// TestReceiveAndPayMovesMoney is the assertion that would have caught the
// defect this work exists to fix: receiving with a payment used to mark the
// invoice paid and clear the supplier's balance while producing no payment
// record, no receipt, and no cash leaving any drawer.
//
// Everything happens inside a transaction that is rolled back, so the dev
// database is untouched.
func TestReceiveAndPayMovesMoney(t *testing.T) {
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

	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // the whole point: leave no trace

	// Arrange: a supplier, a product, and a locker to pay from.
	var supplierID int64
	must(t, tx.GetContext(ctx, &supplierID,
		`INSERT INTO suppliers (name) VALUES ('TEST money trail') RETURNING id`))
	var productID int64
	must(t, tx.GetContext(ctx, &productID, `SELECT id FROM products WHERE is_active LIMIT 1`))
	var lockerID int64
	must(t, tx.GetContext(ctx, &lockerID,
		`INSERT INTO lockers (name, kind) VALUES ('TEST safe', 'safe') RETURNING id`))
	must(t, seedLockerBalance(ctx, tx, lockerID, decimal.NewFromInt(5000)))

	// Act: receive 10 @ 100, then pay the whole 1000 from the locker.
	detail, err := purchases.CreateTx(ctx, tx, purchases.CreateInput{
		SupplierID: supplierID,
		Discount:   "0",
		Items: []purchases.ItemInput{{
			ProductID: productID, Quantity: "10", CostPrice: "100", SellingPrice: "150",
		}},
	}, 1)
	if err != nil {
		t.Fatalf("receiving the delivery: %v", err)
	}
	total := detail.Purchase.Total

	// PayTx and MoveTx are methods, but both take the transaction explicitly and
	// neither reads the service's own db handle — verified before writing this.
	// cashflow's sales dependency is unused on this path, hence nil.
	payer := supplierpay.NewService(conn)
	mover := cashflow.NewService(conn, nil)

	res, err := payer.PayTx(ctx, tx, supplierID, supplierpay.PayInput{
		Method:      "cash",
		Allocations: []supplierpay.Alloc{{PurchaseID: detail.Purchase.ID, Amount: total}},
	}, 1)
	if err != nil {
		t.Fatalf("paying: %v", err)
	}
	rec, err := mover.MoveTx(ctx, tx, cashflow.MoveInput{
		From:        cashflow.Locker(lockerID),
		To:          cashflow.External(),
		Amount:      res.Total,
		Reason:      "supplier payment: TEST money trail",
		ReceiptKind: "supplier_payment",
		Party:       "TEST money trail",
		Ref:         &cashflow.Ref{Kind: "supplier_payment", ID: res.PaymentID},
		ActorID:     1,
	})
	if err != nil {
		t.Fatalf("moving the cash: %v", err)
	}

	// Assert: every one of these was zero before this work.
	var payments int
	must(t, tx.GetContext(ctx, &payments,
		`SELECT count(*) FROM supplier_payments WHERE supplier_id = $1`, supplierID))
	if payments != 1 {
		t.Errorf("supplier_payments rows = %d, want 1", payments)
	}

	var receipts int
	must(t, tx.GetContext(ctx, &receipts, `SELECT count(*) FROM money_receipts WHERE id = $1`, rec.ID))
	if receipts != 1 {
		t.Errorf("money_receipts rows = %d, want 1", receipts)
	}

	var lockerDelta decimal.Decimal
	must(t, tx.GetContext(ctx, &lockerDelta,
		`SELECT COALESCE(SUM(balance_delta),0) FROM locker_ledger WHERE locker_id = $1 AND ref_kind = 'supplier_payment'`,
		lockerID))
	if !lockerDelta.Equal(total.Neg()) {
		t.Errorf("locker moved by %s, want %s — the cash must actually leave", lockerDelta, total.Neg())
	}

	var paid, owed decimal.Decimal
	must(t, tx.GetContext(ctx, &paid, `SELECT paid_amount FROM purchases WHERE id = $1`, detail.Purchase.ID))
	must(t, tx.GetContext(ctx, &owed, `SELECT outstanding_balance FROM suppliers WHERE id = $1`, supplierID))
	if !paid.Equal(total) {
		t.Errorf("purchase paid_amount = %s, want %s", paid, total)
	}
	if !owed.IsZero() {
		t.Errorf("supplier still owed %s, want 0", owed)
	}
}

// TestReceiveWithoutPayingOwesEverything is the other half: receiving alone
// must leave the full amount owed and move nothing.
func TestReceiveWithoutPayingOwesEverything(t *testing.T) {
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

	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck

	var supplierID int64
	must(t, tx.GetContext(ctx, &supplierID,
		`INSERT INTO suppliers (name) VALUES ('TEST unpaid') RETURNING id`))
	var productID int64
	must(t, tx.GetContext(ctx, &productID, `SELECT id FROM products WHERE is_active LIMIT 1`))

	detail, err := purchases.CreateTx(ctx, tx, purchases.CreateInput{
		SupplierID: supplierID,
		Discount:   "0",
		Items: []purchases.ItemInput{{
			ProductID: productID, Quantity: "4", CostPrice: "25", SellingPrice: "40",
		}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}

	var owed decimal.Decimal
	must(t, tx.GetContext(ctx, &owed, `SELECT outstanding_balance FROM suppliers WHERE id = $1`, supplierID))
	if !owed.Equal(detail.Purchase.Total) {
		t.Errorf("owed %s after receiving, want the full %s", owed, detail.Purchase.Total)
	}

	var moves int
	must(t, tx.GetContext(ctx, &moves,
		`SELECT count(*) FROM supplier_payments WHERE supplier_id = $1`, supplierID))
	if moves != 0 {
		t.Errorf("receiving alone created %d payments, want 0", moves)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// seedLockerBalance gives a fresh locker an opening balance to pay out of.
//
// The kind must be 'open_balance': locker_ledger_kind_check allows only
// open_balance, transfer, payment, intake, bank_charge, interest and adjust.
func seedLockerBalance(ctx context.Context, tx *sqlx.Tx, lockerID int64, amount decimal.Decimal) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO locker_ledger (locker_id, balance_delta, kind, note)
		 VALUES ($1, $2, 'open_balance', 'test seed')`, lockerID, amount)
	return err
}
```

Signatures verified while writing this plan: `(*supplierpay.Service).PayTx(ctx, tx, supplierID, PayInput, userID)` and `(*cashflow.Service).MoveTx(ctx, tx, MoveInput)`. If Task 5 shifted anything, adjust the calls — never the assertions.

- [ ] **Step 2: Run it and watch it fail**

```bash
go test ./internal/web/ -run 'MovesMoney|OwesEverything' -v
```

Expected: FAIL to compile or FAIL on the assertions, depending on Task 5's exact shape. Do not proceed until you have seen it fail for a reason you understand.

- [ ] **Step 3: Write the shared payment helper**

Create `internal/web/supplier_pay_shared.go`:

```go
package web

import (
	"context"

	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/supplierpay"

	"github.com/jmoiron/sqlx"
)

// payRequest is one supplier payment, already parsed and validated by the
// caller. Source is only read for cash — card and online payments record the
// payment without touching a drawer.
type payRequest struct {
	SupplierID   int64
	SupplierName string
	In           supplierpay.PayInput
	Source       cashflow.Location
}

// paySupplierTx records a supplier payment and moves the cash, inside the
// caller's transaction.
//
// It exists so the admin screen and the till run the same code. They differ
// only in which cash sources they offer and which URL the print prompt points
// at — never in what gets written.
//
// Returns the payment result and, for cash, the money receipt. A nil receipt
// means a non-cash method, not a failure.
func (s *Server) paySupplierTx(ctx context.Context, tx *sqlx.Tx, req payRequest, userID int64) (*supplierpay.Result, *cashflow.Receipt, error) {
	res, err := s.supplierPay.PayTx(ctx, tx, req.SupplierID, req.In, userID)
	if err != nil {
		return nil, nil, err
	}
	if res.Method != "cash" {
		return res, nil, nil
	}
	rec, err := s.cashflow.MoveTx(ctx, tx, cashflow.MoveInput{
		From:        req.Source,
		To:          cashflow.External(),
		Amount:      res.Total,
		Reason:      "supplier payment: " + req.SupplierName,
		ReceiptKind: "supplier_payment",
		Party:       req.SupplierName,
		Ref:         &cashflow.Ref{Kind: "supplier_payment", ID: res.PaymentID},
		ActorID:     userID,
	})
	if err != nil {
		return nil, nil, err
	}
	return res, rec, nil
}
```

- [ ] **Step 4: Point the admin pay handler at it**

In `internal/web/admin_more.go`, replace the `appdb.WithTx` block inside `SupplierPay` (the one containing `PayTx` and `MoveTx`) with:

```go
	var res *supplierpay.Result
	var rec *cashflow.Receipt
	err = appdb.WithTx(ctx, a.db, func(tx *sqlx.Tx) error {
		r, k, txErr := a.s.paySupplierTx(ctx, tx, payRequest{
			SupplierID: id, SupplierName: name, In: in, Source: src,
		}, userID)
		res, rec = r, k
		return txErr
	})
```

Everything after it (audit log, `afterMoneyMove`, `htmxDone`) is unchanged.

- [ ] **Step 5: Compose payment into admin receiving**

In `internal/web/admin_purchases.go`, rewrite `PurchaseReceive`:

```go
// PurchaseReceive takes in a delivery against a draft order, optionally paying
// the supplier in the same breath.
//
// Goods and payment are one transaction: if the payment fails the goods roll
// back with it, so stock can never land without its payable. Paying goes
// through supplierpay + cashflow so a real payment row, a real drawer movement
// and a real receipt all exist — the field this replaced did none of that.
func (a *adminUI) PurchaseReceive(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in purchases.ReceiveInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	pay, err := parsePayNow(c)
	if err != nil {
		return err
	}
	userID := middleware.CurrentUserID(c)

	var d *purchases.Detail
	var rec *cashflow.Receipt
	err = appdb.WithTx(ctx, a.db, func(tx *sqlx.Tx) error {
		got, txErr := purchases.ReceiveTx(ctx, tx, id, in, userID)
		if txErr != nil {
			return txErr
		}
		d = got
		if !pay.amount.IsPositive() {
			return nil
		}
		name := ""
		if sup, gerr := a.s.suppliers.Get(ctx, d.Purchase.SupplierID); gerr == nil {
			name = sup.Name
		}
		_, k, txErr := a.s.paySupplierTx(ctx, tx, payRequest{
			SupplierID:   d.Purchase.SupplierID,
			SupplierName: name,
			In: supplierpay.PayInput{
				Method:      pay.method,
				Allocations: []supplierpay.Alloc{{PurchaseID: id, Amount: pay.amount}},
			},
			Source: pay.source,
		}, userID)
		rec = k
		return txErr
	})
	if err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionUpdate, "purchase", strconv.FormatInt(id, 10), "received purchase order")
	if rec != nil {
		a.s.printMoneyReceipt(ctx, rec)
	}
	return response.OK(c, d)
}
```

Add the parser at the bottom of the same file:

```go
// payNow is the optional "paying the supplier now" part of a receive form.
type payNow struct {
	amount decimal.Decimal
	method string
	source cashflow.Location
}

// parsePayNow reads the payment block from a receive request. A blank or zero
// amount means the goods are taken in on account and nothing is paid.
func parsePayNow(c echo.Context) (payNow, error) {
	raw := strings.TrimSpace(c.FormValue("pay_amount"))
	if raw == "" || raw == "0" {
		return payNow{amount: decimal.Zero}, nil
	}
	amt, err := money.Parse(raw)
	if err != nil || amt.IsNegative() {
		return payNow{}, apperr.Validation("payment amount must be a non-negative number")
	}
	method, ok := normSupplierMethod(c.FormValue("pay_method"))
	if !ok {
		return payNow{}, apperr.Validation("invalid payment method")
	}
	out := payNow{amount: amt, method: method}
	if method == "cash" {
		src, perr := parseLocation(c.FormValue("pay_source"))
		if perr != nil {
			return payNow{}, perr
		}
		out.source = src
	}
	return out, nil
}
```

`c.Bind` into a struct consumes the body for JSON requests, so `grnReceive` must post the payment fields as **form values** alongside a JSON body, or the handler must read them from the already-bound struct. Simplest and consistent with the rest of the admin screens: extend `grnReceive` to post `application/x-www-form-urlencoded` for the payment fields via a second parameter, **or** add `PayAmount`, `PayMethod`, `PaySource` as string fields on `purchases.ReceiveInput`. Prefer the latter **only if** they are documented as web-layer passthrough that the package itself never reads — otherwise the trap returns. Decide when implementing and record the choice in the commit message.

- [ ] **Step 6: Replace the Paid Amount box**

In `templates/pages/admin/purchases.templ`, replace the Paid Amount block (lines 318-321) with:

```html
				<div>
					<label class="block text-sm font-medium mb-1">Paying now?</label>
					<input type="number" step="0.01" min="0" x-model.number="payAmount" class="w-full border rounded-lg px-3 py-2" placeholder="0.00"/>
					<p class="text-xs text-slate-500">Leave blank to take the goods on account.</p>
				</div>
				<div x-show="payAmount > 0">
					<label class="block text-sm font-medium mb-1">Method</label>
					<select x-model="payMethod" class="w-full border rounded-lg px-3 py-2">
						<option value="cash">Cash</option>
						<option value="card">Card</option>
						<option value="online">Online</option>
					</select>
				</div>
				<div x-show="payAmount > 0 && payMethod === 'cash'">
					<label class="block text-sm font-medium mb-1">Cash comes from</label>
					<select x-model="paySource" class="w-full border rounded-lg px-3 py-2">
						<template x-for="opt in sources" :key="opt.value">
							<option :value="opt.value" x-text="opt.label"></option>
						</template>
					</select>
				</div>
```

The page must pass the cash sources into `grnReceive`'s config, using the existing `cashLocationChoices` on `*adminUI`.

In `static/js/app.js`, in `grnReceive`, replace `paid: 0,` with:

```js
    payAmount: 0,
    payMethod: "cash",
    paySource: "",
    sources: config.sources || [],
```

and in `submit()` replace `paid_amount: String(this.paid || 0),` with:

```js
          pay_amount: String(this.payAmount || 0),
          pay_method: this.payMethod,
          pay_source: this.paySource,
```

- [ ] **Step 7: Run the tests**

```bash
make templ && make css && go build ./... && go test ./internal/web/ -run 'MovesMoney|OwesEverything' -v && make test
```

Expected: both money tests PASS, whole suite green.

- [ ] **Step 8: Re-run the original live proof**

Pay the invoice from Task 5 through the admin screen and confirm every column that read zero now reads correctly:

```bash
go build -o /tmp/pos ./cmd/server && pkill -f '/tmp/pos'; env $(grep -v '^#' .env | grep -v '^$' | xargs -d '\n') /tmp/pos & sleep 3
curl -s -c /tmp/j.txt -X POST http://localhost:3000/login -d "phone=0000000001&pin=2273" -o /dev/null
SID=$(docker compose exec -T postgres psql -U pos_user -d pos_db -tAc "SELECT id FROM suppliers WHERE name='PLAN Supplier'")
POID=$(docker compose exec -T postgres psql -U pos_user -d pos_db -tAc "SELECT id FROM purchases WHERE supplier_id=$SID ORDER BY id DESC LIMIT 1")
LID=$(docker compose exec -T postgres psql -U pos_user -d pos_db -tAc "SELECT id FROM lockers WHERE is_active LIMIT 1")
curl -s -b /tmp/j.txt -X POST http://localhost:3000/admin/suppliers/$SID/payment \
  --data-urlencode "method=cash" --data-urlencode "source=locker:$LID" --data-urlencode "alloc_$POID=1000" -o /dev/null -w "%{http_code}\n"
docker compose exec -T postgres psql -U pos_user -d pos_db -c "
SELECT (SELECT count(*) FROM supplier_payments WHERE supplier_id=$SID) AS payments,
       (SELECT count(*) FROM money_receipts WHERE ref_kind='supplier_payment')  AS receipts,
       (SELECT COALESCE(SUM(balance_delta),0) FROM locker_ledger WHERE locker_id=$LID AND ref_kind='supplier_payment') AS locker_delta,
       (SELECT paid_amount FROM purchases WHERE id=$POID) AS paid,
       (SELECT outstanding_balance FROM suppliers WHERE id=$SID) AS owed;"
```

Expected: payments `1`, receipts `1`, locker_delta `-1000.00`, paid `1000.00`, owed `0.00`. Every one of these was `0` in the audit that motivated this work.

Clean up the test data and restore product 1:

```bash
docker compose exec -T postgres psql -U pos_user -d pos_db <<SQL
BEGIN;
DELETE FROM supplier_payment_allocations WHERE payment_id IN (SELECT id FROM supplier_payments WHERE supplier_id=$SID);
DELETE FROM supplier_payments WHERE supplier_id=$SID;
DELETE FROM money_receipts WHERE ref_kind='supplier_payment';
DELETE FROM locker_ledger WHERE ref_kind='supplier_payment';
DELETE FROM stock_batches WHERE purchase_item_id IN (SELECT id FROM purchase_items WHERE purchase_id=$POID);
DELETE FROM stock_movements WHERE reference_type='purchase' AND reference_id=$POID;
DELETE FROM purchase_items WHERE purchase_id=$POID;
DELETE FROM purchases WHERE supplier_id=$SID;
DELETE FROM suppliers WHERE id=$SID;
UPDATE products SET cost_price='0.00', selling_price='750.00' WHERE id=1;
UPDATE stock SET quantity='1.000000' WHERE product_id=1;
COMMIT;
SQL
```

- [ ] **Step 9: Commit**

```bash
git add internal/web/supplier_pay_shared.go internal/web/supplier_money_test.go internal/web/admin_more.go internal/web/admin_purchases.go templates/pages/admin/purchases.templ static/js/app.js
git commit -m "fix(purchases): paying a supplier now actually moves the money

The receive form's Paid Amount marked an invoice paid and cleared the
supplier's balance while producing no payment record, no receipt and no
cash out of any drawer. The owner handed over notes and the till still
counted them as present, so it closed short with no explanation.

Receiving and paying are now one transaction through supplierpay and
cashflow: if the payment fails the goods roll back with it. One shared
helper serves admin and the till, so they cannot drift.

Two DB-guarded tests assert exactly the columns that read zero before.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 7: The Suppliers tab and paying at the counter

**Files:**
- Create: `internal/web/cashier_suppliers.go`
- Create: `templates/pages/cashier/suppliers.templ`
- Modify: `templates/layouts/cashier.templ:7-49`
- Modify: `internal/web/web.go` (routes)

**Interfaces:**
- Consumes: `middleware.RequireSupplierAccess`, `middleware.CanHandleSuppliersCtx`, `middleware.MaySeeSuppliers` (Task 2); `lockers.Service.ListForCashier` (Task 4); `(*Server).paySupplierTx` (Task 6).
- Produces: routes `GET /cashier/suppliers`, `GET /cashier/suppliers/table`, `GET /cashier/suppliers/pay/:id`, `POST /cashier/suppliers/:id/payment`.

- [ ] **Step 1: Add the tab, hidden by default**

In `templates/layouts/cashier.templ`, add `"karots-pos/internal/middleware"` to the imports, and inside the `<nav>` after the warranty tab:

```html
							if middleware.MaySeeSuppliers(role, middleware.CanHandleSuppliersCtx(ctx)) {
								@cashierTab("/cashier/suppliers", "Suppliers", active == "suppliers")
							}
```

`ctx` is in scope inside every templ component. Add the same condition around a new palette entry in `cashierPalette` — that function takes only `role`, so give it a second parameter `canSuppliers bool` and pass `middleware.CanHandleSuppliersCtx(ctx)` at its one call site inside the layout.

- [ ] **Step 2: Wire the routes**

In `internal/web/web.go`, after the warranty routes in the `cg` group:

```go
	// Supplier counter (per-user permission; admins/managers always pass).
	sg := cg.Group("/suppliers", middleware.RequireSupplierAccess())
	sg.GET("", cashier.Suppliers)
	sg.GET("/table", cashier.SuppliersTable)
	sg.GET("/pay/:id", cashier.SupplierPayForm)
	sg.POST("/:id/payment", cashier.SupplierPayAtCounter)
```

- [ ] **Step 3: Write the handlers**

Create `internal/web/cashier_suppliers.go` with `Suppliers` (renders the page with the supplier list), `SuppliersTable` (the HTMX search fragment), `SupplierPayForm` (open invoices + cash sources) and `SupplierPayAtCounter`.

`SupplierPayAtCounter` mirrors `admin_more.go`'s `SupplierPay` but with two differences. Cash sources come from the till plus cashier-accessible lockers:

```go
// cashierCashSources lists where a cashier may take cash from: their own open
// drawer first, then the lockers the owner has marked usable by cashiers.
func (h *cashierUI) cashierCashSources(ctx context.Context, userID int64, userName string) ([]adminfragments.LocationChoice, error) {
	sym := h.s.symbol(ctx)
	out := []adminfragments.LocationChoice{{
		Value: "till:" + strconv.FormatInt(userID, 10),
		Label: "My drawer — " + userName,
		Group: "Till",
	}}
	rows, err := h.s.lockers.ListForCashier(ctx)
	if err != nil {
		return nil, err
	}
	for _, l := range rows {
		out = append(out, adminfragments.LocationChoice{
			Value: "locker:" + strconv.FormatInt(l.ID, 10),
			Label: l.Name + " (" + money.Format(sym, l.Balance) + ")",
			Group: "Lockers",
		})
	}
	return out, nil
}
```

and the print policy follows `CreditPay` rather than `afterMoneyMove`, because `/admin/money-receipts/...` is unreachable for a cashier:

```go
	cfg, _ := h.s.settings.Get(ctx)
	msg := "Paid " + money.Display(res.Total) + " to " + name
	if rec != nil && cfg != nil && cfg.AskToPrint {
		printURL := "/cashier/money-receipts/" + strconv.FormatInt(rec.ID, 10) + "/print"
		c.Response().Header().Set("HX-Trigger",
			response.PrintPrompt(msg+" · "+rec.ReceiptNo, printURL, false, "reload-suppliers", "close-modal"))
		return c.NoContent(200)
	}
	if rec != nil {
		h.s.printMoneyReceipt(ctx, rec)
	}
	return htmxDone(c, msg, "reload-suppliers")
```

The allocation parsing (`alloc_<id>` form values, the unallocated fallback) is identical to `admin_more.go:189-214`; reuse it by extracting that loop into a shared `parseAllocations(c echo.Context, invoices []purchases.Purchase) (supplierpay.PayInput, error)` in `supplier_pay_shared.go` and calling it from both handlers. Do not copy it.

- [ ] **Step 4: Write the page**

Create `templates/pages/cashier/suppliers.templ` following `templates/pages/cashier/more.templ`'s Credit page: a `SuppliersData` struct carrying `CashierName`, `Role`, `ShowChangePin` and the supplier rows, wrapped in `@layouts.Cashier("Suppliers", d.CashierName, d.Role, "suppliers", d.ShowChangePin)`. Each row shows the supplier name, phone and amount owed, with **Pay**, **Receive** and **Order** buttons. Receive and Order open in Tasks 8 and 9 — render them disabled with `title="Coming next"` for now.

- [ ] **Step 5: Build and test**

```bash
make templ && make css && go build ./... && make test
```

Expected: green.

- [ ] **Step 6: Verify the gate live**

```bash
go build -o /tmp/pos ./cmd/server && pkill -f '/tmp/pos'; env $(grep -v '^#' .env | grep -v '^$' | xargs -d '\n') /tmp/pos & sleep 3
# The Task 3 test cashier, currently NOT trusted:
curl -s -c /tmp/c.txt -X POST http://localhost:3000/login -d "phone=0777000111&pin=4321" -o /dev/null
curl -s -b /tmp/c.txt -o /dev/null -w "plain cashier: %{http_code}\n" http://localhost:3000/cashier/suppliers
curl -s -b /tmp/c.txt http://localhost:3000/cashier | grep -c 'cashier/suppliers' || echo "tab hidden: correct"
# Trust them, then retry — no re-login, the flag is read per request:
docker compose exec -T postgres psql -U pos_user -d pos_db -c \
  "UPDATE users SET can_handle_suppliers = true WHERE phone='0777000111';"
curl -s -b /tmp/c.txt -o /dev/null -w "trusted cashier: %{http_code}\n" http://localhost:3000/cashier/suppliers
curl -s -b /tmp/c.txt http://localhost:3000/cashier | grep -c 'cashier/suppliers'
```

Expected: `403` then `200`, tab count `0` then `1`, **with no re-login in between**. That is the whole point of reading the flag per request.

- [ ] **Step 7: Commit**

```bash
git add internal/web/cashier_suppliers.go internal/web/supplier_pay_shared.go internal/web/web.go templates/pages/cashier/suppliers.templ templates/layouts/cashier.templ
git commit -m "feat(till): pay a supplier at the counter

The cashier route group had no supplier capability at all, so a supplier
asking for money while the cashier was alone meant calling the owner
away or recording nothing — and cash handed over off-system closes the
till short.

Cash comes from the cashier's own drawer by default; cashflow already
refuses when no session is open. Other lockers appear only if the owner
marked them usable by cashiers.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 8: Receiving a delivery at the counter

**Files:**
- Modify: `internal/web/cashier_suppliers.go` (add the receive handlers)
- Modify: `templates/pages/cashier/suppliers.templ` (the receive screen)
- Modify: `internal/web/web.go` (routes)

**Interfaces:**
- Consumes: `purchases.CreateTx`, `purchases.ReceiveTx` (Task 5); `(*Server).paySupplierTx`, `parsePayNow` (Task 6); `cashierCashSources` (Task 7).
- Produces: routes `GET /cashier/suppliers/:id/receive`, `POST /cashier/suppliers/:id/receive`, `POST /cashier/purchases/:id/receive`.

- [ ] **Step 1: Add the routes**

In the `sg` group:

```go
	sg.GET("/:id/receive", cashier.ReceiveForm)      // walk-in delivery, no prior order
	sg.POST("/:id/receive", cashier.ReceiveWalkIn)   // → purchases.CreateTx
	sg.GET("/orders/:poID/receive", cashier.ReceiveAgainstOrderForm)
	sg.POST("/orders/:poID/receive", cashier.ReceiveAgainstOrder) // → purchases.ReceiveTx
```

- [ ] **Step 2: Write the walk-in handler**

In `internal/web/cashier_suppliers.go`:

```go
// ReceiveWalkIn takes in a delivery that was never ordered — the supplier who
// simply turns up with goods. Goods and any payment are one transaction, so a
// failed payment never leaves stock on the shelf without its payable.
func (h *cashierUI) ReceiveWalkIn(c echo.Context) error {
	ctx := c.Request().Context()
	supplierID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in purchases.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	in.SupplierID = supplierID // never trust the body for who this is owed to
	if err := c.Validate(&in); err != nil {
		return err
	}
	pay, err := parsePayNow(c)
	if err != nil {
		return err
	}
	userID := middleware.CurrentUserID(c)
	sup, err := h.s.suppliers.Get(ctx, supplierID)
	if err != nil {
		return err
	}

	var d *purchases.Detail
	var rec *cashflow.Receipt
	err = appdb.WithTx(ctx, h.s.db, func(tx *sqlx.Tx) error {
		got, txErr := purchases.CreateTx(ctx, tx, in, userID)
		if txErr != nil {
			return txErr
		}
		d = got
		if !pay.amount.IsPositive() {
			return nil
		}
		_, k, txErr := h.s.paySupplierTx(ctx, tx, payRequest{
			SupplierID:   supplierID,
			SupplierName: sup.Name,
			In: supplierpay.PayInput{
				Method:      pay.method,
				Allocations: []supplierpay.Alloc{{PurchaseID: d.Purchase.ID, Amount: pay.amount}},
			},
			Source: pay.source,
		}, userID)
		rec = k
		return txErr
	})
	if err != nil {
		return err
	}
	h.s.logAudit(c, audit.ActionCreate, "purchase", strconv.FormatInt(d.Purchase.ID, 10),
		"received a walk-in delivery from "+sup.Name)
	if rec != nil {
		h.s.printMoneyReceipt(ctx, rec)
	}
	return htmxDone(c, "Goods received from "+sup.Name, "reload-suppliers")
}
```

`ReceiveAgainstOrder` is the same shape with `purchases.ReceiveTx(ctx, tx, poID, in, userID)` and a `purchases.ReceiveInput` bind, and it reads the supplier id from the loaded draft rather than the URL.

- [ ] **Step 3: Write the receive screen**

Add to `templates/pages/cashier/suppliers.templ` a receive form reusing the admin entry screen's Alpine component and the product search picker from `templates/fragments/admin/pickers.templ`. It needs: a product picker per line, quantity, cost, selling price with the existing margin warning, then the same "Paying now?" block as Task 6 with `cashierCashSources` as the source list.

If the draft list for that supplier is non-empty, show a "Receiving against an order?" selector at the top that switches the form to the order's lines.

- [ ] **Step 4: Build and test**

```bash
make templ && make css && go build ./... && make test
```

Expected: green.

- [ ] **Step 5: Verify a counter delivery with payment**

Log in as the trusted cashier from Task 7, open a till session, then receive a walk-in delivery paying half:

```bash
curl -s -c /tmp/c.txt -X POST http://localhost:3000/login -d "phone=0777000111&pin=4321" -o /dev/null
curl -s -b /tmp/c.txt -X POST http://localhost:3000/api/cash-register/open -H "Content-Type: application/json" -d '{"opening_cash":"2000"}' -o /dev/null -w "till open: %{http_code}\n"
SID=$(docker compose exec -T postgres psql -U pos_user -d pos_db -tAc "SELECT id FROM suppliers ORDER BY id LIMIT 1")
UID=$(docker compose exec -T postgres psql -U pos_user -d pos_db -tAc "SELECT id FROM users WHERE phone='0777000111'")
curl -s -b /tmp/c.txt -X POST "http://localhost:3000/cashier/suppliers/$SID/receive" \
  --data-urlencode "discount=0" --data-urlencode "pay_amount=500" --data-urlencode "pay_method=cash" \
  --data-urlencode "pay_source=till:$UID" \
  --data-urlencode 'items[0][product_id]=1' --data-urlencode 'items[0][quantity]=10' \
  --data-urlencode 'items[0][cost_price]=100' --data-urlencode 'items[0][selling_price]=150' \
  -o /dev/null -w "receive: %{http_code}\n"
docker compose exec -T postgres psql -U pos_user -d pos_db -c "
SELECT p.total, p.paid_amount, p.status,
       (SELECT count(*) FROM supplier_payments WHERE supplier_id=p.supplier_id) AS payments,
       (SELECT COALESCE(SUM(amount),0) FROM cash_movements WHERE type='out') AS cash_out
FROM purchases p ORDER BY p.id DESC LIMIT 1;"
```

Expected: total `1000.00`, paid `500.00`, status `partial`, one payment row, and Rs 500 out of the drawer. Then confirm the Z-report for that session balances.

Clean up as in Task 6 and close the till.

- [ ] **Step 6: Commit**

```bash
git add internal/web/cashier_suppliers.go templates/pages/cashier/suppliers.templ internal/web/web.go
git commit -m "feat(till): take in a delivery at the counter

Covers the supplier who simply turns up with goods and no prior order,
which is why this uses CreateTx rather than chaining draft-then-receive.
Paying on the spot is the same transaction as the goods.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 9: Placing an order at the counter

**Files:**
- Modify: `internal/web/cashier_suppliers.go`
- Modify: `templates/pages/cashier/suppliers.templ`
- Modify: `internal/web/web.go`

**Interfaces:**
- Consumes: `purchases.Service.CreateDraft`.
- Produces: routes `GET /cashier/suppliers/:id/order`, `POST /cashier/suppliers/:id/order`.

- [ ] **Step 1: Add the routes and handler**

```go
	sg.GET("/:id/order", cashier.OrderForm)
	sg.POST("/:id/order", cashier.OrderCreate)
```

```go
// OrderCreate records what the supplier should send next time as a normal draft
// purchase order, stamped with the cashier who took it. The phone call to the
// owner is the approval, so there is no second confirmation step — the draft
// simply appears in the owner's Purchases list.
func (h *cashierUI) OrderCreate(c echo.Context) error {
	ctx := c.Request().Context()
	supplierID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in purchases.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	in.SupplierID = supplierID
	if err := c.Validate(&in); err != nil {
		return err
	}
	d, err := h.s.purchases.CreateDraft(ctx, in, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	h.s.logAudit(c, audit.ActionCreate, "purchase", strconv.FormatInt(d.Purchase.ID, 10),
		"took a supplier order at the counter")
	return response.Created(c, map[string]any{
		"id":        d.Purchase.ID,
		"print_url": "/admin/purchases/po/print?ids=" + strconv.FormatInt(d.Purchase.ID, 10),
	})
}
```

The PO print route lives under `/admin` and a cashier cannot reach it. Either add `cg.GET("/suppliers/orders/print", cashier.OrderPrint)` delegating to the same `admin.DraftPOPrint` logic, or move that handler onto `*Server` and register it in both groups. Do the latter — one handler, two registrations.

- [ ] **Step 2: Write the order screen**

A trimmed version of the receive form: product picker, quantity, and an optional expected date. No cost or selling price — this is a request, not a receipt. On save, open the printable PO in a new tab so the supplier can take it.

- [ ] **Step 3: Build and test**

```bash
make templ && make css && go build ./... && make test
```

Expected: green.

- [ ] **Step 4: Verify**

```bash
curl -s -b /tmp/c.txt -X POST "http://localhost:3000/cashier/suppliers/$SID/order" \
  --data-urlencode "discount=0" \
  --data-urlencode 'items[0][product_id]=1' --data-urlencode 'items[0][quantity]=24' \
  --data-urlencode 'items[0][cost_price]=0' --data-urlencode 'items[0][selling_price]=0' \
  -o /dev/null -w "order: %{http_code}\n"
docker compose exec -T postgres psql -U pos_user -d pos_db -c \
  "SELECT id, status, received_by FROM purchases ORDER BY id DESC LIMIT 1;"
curl -s -b /tmp/c.txt -o /dev/null -w "PO print: %{http_code}\n" "http://localhost:3000/cashier/suppliers/orders/print?ids=$POID"
```

Expected: `201`, status `draft`, `received_by` = the cashier's id, and the print page returns `200` for a cashier.

Delete the draft afterwards.

- [ ] **Step 5: Commit**

```bash
git add internal/web/cashier_suppliers.go templates/pages/cashier/suppliers.templ internal/web/web.go
git commit -m "feat(till): take a supplier order at the counter

Becomes a normal draft PO stamped with the cashier who took it, and
prints a slip for the supplier to take away.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 10: Full-journey verification and documentation

**Files:**
- Modify: `docs/` — whichever page documents cashier capabilities and user roles
- No code changes expected; fix anything this task uncovers

- [ ] **Step 1: Run the whole suite with a database**

```bash
export DATABASE_URL=$(grep '^DATABASE_URL' .env | cut -d= -f2-)
make test && go test ./internal/web/ -run 'MovesMoney|OwesEverything' -v
```

Expected: green, and the two money tests actually **run** rather than skip. If they skip, `DATABASE_URL` did not reach the test.

- [ ] **Step 2: Walk the full counter journey in a browser**

As the trusted cashier: open the till, take in a delivery paying part of it, pay the rest from the Suppliers screen, place an order and print it, then close the till.

Confirm: stock rose by the delivered quantity; the supplier's balance is zero; the drawer fell by exactly the total paid; two CR- receipts exist and reprint; the Z-report shows no unexplained variance; the draft order is in the owner's Purchases list.

- [ ] **Step 3: Confirm the permission actually restricts**

Switch the flag off for that cashier. Without logging out, confirm the Suppliers tab disappears on the next page load and a direct URL returns 403. Confirm an admin still reaches everything.

- [ ] **Step 4: Tidy the dev database**

Remove the test supplier, purchases, payments, receipts, ledger rows and the `Plan Test` user; restore product 1 to cost `0.00`, sell `750.00`, stock `1`. Confirm the catalogue totals still read 618 products and Rs 2,087,632.50.

- [ ] **Step 5: Document it**

Add a short section to the docs covering: what the flag grants, that it exposes cost prices, that the drawer must be open to pay cash, and that a locker only appears to cashiers when marked usable.

- [ ] **Step 6: Commit**

```bash
git add docs/
git commit -m "docs: supplier handling at the counter

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Self-review notes

**Spec coverage.** Permission → Tasks 1-3. Nav placement → Task 7. Paying → Tasks 6-7. Receiving, including pay-on-the-spot → Tasks 5, 6, 8. Ordering → Task 9. Locker access → Tasks 1, 4. The paid-amount defect → Tasks 5-6. Testing section → Tasks 2, 4, 6, 10.

**Known soft spots, flagged rather than hidden:**
- Task 6 Step 5 leaves one decision to the implementer (form values vs. passthrough struct fields for the payment block) because it depends on how `c.Bind` behaves against the existing JSON payload. The constraint is stated: whichever is chosen, `purchases` must never read those fields.
- Tasks 7-9 describe the templates in prose rather than full markup, because they follow existing pages closely (`more.templ`, `purchases.templ`) and transcribing several hundred lines of near-duplicate markup into the plan would be worse than pointing at the pattern.
- Task 6's test constructs `cashflow.NewService(conn, nil)`. `MoveTx` was checked and reads neither the sales service nor the service's own db handle, but if that ever changes the nil bites. It is a test-only shortcut, not a pattern to copy into production code.
- Task 9 moves `DraftPOPrint` onto `*Server` so both route groups can register it. That is a small refactor of admin code in service of a cashier feature — deliberate, and cheaper than a second copy of the PO renderer.
