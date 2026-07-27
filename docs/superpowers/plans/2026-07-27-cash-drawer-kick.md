# Cash-Drawer Kick Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Open the physical cash drawer (wired to the thermal receipt printer) by sending the ESC/POS drawer-kick pulse on every till cash event.

**Architecture:** Kick on the money event (post-commit), never on the paper — so reprints never fire it and it works regardless of "Ask to print". A pure `escpos.DrawerKick` primitive builds the pulse; a shared kicker closure sends it best-effort via `printing.Raw` (CUPS raw on Unix, winspool RAW on Windows). Triggers are wired at four seams: the cash-register service (open/close/deposit/withdraw), the sales service (cash sale), the web counter handlers (till-paid money receipts), and a manual No-Sale button.

**Tech Stack:** Go, Echo, sqlx, Postgres/goose, Templ, HTMX/Alpine, ESC/POS.

## Global Constraints

- **Setting default OFF.** `open_cash_drawer` defaults false; when off, every kick path is a byte-identical no-op. Additive migration only, no backfill.
- **Best-effort, post-commit.** A kick is fire-and-forget after the DB tx commits; a printer error is never surfaced and never rolls back or fails a money transaction. A rolled-back tx must never have kicked.
- **Kick lives in the receipt-document layer, never the shared transport.** `printing.Raw` also carries TSPL label streams; ESC/POS kick bytes must only ever go to `cfg.ReceiptPrinter`.
- **Pin encoding:** `drawer_kick_pin` 0 = drawer pin 2 (`m=0`), 1 = pin 5 (`m=1`). Pulse = `1B 70 m 19 FA`.
- **Never kick on:** receipt reprints/views, card/credit-only sales, money paid from a locker/bank.
- Commit message trailer: `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`.

---

## File Structure

- `migrations/0056_cash_drawer.sql` — new: two additive settings columns.
- `internal/features/settings/settings.go` — modify: struct + UpdateInput fields + Update SQL.
- `templates/pages/admin/settings.templ` — modify: toggle + pin select (printing section).
- `internal/escpos/escpos.go` — modify: `DrawerKick` + `KickDrawer`.
- `internal/features/cashregister/cashregister.go` — modify: `WithDrawerKick` hook + post-commit calls.
- `internal/features/sales/service.go` — modify: `tenderPaidCash` helper, `WithDrawerKick` hook, Create post-commit call.
- `internal/features/sales/api.go` — modify: `RegisterAPI` returns `*Service`.
- `cmd/server/main.go` — modify: build the kicker closure; wire into cashregister + sales services.
- `internal/web/cashier.go` — modify: `kickDrawer` helper on `*Server`; call at counter money-receipt sites; POSData field; No-Sale handler.
- `internal/web/cashier_suppliers.go`, `internal/web/cashier_expenses.go` — modify: kick when source is the till.
- `templates/pages/cashier/pos.templ` — modify: `OpenCashDrawer` field + No-Sale button.
- `static/js/app.js` — modify: `noSale()` action.
- `internal/web/web.go` — modify: register `POST /cashier/drawer/open`.
- Tests: `internal/escpos/escpos_test.go`, `internal/features/sales/tender_test.go`.

---

## Task 1: Setting — schema, model, UI

**Files:**
- Create: `migrations/0056_cash_drawer.sql`
- Modify: `internal/features/settings/settings.go` (struct ~line 27, UpdateInput ~line 60, Update SQL ~line 129)
- Modify: `templates/pages/admin/settings.templ` (printing section, near `ask_to_print`)

**Interfaces:**
- Produces: `settings.Settings.OpenCashDrawer bool`, `settings.Settings.DrawerKickPin int`; the same two on `settings.UpdateInput` with form tags `open_cash_drawer`, `drawer_kick_pin`.

- [ ] **Step 1: Write the migration**

`migrations/0056_cash_drawer.sql`:
```sql
-- +goose Up
ALTER TABLE settings ADD COLUMN open_cash_drawer BOOLEAN  NOT NULL DEFAULT false;
ALTER TABLE settings ADD COLUMN drawer_kick_pin  SMALLINT NOT NULL DEFAULT 0; -- 0 = pin 2, 1 = pin 5

-- +goose Down
ALTER TABLE settings DROP COLUMN drawer_kick_pin;
ALTER TABLE settings DROP COLUMN open_cash_drawer;
```

- [ ] **Step 2: Run the migration up then down then up**

Run: `set -a && . ./.env && set +a && goose -dir migrations postgres "$DATABASE_URL" up && goose -dir migrations postgres "$DATABASE_URL" down && goose -dir migrations postgres "$DATABASE_URL" up`
Expected: clean up/down/up (if `goose` CLI is absent, the server runs migrations at boot — start it once instead and confirm no error).

- [ ] **Step 3: Add the model fields**

In `settings.go`, in `type Settings struct` after `StockTakeEnabled`:
```go
	OpenCashDrawer bool `db:"open_cash_drawer" json:"open_cash_drawer"`
	DrawerKickPin  int  `db:"drawer_kick_pin"  json:"drawer_kick_pin"`
```
In `type UpdateInput struct` after `StockTakeEnabled`:
```go
	OpenCashDrawer bool `json:"open_cash_drawer" form:"open_cash_drawer"`
	DrawerKickPin  int  `json:"drawer_kick_pin"  form:"drawer_kick_pin" validate:"omitempty,oneof=0 1"`
```

- [ ] **Step 4: Persist them in Repository.Update**

In the `UPDATE settings SET` statement, extend the column list and the args. Change the tail of the SET list from `lock_timeout_minutes=$22, stock_take_enabled=$23` to:
```
			lock_timeout_minutes=$22, stock_take_enabled=$23,
			open_cash_drawer=$24, drawer_kick_pin=$25
```
and append to the args (after `in.StockTakeEnabled`):
```go
		in.OpenCashDrawer, in.DrawerKickPin)
```

- [ ] **Step 5: Add the Settings UI controls**

In `templates/pages/admin/settings.templ`, near the `ask_to_print` checkbox in the printing panel, add:
```html
<label class="flex items-center gap-2">
	<input type="checkbox" name="open_cash_drawer" value="true" checked?={ d.Settings.OpenCashDrawer }/>
	<span class="text-sm">Open the cash drawer on till cash events (needs a drawer wired to the receipt printer)</span>
</label>
<div class="mt-2">
	<label class="block text-sm font-medium mb-1">Drawer pin</label>
	<select name="drawer_kick_pin" class="border rounded-lg px-3 py-2">
		<option value="0" selected?={ d.Settings.DrawerKickPin == 0 }>Pin 2 (most drawers)</option>
		<option value="1" selected?={ d.Settings.DrawerKickPin == 1 }>Pin 5</option>
	</select>
</div>
```
(Match the surrounding template's field-access name for the settings value — grep the file for `ask_to_print` and mirror how that checkbox reads its bound value.)

- [ ] **Step 6: Build**

Run: `templ generate && go build ./...`
Expected: builds clean.

- [ ] **Step 7: Commit**

```bash
git add migrations/0056_cash_drawer.sql internal/features/settings/settings.go templates/pages/admin/settings.templ
git commit -m "feat(settings): cash-drawer open toggle + pin (default off)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: `escpos.DrawerKick` primitive + sender (pure TDD)

**Files:**
- Modify: `internal/escpos/escpos.go` (already imports `settings` and `printing`? — it imports `settings`; add `printing`)
- Test: `internal/escpos/escpos_test.go`

**Interfaces:**
- Consumes: `settings.Settings.OpenCashDrawer`, `.DrawerKickPin`, `.ReceiptPrinter` (Task 1).
- Produces: `func DrawerKick(cfg settings.Settings) []byte`; `func KickDrawer(ctx context.Context, cfg settings.Settings)`.

- [ ] **Step 1: Write the failing test**

Append to `internal/escpos/escpos_test.go`:
```go
func TestDrawerKick(t *testing.T) {
	off := settings.Settings{OpenCashDrawer: false}
	if DrawerKick(off) != nil {
		t.Fatal("disabled must return nil")
	}
	pin2 := DrawerKick(settings.Settings{OpenCashDrawer: true, DrawerKickPin: 0})
	if !bytes.Equal(pin2, []byte{0x1B, 0x70, 0x00, 0x19, 0xFA}) {
		t.Fatalf("pin2 = % x", pin2)
	}
	pin5 := DrawerKick(settings.Settings{OpenCashDrawer: true, DrawerKickPin: 1})
	if !bytes.Equal(pin5, []byte{0x1B, 0x70, 0x01, 0x19, 0xFA}) {
		t.Fatalf("pin5 = % x", pin5)
	}
}
```
(Ensure the test file imports `bytes` and `karots-pos/internal/features/settings`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/escpos/ -run TestDrawerKick -v`
Expected: FAIL — `DrawerKick` undefined.

- [ ] **Step 3: Implement**

Add to `internal/escpos/escpos.go` (add `"context"` and `"karots-pos/internal/printing"` to imports):
```go
// DrawerKick returns the ESC/POS pulse that opens the cash drawer wired to the
// receipt printer (ESC p m t1 t2), or nil when the shop hasn't enabled it. m is
// the drawer pin (0 = pin 2, 1 = pin 5); t1/t2 are on/off times in 2 ms units.
func DrawerKick(cfg settings.Settings) []byte {
	if !cfg.OpenCashDrawer {
		return nil
	}
	m := byte(0)
	if cfg.DrawerKickPin == 1 {
		m = 1
	}
	return []byte{0x1B, 0x70, m, 0x19, 0xFA}
}

// KickDrawer sends the drawer pulse to the receipt printer, best-effort. A
// printer that is offline, missing, or has no drawer attached is a silent no-op —
// the caller has already committed the money transaction. No-op when disabled.
func KickDrawer(ctx context.Context, cfg settings.Settings) {
	if b := DrawerKick(cfg); b != nil {
		_ = printing.Raw(ctx, cfg.ReceiptPrinter, b)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/escpos/ -run TestDrawerKick -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/escpos/escpos.go internal/escpos/escpos_test.go
git commit -m "feat(escpos): DrawerKick pulse + best-effort KickDrawer sender

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 3: Cash-register hook — open / close / deposit / withdraw

**Files:**
- Modify: `internal/features/cashregister/cashregister.go` (Service struct ~287, `WithLockerLeg` ~301, `Open` ~379, `Close` ~421, `adjust` ~509)
- Modify: `cmd/server/main.go` (~line 202, after `crSvc.WithLockerLeg(...)`)

**Interfaces:**
- Consumes: `escpos.KickDrawer` (Task 2), `settings.Service` (existing).
- Produces: `func (s *Service) WithDrawerKick(fn func(ctx context.Context)) *Service`; a `newDrawerKicker(settingsSvc *settings.Service) func(context.Context)` in `main`.

- [ ] **Step 1: Add the hook field + setter**

In `cashregister.go`, add to `type Service struct` (near `lockerLeg LockerLeg`):
```go
	drawerKick func(ctx context.Context) // optional; fires post-commit on a till cash event
```
Add after `WithLockerLeg`:
```go
// WithDrawerKick injects a best-effort action fired AFTER a till cash event
// commits (open, close, pay-in, withdrawal) — used to pop the physical cash
// drawer. nil = no drawer.
func (s *Service) WithDrawerKick(fn func(ctx context.Context)) *Service {
	s.drawerKick = fn
	return s
}

func (s *Service) kick(ctx context.Context) {
	if s.drawerKick != nil {
		s.drawerKick(ctx)
	}
}
```

- [ ] **Step 2: Call it post-commit in Open, Close, adjust**

In `Open`, replace `return sess, nil` (the final success return, after the `if receiptID > 0` block) with:
```go
	s.kick(ctx)
	return sess, nil
```
In `Close`, after the `if receiptID > 0 { res.ReceiptID = &receiptID }` block and before `return res, nil`, insert:
```go
	s.kick(ctx)
```
In `adjust` (covers PayIn + Withdraw), after the `if receiptID > 0 { sum.ReceiptID = &receiptID }` block, before its `return sum, nil`, insert:
```go
	s.kick(ctx)
```
(Each is placed after the `appdb.WithTx(...)` call has returned without error — never inside the tx.)

- [ ] **Step 3: Wire the kicker in main.go**

In `cmd/server/main.go`, add a helper (below imports, or in a small `drawer.go` in `cmd/server`):
```go
func newDrawerKicker(settingsSvc *settings.Service) func(context.Context) {
	return func(ctx context.Context) {
		cfg, err := settingsSvc.Get(ctx)
		if err != nil || cfg == nil {
			return
		}
		escpos.KickDrawer(ctx, *cfg)
	}
}
```
Then, right after `crSvc.WithLockerLeg(...)` (~line 205):
```go
	drawerKicker := newDrawerKicker(settings.NewService(sqlxDB))
	crSvc.WithDrawerKick(drawerKicker)
```
Add imports to `main.go` if missing: `"karots-pos/internal/escpos"` and `"context"`.

- [ ] **Step 4: Build**

Run: `go build ./...`
Expected: builds clean.

- [ ] **Step 5: Commit**

```bash
git add internal/features/cashregister/cashregister.go cmd/server/main.go
git commit -m "feat(cashregister): kick the drawer on open/close/deposit/withdraw

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 4: Cash-sale kick — sales service

**Files:**
- Modify: `internal/features/sales/service.go` (Service struct, `Create` ~line 395–420 tender block and its final `return detail, nil`)
- Modify: `internal/features/sales/api.go` (`RegisterAPI` ~line 111)
- Modify: `cmd/server/main.go` (~line 199 `sales.RegisterAPI(...)`)
- Test: `internal/features/sales/tender_test.go`

**Interfaces:**
- Consumes: `sales.PaymentInput.Method` (existing), `drawerKicker` (Task 3).
- Produces: `func tenderPaidCash(ps []PaymentInput) bool`; `func (s *Service) WithDrawerKick(fn func(ctx context.Context)) *Service`; `sales.RegisterAPI` now returns `*Service`.

- [ ] **Step 1: Write the failing test**

Create `internal/features/sales/tender_test.go`:
```go
package sales

import "testing"

func TestTenderPaidCash(t *testing.T) {
	cases := []struct {
		name string
		in   []PaymentInput
		want bool
	}{
		{"cash", []PaymentInput{{Method: "cash", Amount: "100"}}, true},
		{"card only", []PaymentInput{{Method: "card", Amount: "100"}}, false},
		{"credit only", []PaymentInput{{Method: "credit", Amount: "100"}}, false},
		{"mixed card+cash", []PaymentInput{{Method: "card", Amount: "60"}, {Method: "cash", Amount: "40"}}, true},
		{"cash zero amount", []PaymentInput{{Method: "cash", Amount: "0"}}, false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tenderPaidCash(tc.in); got != tc.want {
				t.Fatalf("tenderPaidCash(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
```
(Confirm `PaymentInput`'s fields are `Method string` and `Amount string` — grep `type PaymentInput` in `service.go`; adjust the literals if the amount field differs.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/features/sales/ -run TestTenderPaidCash -v`
Expected: FAIL — `tenderPaidCash` undefined.

- [ ] **Step 3: Implement the helper + hook**

In `service.go` add:
```go
// tenderPaidCash reports whether a sale's payment split includes a positive cash
// line — the condition for popping the physical drawer.
func tenderPaidCash(ps []PaymentInput) bool {
	for _, p := range ps {
		if p.Method == "cash" {
			if v, err := money.Parse(p.Amount); err == nil && v.IsPositive() {
				return true
			}
		}
	}
	return false
}
```
Add to `type Service struct`:
```go
	drawerKick func(ctx context.Context)
```
Add:
```go
// WithDrawerKick injects a best-effort action fired AFTER a cash sale commits, to
// pop the physical cash drawer. nil = no drawer.
func (s *Service) WithDrawerKick(fn func(ctx context.Context)) *Service {
	s.drawerKick = fn
	return s
}
```
(Ensure `money` is imported in `service.go` — it is used already.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/features/sales/ -run TestTenderPaidCash -v`
Expected: PASS.

- [ ] **Step 5: Fire the kick post-commit in Create**

At the end of `Create`, replace the final `return detail, nil` with:
```go
	if s.drawerKick != nil && tenderPaidCash(in.Payments) {
		s.drawerKick(ctx)
	}
	return detail, nil
```
(This is after `appdb.WithTx` returned nil — post-commit.)

- [ ] **Step 6: Return the service from RegisterAPI + wire it**

In `api.go`, change `func RegisterAPI(e *echo.Echo, db *sqlx.DB, cfg *config.Config)` to return `*Service`:
```go
func RegisterAPI(e *echo.Echo, db *sqlx.DB, cfg *config.Config) *Service {
	svc := NewService(db)
	api := &APIHandler{svc: svc}
	// ... existing route registration unchanged ...
	return svc
}
```
(Grep the current body — reuse the existing `svc`/`api` locals; just add `return svc`. If it currently builds the service inline, hoist it to a local first.)

In `main.go`, change line ~199 from `sales.RegisterAPI(e, sqlxDB, cfg)` to:
```go
	posSales := sales.RegisterAPI(e, sqlxDB, cfg)
	posSales.WithDrawerKick(drawerKicker)
```
(`drawerKicker` from Task 3 is already in scope. If Task 4 is done before the kicker line, move the `drawerKicker :=` line up so both services see it.)

- [ ] **Step 7: Build + full package test**

Run: `go build ./... && go test ./internal/features/sales/ ./internal/features/cashregister/ -v`
Expected: builds clean, tests pass.

- [ ] **Step 8: Commit**

```bash
git add internal/features/sales/service.go internal/features/sales/api.go internal/features/sales/tender_test.go cmd/server/main.go
git commit -m "feat(sales): kick the drawer on a cash-tendered sale

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 5: Till-paid money receipts (web counter handlers)

**Files:**
- Modify: `internal/web/cashier.go` (add `kickDrawer` method on `*Server`)
- Modify: `internal/web/cashier_expenses.go` (`ExpenseRecord`, after the move commits, ~line 100)
- Modify: `internal/web/cashier_suppliers.go` (`SupplierPayAtCounter` ~287, `SupplierRefundAtCounter` ~491 — kick when the till is the source/dest)

**Interfaces:**
- Consumes: `escpos.KickDrawer` (Task 2), `cashflow.KindTill`, `s.settings` (existing).
- Produces: `func (s *Server) kickDrawer(ctx context.Context)`; `func (s *Server) kickIfTill(ctx context.Context, loc cashflow.Location)`.

- [ ] **Step 1: Add the web kicker helpers**

In `internal/web/cashier.go` (add `"karots-pos/internal/escpos"` and `"karots-pos/internal/features/cashflow"` imports if missing):
```go
// kickDrawer pops the physical cash drawer, best-effort, honouring the shop
// setting. Used by counter money-receipt handlers and the No-Sale button.
func (s *Server) kickDrawer(ctx context.Context) {
	if cfg, err := s.settings.Get(ctx); err == nil && cfg != nil {
		escpos.KickDrawer(ctx, *cfg)
	}
}

// kickIfTill kicks only when the cash actually left the physical till drawer (not
// a locker or bank).
func (s *Server) kickIfTill(ctx context.Context, loc cashflow.Location) {
	if loc.Kind == cashflow.KindTill {
		s.kickDrawer(ctx)
	}
}
```

- [ ] **Step 2: Kick on a till-paid counter expense**

In `cashier_expenses.go` `ExpenseRecord`, after the `appdb.WithTx(...)` block returns without error (right after the `err = appdb.WithTx(...)` error check, before building the response), add:
```go
	h.s.kickIfTill(ctx, src)
```
(`src` is the `cashflow.Location` from `h.counterSource(...)`.)

- [ ] **Step 3: Kick on a till-paid supplier payment and refund**

In `cashier_suppliers.go`, in `SupplierPayAtCounter` after the payment commits, add `h.s.kickIfTill(ctx, pay.source)` (the parsed source). In `SupplierRefundAtCounter`, the refund cash comes INTO a location `dest`; a refund into the till adds cash to the drawer, so also add `h.s.kickIfTill(ctx, dest)` after it commits.
(Grep each handler for the `counterSource` result variable name and use it; place the call after the transaction's error check, post-commit.)

- [ ] **Step 4: Build**

Run: `go build ./...`
Expected: builds clean.

- [ ] **Step 5: Commit**

```bash
git add internal/web/cashier.go internal/web/cashier_expenses.go internal/web/cashier_suppliers.go
git commit -m "feat(counter): kick the drawer on till-paid expenses, supplier pay/refund

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 6: Manual "Open Drawer" (No-Sale) button

**Files:**
- Modify: `internal/web/cashier.go` (add `OpenDrawer` handler)
- Modify: `internal/web/web.go` (register `POST /cashier/drawer/open` in the cashier group ~line 179)
- Modify: `templates/pages/cashier/pos.templ` (POSData ~line 6; a button near the deposit/withdraw controls)
- Modify: `internal/web/cashier.go` `POS` handler (~line 107, set `OpenCashDrawer`)
- Modify: `static/js/app.js` (add `noSale()` to the pos() component)

**Interfaces:**
- Consumes: `s.kickDrawer` (Task 5), `s.logAudit` (existing), `settings.Settings.OpenCashDrawer`.
- Produces: `POST /cashier/drawer/open`; `POSData.OpenCashDrawer bool`; Alpine `noSale()`.

- [ ] **Step 1: Add the handler (kick + audit, gated by the setting)**

In `cashier.go`:
```go
// OpenDrawer pops the cash drawer with no transaction (the No-Sale button). It is
// available only when the shop enabled the drawer, and every use is audited — a
// no-sale open is a classic theft surface, so the owner gets a trail.
func (h *cashierUI) OpenDrawer(c echo.Context) error {
	ctx := c.Request().Context()
	cfg, err := h.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	if cfg == nil || !cfg.OpenCashDrawer {
		return apperr.Forbidden("cash drawer is not enabled")
	}
	h.s.kickDrawer(ctx)
	h.s.logAudit(c, audit.ActionUpdate, "cash_drawer", "", "opened drawer (no sale)")
	c.Response().Header().Set("HX-Trigger", response.Toast("Drawer opened", "success"))
	return response.NoContent(c)
}
```
(Confirm `apperr`, `audit`, `response` are imported in `cashier.go`.)

- [ ] **Step 2: Register the route**

In `web.go`, in the cashier group (`cg`), near line 179:
```go
	cg.POST("/drawer/open", cashier.OpenDrawer)
```

- [ ] **Step 3: Thread the setting into POSData**

In `pos.templ`, add to `type POSData struct`:
```go
	OpenCashDrawer  bool
```
In `cashier.go` `POS` handler where `cashierpages.POSData{...}` is built, add:
```go
		OpenCashDrawer:  cfg.OpenCashDrawer,
```
(The handler already loads `cfg` for `AskToPrint`; reuse it.)

- [ ] **Step 4: Add the button**

In `pos.templ`, near the deposit/withdraw trigger buttons, add (rendered only when enabled):
```html
if d.OpenCashDrawer {
	<button type="button" x-on:click="noSale()" class="px-3 py-2 rounded-lg border text-sm">Open Drawer</button>
}
```

- [ ] **Step 5: Add the Alpine action**

In `static/js/app.js`, inside the `pos()` component, add:
```js
async noSale() {
  try { await apiFetch("POST", "/cashier/drawer/open"); }
  catch (e) { /* apiFetch toasts its own error */ }
},
```

- [ ] **Step 6: Build**

Run: `templ generate && make css && go build ./...`
Expected: builds clean.

- [ ] **Step 7: Commit**

```bash
git add internal/web/cashier.go internal/web/web.go templates/pages/cashier/pos.templ static/js/app.js static/css/tailwind.css
git commit -m "feat(cashier): manual No-Sale open-drawer button (audited, setting-gated)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 7: Test-print kick + live E2E on the ESC/POS emulator

**Files:**
- Modify: `internal/web/admin.go` (~line 843, the Test-print handler `escpos.Send(ctx, cfg.ReceiptPrinter, escpos.TestDocument(*cfg))`)

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 1: Add the kick to the Test-print button**

In `admin.go` at the test-print handler, right after the successful `escpos.Send(...)` of the test document, add:
```go
	escpos.KickDrawer(ctx, *cfg)
```
So pressing "Test print" both prints and pops the drawer — the owner's one-click hardware check (works on Windows too, same transport).

- [ ] **Step 2: Build + full test suite + vet**

Run: `templ generate && make css && go build ./... && go vet ./... && go test ./...`
Expected: all green.

- [ ] **Step 3: Bring up the ESC/POS emulator**

Run: `make docker-up` (or the compose target that starts the ESC/POS emulator on :9100 with the viewer on :8631 — grep `docker-compose*.yml` for `9100`). Set the shop `receipt_printer` to `tcp://localhost:9100` (Settings page or `UPDATE settings SET receipt_printer='tcp://localhost:9100', open_cash_drawer=true WHERE id=1;`).

- [ ] **Step 4: Live E2E — kicks that MUST fire**

Start the server (`make run`). Logged in as a cashier (seed one like the reload test, with an open till), exercise and confirm the drawer-kick bytes `1B 70 00 19 FA` reach the emulator (watch the viewer / emulator log) for each:
- register **open**, register **close**
- a recharge **deposit** and a **withdrawal**
- a **cash** sale
- a till-paid **counter expense**
- the **No-Sale** button
- the **Test print** button

- [ ] **Step 5: Live E2E — kicks that MUST NOT fire**

Confirm **no** kick bytes for: a **card-only** sale, a **receipt reprint** (Receipts tab), a **locker-paid** expense, and — after `UPDATE settings SET open_cash_drawer=false` — every one of the Step-4 actions (byte-identical to today).

- [ ] **Step 6: Commit**

```bash
git add internal/web/admin.go
git commit -m "feat(printing): pop the drawer on Test print; drawer-kick verified E2E

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Self-Review

- **Spec coverage:** toggle+pin (T1) ✓; primitive+sender (T2) ✓; open/close/deposit/withdraw (T3) ✓; cash sale, cash-touching only (T4) ✓; till-paid money receipts (T5) ✓; No-Sale button audited + setting-gated (T6) ✓; Test-print + Windows via `printing.Raw` + full E2E incl. negative cases (T7) ✓; default-off inertness (T1 default + verified T7 Step 5) ✓.
- **Reprints never kick:** guaranteed by construction — kicks fire at money-event sites (service Create / register methods / counter handlers), never at print endpoints. Verified T7 Step 5.
- **Type consistency:** `WithDrawerKick(fn func(ctx context.Context)) *Service` identical on both cashregister and sales; `DrawerKick(settings.Settings) []byte` / `KickDrawer(context.Context, settings.Settings)` used consistently by main's `newDrawerKicker` and web's `kickDrawer`.
- **Placeholder scan:** none — every step carries real code or an exact command. The two "grep to confirm field name" notes are verification aids, not deferred work; the code given is complete.
