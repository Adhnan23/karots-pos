# Admin Bill Payment & Cash Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the admin a bill-payment / get-money page with two free storage pickers (account side + physical-cash side, any locker kind or open till), and fix the cashier flow so bill-pay only offers banks the owner marked cashier-accessible.

**Architecture:** Extract the bill-pay leg math and the cashier-access rule into two pure, unit-tested helpers in a new `banktx.go`. Refactor the existing cashier `BankTx` to reuse them (behaviour-identical, plus the newly-enforced access gate). Add a new admin page + handler that reuse the same helpers, the existing `LocationPicker`, `cashflow.MoveTx`, `RecordBillTx`, the bill slip/receipts tabs, and the admin refill's print-policy pattern. No migration.

**Tech Stack:** Go, Echo, sqlx, templ, Alpine/HTMX, `shopspring/decimal`. Package `plugins/recharge`.

## Global Constraints

- Every money move goes through `cashflow.MoveTx` inside one `appdb.WithTx` transaction; a partial failure must roll the whole thing back. Each leg gets its own CR- receipt.
- The cashier money model does **not** change: bank-type only, physical cash → the cashier's own till. The only cashier change is enforcing `cashier_access`.
- No schema/migration change: `recharge_bill_tx.session_id` is nullable, `bank_locker_id` is `NOT NULL` with **no** foreign key (store `0` when the account side is a till).
- No External "pocket" counterparty — "pocket" is an existing locker kind (`safe / bank / pocket / other`). Admin pickers list real lockers (any kind) + open tills only.
- Overdraw is guarded by `cashflow.Move` per pile, which already honours each locker's `allow_negative`. Do not add a second guard.
- Drawer kick is best-effort and setting-gated (`escpos.KickDrawer(ctx, *cfg)`), fired only after the tx commits and only when a till is involved. A printer error never fails the money move.
- Leave `.claude/settings.local.json` and `static/css/tailwind.css` out of commits. End commit messages with `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`.
- Dev `pos_db` is disposable; live checks run against it and tidy up after.

---

### Task 1: Shared leg-builder + cashier-access predicate (pure helpers)

Two pure functions, fully unit-tested, that later tasks consume. No behaviour change to any running code yet.

**Files:**
- Create: `plugins/recharge/banktx.go`
- Test: `plugins/recharge/banktx_test.go`

**Interfaces:**
- Consumes: `karots-pos/internal/features/cashflow` (`Location`, `Locker`, `Till`, `External`, `KindTill`, `KindLocker`), `karots-pos/internal/features/lockers` (`Locker`, `KindBank`), `github.com/shopspring/decimal`.
- Produces:
  - `type bankLeg struct { From, To cashflow.Location; Amount decimal.Decimal; Party string }`
  - `func buildBankLegs(typ string, account, cash cashflow.Location, amt, svc decimal.Decimal, biller string) []bankLeg`
  - `func bankUsableByCashier(l *lockers.Locker) bool`

- [ ] **Step 1: Write the failing tests**

Create `plugins/recharge/banktx_test.go`:

```go
package recharge

import (
	"testing"

	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/lockers"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestBuildBankLegs(t *testing.T) {
	account := cashflow.Locker(7) // e.g. a bank locker
	cash := cashflow.Till(3)      // e.g. a till
	ext := cashflow.External()

	type want struct {
		from, to cashflow.Location
		amount   string
	}
	cases := []struct {
		name  string
		typ   string
		svc   string
		legs  []want
	}{
		{
			name: "billpay no service charge",
			typ:  "billpay", svc: "0",
			legs: []want{
				{account, ext, "100"},   // bank pays biller (down, guarded)
				{ext, cash, "100"},       // customer cash in
			},
		},
		{
			name: "billpay with service charge",
			typ:  "billpay", svc: "20",
			legs: []want{
				{account, ext, "100"},
				{ext, cash, "120"}, // principal + service charge, all cash in
			},
		},
		{
			name: "getmoney no service charge",
			typ:  "getmoney", svc: "0",
			legs: []want{
				{cash, ext, "100"},   // cash out (guarded)
				{ext, account, "100"}, // e-money into the account
			},
		},
		{
			name: "getmoney with service charge",
			typ:  "getmoney", svc: "20",
			legs: []want{
				{cash, ext, "100"},
				{ext, account, "100"},
				{ext, cash, "20"}, // service charge extra cash in
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildBankLegs(tc.typ, account, cash, dec("100"), dec(tc.svc), "Bill 42")
			if len(got) != len(tc.legs) {
				t.Fatalf("got %d legs, want %d", len(got), len(tc.legs))
			}
			for i, w := range tc.legs {
				g := got[i]
				if g.From != w.from || g.To != w.to || !g.Amount.Equal(dec(w.amount)) {
					t.Fatalf("leg %d = {%v->%v %s}, want {%v->%v %s}",
						i, g.From, g.To, g.Amount, w.from, w.to, w.amount)
				}
			}
		})
	}
}

func TestBankUsableByCashier(t *testing.T) {
	mk := func(active bool, kind string, access bool) *lockers.Locker {
		return &lockers.Locker{IsActive: active, Kind: kind, CashierAccess: access}
	}
	cases := []struct {
		name string
		l    *lockers.Locker
		want bool
	}{
		{"accessible active bank", mk(true, lockers.KindBank, true), true},
		{"bank without cashier access", mk(true, lockers.KindBank, false), false},
		{"inactive bank", mk(false, lockers.KindBank, true), false},
		{"safe (not a bank)", mk(true, lockers.KindSafe, true), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bankUsableByCashier(tc.l); got != tc.want {
				t.Fatalf("bankUsableByCashier = %v, want %v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./plugins/recharge/ -run 'TestBuildBankLegs|TestBankUsableByCashier' -v`
Expected: FAIL — `undefined: buildBankLegs`, `undefined: bankUsableByCashier`.

- [ ] **Step 3: Write the implementation**

Create `plugins/recharge/banktx.go`:

```go
package recharge

import (
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/lockers"

	"github.com/shopspring/decimal"
)

// bankLeg is one cashflow move in a bill-payment / get-money. Reason is applied
// by the handler (it is the same for every leg); Party labels the External
// counterparty on that leg's CR- receipt.
type bankLeg struct {
	From, To cashflow.Location
	Amount   decimal.Decimal
	Party    string
}

// buildBankLegs returns the cashflow legs for a bill payment ("billpay") or a
// get-money ("getmoney"). account is the storage the biller is paid from (billpay)
// or the e-money lands in (getmoney); cash is the physical-cash pile. svc (>= 0)
// is the service charge, always extra cash into the cash pile. biller labels the
// bank↔biller leg; the cash↔customer legs are labelled "Customer".
//
// The order matters: the overdraw-guarded leg (bank down for billpay, cash out
// for getmoney) comes first so an overdraw rolls the whole tx back before any
// money moves.
func buildBankLegs(typ string, account, cash cashflow.Location, amt, svc decimal.Decimal, biller string) []bankLeg {
	ext := cashflow.External()
	switch typ {
	case "billpay":
		return []bankLeg{
			{From: account, To: ext, Amount: amt, Party: biller},
			{From: ext, To: cash, Amount: amt.Add(svc), Party: "Customer"},
		}
	case "getmoney":
		legs := []bankLeg{
			{From: cash, To: ext, Amount: amt, Party: "Customer"},
			{From: ext, To: account, Amount: amt, Party: "Customer"},
		}
		if svc.IsPositive() {
			legs = append(legs, bankLeg{From: ext, To: cash, Amount: svc, Party: "Customer"})
		}
		return legs
	}
	return nil
}

// bankUsableByCashier reports whether a locker is one a cashier may run bill-pay
// against: an active bank the owner marked cashier-accessible. Used both to filter
// the cashier's bank picker and to guard the POST (a forged bank id can't slip
// past the picker filter otherwise).
func bankUsableByCashier(l *lockers.Locker) bool {
	return l != nil && l.IsActive && l.Kind == lockers.KindBank && l.CashierAccess
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./plugins/recharge/ -run 'TestBuildBankLegs|TestBankUsableByCashier' -v`
Expected: PASS (all sub-tests).

- [ ] **Step 5: Commit**

```bash
git add plugins/recharge/banktx.go plugins/recharge/banktx_test.go
git commit -m "feat(recharge): shared bill-pay leg builder + cashier bank-access predicate

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: Refactor cashier BankTx to reuse the helpers + enforce bank access

Make the cashier flow use `buildBankLegs` (one source of truth for the leg math) and enforce `cashier_access` in both the bank picker and the POST guard. Behaviour change: banks with `cashier_access = off` no longer appear and are rejected if posted. Money model otherwise identical.

**Files:**
- Modify: `plugins/recharge/cashier.go` (`bankLockers` ~lines 659-673; `BankTx` ~lines 487-598)

**Interfaces:**
- Consumes: `buildBankLegs`, `bankUsableByCashier`, `bankLeg` (Task 1).

- [ ] **Step 1: Filter the cashier bank picker to accessible banks**

In `plugins/recharge/cashier.go`, replace the body of `bankLockers`:

```go
// bankLockers returns the active core kind="bank" lockers the owner marked
// cashier-accessible, with live balances, for the cashier bill-pay / get-money
// picker. A "bank" is a plain core locker managed under Money → Cash Lockers;
// the plugin only reads & moves them. Banks with cashier_access off are hidden —
// the cashier literally cannot pick them (and BankTx re-checks on POST).
func (p *Plugin) bankLockers(ctx context.Context) ([]lockers.Locker, error) {
	all, err := p.lockers.List(ctx, true)
	if err != nil {
		return nil, err
	}
	banks := make([]lockers.Locker, 0, len(all))
	for i := range all {
		if bankUsableByCashier(&all[i]) {
			banks = append(banks, all[i])
		}
	}
	return banks, nil
}
```

- [ ] **Step 2: Guard the POST and reuse buildBankLegs in BankTx**

In `BankTx`, replace the bank-validation line:

```go
	bank, err := h.p.lockers.Get(ctx, bankID)
	if err != nil || bank == nil || !bank.IsActive || bank.Kind != lockers.KindBank {
		return apperr.Validation("choose a valid bank")
	}
```

with:

```go
	bank, err := h.p.lockers.Get(ctx, bankID)
	if err != nil || !bankUsableByCashier(bank) {
		return apperr.Validation("choose a valid bank")
	}
```

Then replace the inline leg-building block:

```go
	till := cashflow.Till(uid)
	bankLoc := cashflow.Locker(bankID)
	ext := cashflow.External()
	type leg struct {
		from, to cashflow.Location
		amount   decimal.Decimal
		party    string
	}
	var legs []leg
	switch typ {
	case "billpay":
		// bank down (guarded) first, then cash in (bill + service charge).
		legs = append(legs, leg{bankLoc, ext, amt, biller})
		legs = append(legs, leg{ext, till, amt.Add(svc), "Customer"})
	case "getmoney":
		// cash out (guarded) first, then bank up, then the service-charge cash-in.
		legs = append(legs, leg{till, ext, amt, "Customer"})
		legs = append(legs, leg{ext, bankLoc, amt, "Customer"})
		if svc.IsPositive() {
			legs = append(legs, leg{ext, till, svc, "Customer"})
		}
	}

	// All legs in ONE transaction so a drawer/bank overdraw rolls everything back.
	if err := appdb.WithTx(ctx, h.p.core.DB, func(tx *sqlx.Tx) error {
		for _, l := range legs {
			if _, err := h.p.cashflow.MoveTx(ctx, tx, cashflow.MoveInput{
				From: l.from, To: l.to, Amount: l.amount, Reason: reason,
				ReceiptKind: typ, Party: l.party, ActorID: uid,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
```

with:

```go
	legs := buildBankLegs(typ, cashflow.Locker(bankID), cashflow.Till(uid), amt, svc, biller)

	// All legs in ONE transaction so a drawer/bank overdraw rolls everything back.
	if err := appdb.WithTx(ctx, h.p.core.DB, func(tx *sqlx.Tx) error {
		for _, l := range legs {
			if _, err := h.p.cashflow.MoveTx(ctx, tx, cashflow.MoveInput{
				From: l.From, To: l.To, Amount: l.Amount, Reason: reason,
				ReceiptKind: typ, Party: l.Party, ActorID: uid,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
```

- [ ] **Step 3: Remove the now-unused import if any**

The `decimal` import stays (used elsewhere in the file). Run `goimports`/build to confirm nothing dangling.

Run: `go build ./plugins/recharge/`
Expected: builds clean. If it reports `"karots-pos/internal/features/cashflow" imported and not used` or an unused var, fix by removing only the now-dead line — but `cashflow` is still used (`cashflow.Locker`, `cashflow.Till`, `cashflow.MoveInput`), so no import change is expected.

- [ ] **Step 4: Run the package tests**

Run: `go test ./plugins/recharge/ -v`
Expected: PASS (existing tests + Task 1 tests). No test asserts on the old inline struct, so nothing else should break.

- [ ] **Step 5: Commit**

```bash
git add plugins/recharge/cashier.go
git commit -m "fix(recharge): cashier bill-pay honors locker cashier_access; reuse leg builder

Only banks marked cashier-accessible appear in the picker and are accepted on
POST (defense in depth). Leg math now comes from the shared buildBankLegs.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: Admin Bill payment & cash page (templ + handlers + routes + nav)

The new admin page, its two handlers, routes, and hub card. Reuses `cashLocationChoices`, `LocationPicker`, `buildBankLegs`, `RecordBillTx`, and the refill print-policy pattern.

**Files:**
- Create: `plugins/recharge/admin_bills.go`
- Create: `plugins/recharge/admin_bills.templ` (generates `admin_bills_templ.go`)
- Modify: `plugins/recharge/recharge.go` (routes ~line 70; nav ~after line 190)

**Interfaces:**
- Consumes: `buildBankLegs`, `bankLeg` (Task 1); `adminUI.cashLocationChoices` and `parseLocation` and `refillSourceAllowed` (existing, `admin.go` / `cashier_refill.go`); `adminfragments.LocationPicker` / `LocationChoice`; `Store.RecordBillTx` / `BillTxInput` / `BillTxByID`; `Plugin.reprintBill`.
- Produces: `func (a *adminUI) Bills(c echo.Context) error`, `func (a *adminUI) BankTx(c echo.Context) error`, `templ AdminBillsPage(userName, symbol string, choices []adminfragments.LocationChoice)`.

- [ ] **Step 1: Write the admin templ page**

Create `plugins/recharge/admin_bills.templ`:

```templ
package recharge

import adminfragments "karots-pos/templates/fragments/admin"
import "karots-pos/templates/layouts"

// AdminBillsPage is the back-office bill-payment / get-money recorder. Unlike the
// cashier form (bank-only, cash → own till), the admin picks BOTH sides from any
// storage pile (locker of any kind, or an open till). The form posts to
// /admin/recharge/bank-tx and drives the UI over HX-Trigger (toast or the shared
// Print/Skip prompt), then resets — it does not swap the page.
templ AdminBillsPage(userName, symbol string, choices []adminfragments.LocationChoice) {
	@layouts.Admin("Bill payment & cash", userName, "recharge-bills") {
		<div class="mb-6">
			<h1 class="text-2xl font-bold">🧾 Bill payment &amp; cash</h1>
			<p class="text-sm text-slate-500">Pay a customer's bill from any account, or hand out cash — choosing exactly which pile each side touches. Every leg is recorded in the shop's cash flow with a receipt.</p>
		</div>
		if len(choices) == 0 {
			<div class="bg-amber-50 text-amber-700 rounded-xl p-4 text-sm">No cash locations yet. Add a locker under Money → Cash Lockers, or open a till.</div>
		} else {
			<div class="bg-white rounded-2xl shadow-sm p-6 max-w-2xl"
				x-data="{ type:'billpay' }">
				<form
					hx-post="/admin/recharge/bank-tx"
					hx-swap="none"
					hx-on::after-request="if(event.detail.successful) this.reset()"
					class="grid grid-cols-1 sm:grid-cols-2 gap-4">
					<div class="sm:col-span-2">
						<label class="block text-sm font-medium text-slate-600 mb-1">Type</label>
						<input type="hidden" name="type" x-model="type"/>
						<div class="grid grid-cols-2 gap-2">
							<button type="button" x-on:click="type='billpay'" x-bind:class="type==='billpay' ? 'ring-2 ring-emerald-500 bg-emerald-50' : 'border border-slate-200'" class="px-3 py-2.5 rounded-lg text-left">
								<div class="font-medium text-sm">Pay a bill</div>
								<div class="text-xs text-slate-400">Account pays the biller (down) → cash comes in (up)</div>
							</button>
							<button type="button" x-on:click="type='getmoney'" x-bind:class="type==='getmoney' ? 'ring-2 ring-emerald-500 bg-emerald-50' : 'border border-slate-200'" class="px-3 py-2.5 rounded-lg text-left">
								<div class="font-medium text-sm">Give cash out</div>
								<div class="text-xs text-slate-400">Cash goes out (down) → account receives it (up)</div>
							</button>
						</div>
					</div>
					<div class="sm:col-span-2">
						<label class="block text-sm font-medium text-slate-600 mb-1">
							<span x-show="type==='billpay'">Pay biller from (account / bank)</span>
							<span x-show="type==='getmoney'">Money lands in (account / bank)</span>
						</label>
						@adminfragments.LocationPicker("account", "Choose an account…", choices)
					</div>
					<div class="sm:col-span-2">
						<label class="block text-sm font-medium text-slate-600 mb-1">
							<span x-show="type==='billpay'">Cash received into</span>
							<span x-show="type==='getmoney'">Cash paid out from</span>
						</label>
						@adminfragments.LocationPicker("cash", "Choose a cash pile…", choices)
					</div>
					<div>
						<label class="block text-sm font-medium text-slate-600 mb-1">Amount</label>
						<input type="number" name="amount" min="0" step="0.01" required placeholder="0.00" class="w-full border rounded-lg px-3 py-2.5 text-right"/>
					</div>
					<div>
						<label class="block text-sm font-medium text-slate-600 mb-1">Service charge (optional)</label>
						<input type="number" name="service_charge" min="0" step="0.01" placeholder="0.00" class="w-full border rounded-lg px-3 py-2.5 text-right"/>
					</div>
					<div>
						<label class="block text-sm font-medium text-slate-600 mb-1">Reference (optional)</label>
						<input type="text" name="reference" placeholder="bill / txn no." class="w-full border rounded-lg px-3 py-2.5"/>
					</div>
					<div>
						<label class="block text-sm font-medium text-slate-600 mb-1">Note (optional)</label>
						<input type="text" name="note" placeholder="" class="w-full border rounded-lg px-3 py-2.5"/>
					</div>
					<div class="sm:col-span-2 flex justify-end">
						<button type="submit" class="py-2.5 px-6 rounded-lg bg-emerald-600 text-white font-semibold hover:bg-emerald-700">Record</button>
					</div>
				</form>
			</div>
		}
	}
}
```

- [ ] **Step 2: Write the admin handlers**

Create `plugins/recharge/admin_bills.go`:

```go
package recharge

import (
	"net/http"
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/escpos"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
	"github.com/labstack/echo/v4"
)

// Bills renders the admin bill-payment / get-money page, seeded with every
// pickable cash location (all active lockers + open tills).
func (a *adminUI) Bills(c echo.Context) error {
	ctx := c.Request().Context()
	choices, err := a.cashLocationChoices(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, AdminBillsPage(middleware.CurrentUserName(c), a.symbol(ctx), choices))
}

// BankTx records an admin bill payment / get-money. Unlike the cashier flow it
// moves between two freely-picked piles (account side + physical-cash side), each
// validated against the offered choices before any money moves. Every leg commits
// in ONE cashflow transaction so an overdraw on any pile rolls the whole thing
// back; the picked source's own allow_negative setting decides whether it may go
// below zero. Records a session-less recharge_bill_tx row for the slip + Bills
// receipts tab, then follows the shop's print policy.
func (a *adminUI) BankTx(c echo.Context) error {
	ctx := c.Request().Context()
	uid := middleware.CurrentUserID(c)

	typ := c.FormValue("type")
	if typ != "billpay" && typ != "getmoney" {
		return apperr.BadRequest("invalid transaction type")
	}

	choices, err := a.cashLocationChoices(ctx)
	if err != nil {
		return err
	}
	accountVal := strings.TrimSpace(c.FormValue("account"))
	cashVal := strings.TrimSpace(c.FormValue("cash"))
	if !refillSourceAllowed(choices, accountVal) || !refillSourceAllowed(choices, cashVal) {
		return apperr.Validation("choose valid cash locations")
	}
	if accountVal == cashVal {
		return apperr.Validation("the account and cash sides must be different places")
	}
	account, err := parseLocation(accountVal)
	if err != nil {
		return err
	}
	cash, err := parseLocation(cashVal)
	if err != nil {
		return err
	}

	amt, err := money.Parse(c.FormValue("amount"))
	if err != nil || !amt.IsPositive() {
		return apperr.Validation("amount must be positive")
	}
	svc := decimal.Zero
	if v := strings.TrimSpace(c.FormValue("service_charge")); v != "" {
		svc, err = money.Parse(v)
		if err != nil || svc.IsNegative() {
			return apperr.Validation("service charge must be zero or more")
		}
	}
	ref := strings.TrimSpace(c.FormValue("reference"))
	note := strings.TrimSpace(c.FormValue("note"))

	reason := txLabel(typ)
	if ref != "" {
		reason += " #" + ref
	}
	if note != "" {
		reason += " - " + note
	}
	biller := "Bill payment"
	if ref != "" {
		biller = "Bill " + ref
	}

	legs := buildBankLegs(typ, account, cash, amt, svc, biller)

	// Capture the account-side pile label from cashflow's own leg labelling, so the
	// slip names the account exactly as the CR- receipts do.
	var accountName string
	if err := appdb.WithTx(ctx, a.p.core.DB, func(tx *sqlx.Tx) error {
		for i, l := range legs {
			rec, err := a.p.cashflow.MoveTx(ctx, tx, cashflow.MoveInput{
				From: l.From, To: l.To, Amount: l.Amount, Reason: reason,
				ReceiptKind: typ, Party: l.Party, ActorID: uid,
			})
			if err != nil {
				return err
			}
			// billpay leg 0 is account→External (FromLabel = account); getmoney leg 1
			// is External→account (ToLabel = account).
			if typ == "billpay" && i == 0 {
				accountName = rec.FromLabel
			}
			if typ == "getmoney" && i == 1 {
				accountName = rec.ToLabel
			}
		}
		return nil
	}); err != nil {
		return err
	}

	// A physical drawer on either side → pop it (best-effort, setting-gated).
	if account.Kind == cashflow.KindTill || cash.Kind == cashflow.KindTill {
		if cfg, cerr := a.p.core.Settings.Get(ctx); cerr == nil && cfg != nil {
			escpos.KickDrawer(ctx, *cfg)
		}
	}

	bankLockerID := int64(0)
	if account.Kind == cashflow.KindLocker {
		bankLockerID = account.ID
	}
	billID, err := a.p.store.RecordBillTx(ctx, BillTxInput{
		SessionID: nil, BankLockerID: bankLockerID, BankName: accountName, Type: typ,
		Amount: amt, ServiceCharge: svc, Reference: ref, Note: note, CreatedBy: uid,
	})
	if err != nil {
		return err
	}

	// Follow the shop's print policy for the bill slip (mirrors the admin Refill):
	// AskToPrint on → the shared Print/Skip prompt; off → best-effort print now.
	// The form is hx-swap="none", so drive the UI over HX-Trigger.
	msg := txLabel(typ) + " recorded — " + accountName
	reprintURL := "/admin/recharge/bill/" + strconv.FormatInt(billID, 10) + "/print"
	if cfg, cerr := a.p.core.Settings.Get(ctx); cerr == nil && cfg != nil && cfg.AskToPrint {
		c.Response().Header().Set("HX-Trigger", response.PrintPrompt(msg, reprintURL, false))
		return c.NoContent(http.StatusOK)
	}
	if t, terr := a.p.store.BillTxByID(ctx, billID); terr == nil {
		_ = a.p.reprintBill(ctx, t) // best-effort: a printer hiccup never fails the move
	}
	c.Response().Header().Set("HX-Trigger", response.Toast(msg, "success"))
	return c.NoContent(http.StatusOK)
}
```

- [ ] **Step 3: Register the routes and hub card**

In `plugins/recharge/recharge.go`, after the `reg.Admin().POST("/recharge/refill", a.Refill)` line (~line 70), add:

```go
	reg.Admin().GET("/recharge/bills", a.Bills)
	reg.Admin().POST("/recharge/bank-tx", a.BankTx)
```

Then after the "Float refills" `AddAdminNav` block (~line 190), add:

```go
	reg.AddAdminNav(plugin.AdminNavEntry{
		SectionLabel: "Reload & Bills",
		Icon:         "📶",
		Href:         "/admin/recharge/bills",
		Label:        "Bill payment & cash",
		Key:          "recharge-bills",
		Desc:         "Pay a bill or hand out cash from any locker or till",
	})
```

- [ ] **Step 4: Generate templ + build + vet**

Run: `make templ && go build ./... && go vet ./plugins/recharge/`
Expected: templ generates `admin_bills_templ.go`; build and vet clean.

- [ ] **Step 5: Run the package tests**

Run: `go test ./plugins/recharge/ ./internal/web/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add plugins/recharge/admin_bills.go plugins/recharge/admin_bills.templ plugins/recharge/admin_bills_templ.go plugins/recharge/recharge.go
git commit -m "feat(recharge): admin bill-payment & cash page with two free storage pickers

Admin can pay a bill or hand out cash choosing any locker/till on both the
account and physical-cash sides, reusing cashflow.MoveTx, the bill slip and
receipts tabs. Session-less recharge_bill_tx row; drawer kick when a till is
involved; shop print policy.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 4: Live verification + report correctness

Live E2E on the dev DB (the recharge package has no DB test harness — money movement is proven live, matching the package convention). Confirm the flows and that every report reading these sources still adds up.

**Files:** none (verification only).

- [ ] **Step 1: Start the dev server**

Run: `make dev` (brings up the DB, regenerates assets, runs the server). Log in as an admin.

- [ ] **Step 2: Admin billpay happy path**

On Reload & Bills → **Bill payment & cash**: type = Pay a bill, account = a bank locker (note its balance), cash = the Safe locker (note its balance), amount = 100, service charge = 20, reference = TEST1. Record.
Expected: bank balance −100; Safe balance +120; a bill slip prints or the Print/Skip prompt shows; the row appears in admin Receipts → **Bills** tab with amount 100 / service 20; **Money → Cash Flow** shows two CR- legs (account→biller 100, customer→Safe 120) with correct signs and no duplicates.

- [ ] **Step 3: Admin getmoney with service charge**

Type = Give cash out, cash = Safe (note balance), account = a bank locker (note balance), amount = 100, service charge = 20. Record.
Expected: Safe −100 then +20 (net −80); bank +100; three CR- legs (Safe→customer 100, customer→bank 100, customer→Safe 20); Bills tab shows the getmoney row.

- [ ] **Step 4: Till on the cash side pops the drawer**

Enable the open-cash-drawer setting. Open a till as a cashier (second session/browser). As admin, run a billpay with cash = that open till. Confirm the drawer kick fires (emulator or hardware) and the till balance rises by amount + service.

- [ ] **Step 5: Overdraw + same-pile guards**

Try a billpay whose amount exceeds a non-negative bank's balance → rejected, nothing moves (verify balances unchanged). Try account = cash = same pile → "must be different places". Try a bank that `allow_negative = true` → the same overdraw is **allowed** (balance goes negative).

- [ ] **Step 6: Report correctness (the spec's dedicated section)**

- Reload & Bills **Report**: note `Service charge earned` before, run an admin billpay with service 50, reload the Report → the figure rises by exactly 50 (admin rows are counted via `BillLedger`, which has no session filter). `Float on hand` and the per-type bars are **unchanged** (bill-pay touches no device float and does not write the float ledger).
- **Ledger** page + CSV: the admin rows are absent (ledger is the float/reload log) — bill rows live only in the Bills receipts tab. Confirm no accidental leakage.
- **P&L / earnings**: run one cashier billpay (service 50) and one admin billpay (service 50) and confirm both raise shop earnings identically — the admin path introduces no different accounting.
- If any figure disagrees, stop and reconcile before shipping.

- [ ] **Step 7: Cashier regression**

As the owner, turn `cashier_access` OFF on a bank (Money → Cash Lockers). As a cashier, open Reload & Bills → that bank no longer appears in bill-pay; if there are no accessible banks, the form shows the empty-banks state and cannot submit. Turn it back ON → it reappears and a bill-pay works exactly as before.

- [ ] **Step 8: Tidy the dev DB**

Reverse or reset the test rows (`make reset-demo` if you want a clean baseline). Note results in the commit/PR description.

---

## Self-Review

**Spec coverage:**
- Admin bill-pay/get-money page with two free storage pickers → Task 3. ✓
- Money flow via one cashflow.MoveTx tx, both types, service charge → Task 1 (builder) + Task 3 (handler). ✓
- Record session-less + receipts/slip reuse → Task 3. ✓
- Cashier `cashier_access` enforcement (picker + POST) → Task 2. ✓
- Negative-allowed lockers via existing guard → Global Constraints + Task 4 step 5. ✓
- No migration → Global Constraints. ✓
- Report correctness → Task 4 step 6. ✓
- Drawer kick when a till is involved → Task 3 + Task 4 step 4. ✓

**Placeholder scan:** No TBD/TODO; every code step has real code; verification steps have concrete expected outcomes.

**Type consistency:** `bankLeg` fields `From/To/Amount/Party` are used consistently in Task 1, Task 2, and Task 3. `buildBankLegs`/`bankUsableByCashier` signatures match across tasks. `BillTxInput` fields (`SessionID *int64`, `BankLockerID int64`, `BankName`, `Type`, `Amount`, `ServiceCharge`, `Reference`, `Note`, `CreatedBy`) match `recon.go`. `cashflow.MoveInput` / `Receipt.FromLabel`/`ToLabel` match `cashflow.go`.
