# Unified Receipts (Sales · Cash · Warranty) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give cashiers and admins one **Receipts** surface — three tabs (Sales `S-`, Cash `CR-`, Warranty) — to find, view, reprint, and filter every receipt the shop hands out.

**Architecture:** Per-tab HTMX fragments (no schema change, no UNION), mirroring the existing `returns`/`credit` page+`/table`-fragment idiom. Each receipt type keeps its own repository/query; the page is a shell whose tabs lazy-load a table fragment. Cashier gains view/reprint of `CR-` money receipts (today admin-only); warranty replacements become listable/reprintable from the existing `warranty_claims` rows.

**Tech Stack:** Go, Echo v4, sqlx, Templ, Tailwind v3, HTMX, Alpine.js, Postgres 17, shopspring/decimal.

## Global Constraints

- Do NOT commit `cmd/server/enabled_plugins.go` (keep remote core-only).
- Do NOT stage/commit `static/css/tailwind.css`; run `make css` if new utility classes appear, but leave it unstaged.
- Run `make templ` after editing any `.templ` file; generated `*_templ.go` are gitignored (not committed).
- Commit messages end with: `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`
- `/cashier/*` routes are available to ALL authenticated roles (group `cg`); `/admin/*` is admin+manager only (group `ag`).
- Verify build with `make templ && go build ./... && go vet ./...` (must be green) before every commit.
- No new tables, no migrations. View/reprint only — receipt generation is unchanged.
- `cashflow.ReceiptService.List/Get` and `cashflow.ReceiptFilter` already exist and are reused as-is.
- Dev: server `make run` (loads `.env`, port 3000); admin login `0000000001`/`2273`; DB `docker exec pos_db psql -U pos_user -d pos_db`.

---

### Task 1: Warranty read methods (ListClaims + GetClaim)

Add a global recent-claims list (with display joins + date-range filter) and a single-claim getter, so the Warranty tab can list and reprint. Today only `ClaimsForUnit(unitID)` exists.

**Files:**
- Modify: `internal/features/warranty/warranty.go` (Claim struct + Repository methods)
- Modify: `internal/features/warranty/service.go` (Service wrappers)
- Test: `internal/features/warranty/service_test.go` (add cases)

**Interfaces:**
- Produces:
  - `warranty.ClaimFilter{ Search string; From, To *time.Time; Limit int }`
  - `(*warranty.Repository).ListClaims(ctx, ClaimFilter) ([]Claim, error)`
  - `(*warranty.Repository).GetClaim(ctx, id int64) (*Claim, error)`
  - `(*warranty.Service).ListClaims(ctx, ClaimFilter) ([]Claim, error)`
  - `(*warranty.Service).GetClaim(ctx, id int64) (*Claim, error)`
  - `Claim` gains read-only joined fields: `ProductName string`, `OldSerial string`, `CustomerName *string`
- Consumes: existing `Claim`, `Repository`, `Service`, `db.Queryer`.

- [ ] **Step 1: Add joined display fields to the Claim struct**

In `internal/features/warranty/warranty.go`, extend the `Claim` struct's `// Joined:` block (currently `HandledByName`, `ReplacementSerial`) with three more:

```go
	// Joined:
	HandledByName     string  `db:"handled_by_name"    json:"handled_by_name"`
	ReplacementSerial *string `db:"replacement_serial" json:"replacement_serial,omitempty"`
	ProductName       string  `db:"product_name"       json:"product_name"`
	OldSerial         string  `db:"old_serial"         json:"old_serial"`
	CustomerName      *string `db:"customer_name"      json:"customer_name,omitempty"`
```

- [ ] **Step 2: Add ClaimFilter type + ListClaims + GetClaim to the repository**

In `internal/features/warranty/warranty.go`, add after `ClaimsForUnit`:

```go
// ClaimFilter narrows the global claims list. To is exclusive (the web layer
// passes the day after the chosen end date, matching the report range helper).
type ClaimFilter struct {
	Search string // matches old/new serial or customer name (blank = any)
	From   *time.Time
	To     *time.Time
	Limit  int
}

// claimSelect lists claims with everything the receipts table + a reprinted slip
// need: the faulty (old) unit's product/serial/customer and the replacement
// unit's serial.
const claimSelect = `
	SELECT wc.*, u.name AS handled_by_name,
	       ou.serial_no AS old_serial,
	       ou.product_id, p.name AS product_name,
	       c.name AS customer_name,
	       ru.serial_no AS replacement_serial
	FROM warranty_claims wc
	JOIN users u                ON u.id = wc.handled_by
	JOIN warranty_units ou      ON ou.id = wc.unit_id
	JOIN products p             ON p.id = ou.product_id
	LEFT JOIN customers c       ON c.id = ou.customer_id
	LEFT JOIN warranty_units ru ON ru.id = wc.replacement_unit_id`

// ListClaims returns recent claims across all units, newest first.
func (r *Repository) ListClaims(ctx context.Context, f ClaimFilter) ([]Claim, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var s *string
	if f.Search != "" {
		s = &f.Search
	}
	var rows []Claim
	err := r.q.SelectContext(ctx, &rows, claimSelect+`
		WHERE ($1::timestamptz IS NULL OR wc.created_at >= $1)
		  AND ($2::timestamptz IS NULL OR wc.created_at <  $2)
		  AND ($3::text IS NULL
		       OR ou.serial_no ILIKE '%' || $3 || '%'
		       OR ru.serial_no ILIKE '%' || $3 || '%'
		       OR c.name       ILIKE '%' || $3 || '%')
		ORDER BY wc.id DESC
		LIMIT $4`, f.From, f.To, s, limit)
	return rows, err
}

// GetClaim loads one claim with the same joined fields as ListClaims.
func (r *Repository) GetClaim(ctx context.Context, id int64) (*Claim, error) {
	var cl Claim
	if err := r.q.GetContext(ctx, &cl, claimSelect+` WHERE wc.id = $1`, id); err != nil {
		return nil, err
	}
	return &cl, nil
}
```

Note: `product_id` is selected only to satisfy the join readability; it maps to the existing `Claim` fields via `db` tags — `Claim` has no `product_id` tag, and sqlx ignores unmapped columns only if `db.Queryer` uses `Unsafe`. To avoid a scan error, REMOVE `ou.product_id,` from `claimSelect` (it is not needed by any consumer). Final `claimSelect` SELECT list is: `wc.*, u.name AS handled_by_name, ou.serial_no AS old_serial, p.name AS product_name, c.name AS customer_name, ru.serial_no AS replacement_serial`.

- [ ] **Step 3: Add the import for `time` if missing**

`internal/features/warranty/warranty.go` already imports `time` (used by `Unit`/`Claim`). Confirm; no change if present.

- [ ] **Step 4: Add Service wrappers**

In `internal/features/warranty/service.go`, add after `List`:

```go
// ListClaims returns recent warranty claims for the receipts/warranty tab.
func (s *Service) ListClaims(ctx context.Context, f ClaimFilter) ([]Claim, error) {
	rows, err := s.repo.ListClaims(ctx, f)
	if err != nil {
		return nil, apperr.Internal("failed to list warranty claims", err)
	}
	return rows, nil
}

// GetClaim loads one claim (for view / reprint).
func (s *Service) GetClaim(ctx context.Context, id int64) (*Claim, error) {
	cl, err := s.repo.GetClaim(ctx, id)
	if err != nil {
		return nil, apperr.NotFound("warranty claim")
	}
	return cl, nil
}
```

- [ ] **Step 5: Write the failing test**

In `internal/features/warranty/service_test.go`, add a test that records a replacement then lists + gets the claim. Match the existing test file's setup helpers (inspect the top of the file for the existing `newTestService`/DB-fixture pattern and reuse it verbatim — do not invent a new harness).

```go
func TestListAndGetClaim(t *testing.T) {
	svc, ctx := newTestService(t) // reuse whatever the file already uses
	unitID := seedWarrantyUnit(t, svc, "OLD-001")   // reuse/define per existing helpers
	if _, err := svc.RecordReplacement(ctx, unitID, "NEW-001", "screen dead", 1); err != nil {
		t.Fatalf("record replacement: %v", err)
	}

	claims, err := svc.ListClaims(ctx, warranty.ClaimFilter{})
	if err != nil {
		t.Fatalf("list claims: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("want 1 claim, got %d", len(claims))
	}
	c := claims[0]
	if c.OldSerial != "OLD-001" || c.ReplacementSerial == nil || *c.ReplacementSerial != "NEW-001" {
		t.Fatalf("bad join: old=%q new=%v", c.OldSerial, c.ReplacementSerial)
	}
	if c.ProductName == "" {
		t.Fatalf("product name not joined")
	}

	got, err := svc.GetClaim(ctx, c.ID)
	if err != nil {
		t.Fatalf("get claim: %v", err)
	}
	if got.ID != c.ID || got.OldSerial != "OLD-001" {
		t.Fatalf("get mismatch: %+v", got)
	}
}
```

If `service_test.go` has no DB fixture (it may be a pure unit test against a fake), instead place this test in a new `internal/features/warranty/claims_test.go` guarded the same way the repo's other DB tests are guarded in this codebase (look for the existing build tag / `testdb` helper used elsewhere, e.g. in `internal/features/cashflow` or `internal/db`). Reuse that exact pattern.

- [ ] **Step 6: Run the test to verify it fails**

Run: `go test ./internal/features/warranty/ -run TestListAndGetClaim -v`
Expected: FAIL (method/fields undefined or assertion fails before implementation) — if it PASSES immediately, the implementation from steps 1–4 is already compiled in; that's fine, proceed.

- [ ] **Step 7: Run the full package test + build**

Run: `go test ./internal/features/warranty/ -v && go build ./...`
Expected: PASS + build green.

- [ ] **Step 8: Commit**

```bash
gofmt -w internal/features/warranty/warranty.go internal/features/warranty/service.go internal/features/warranty/*_test.go
git add internal/features/warranty/warranty.go internal/features/warranty/service.go internal/features/warranty/*_test.go
git commit -m "feat(warranty): ListClaims + GetClaim for the receipts warranty tab

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: Shared date-range form component

The cashier templ package can't call admin's unexported `rptRangeForm`. Add an exported `shared.RangeForm` in the existing `templates/shared` package (`package shared`) for the cashier tabs.

**Files:**
- Create: `templates/shared/rangeform.templ`
- Test: build + visual (no Go unit test for templ)

**Interfaces:**
- Produces: `shared.RangeForm(action, preset, from, to string)` templ component (renders preset buttons + from/to date inputs that GET `action` with `preset`/`from`/`to` query params).
- Consumes: nothing (self-contained markup).

- [ ] **Step 1: Create the shared component**

`templates/shared/rangeform.templ`:

```templ
package shared

// RangeForm renders the shared quick date-range picker (preset buttons + exact
// from/to inputs) used across reports, the locker ledger, and the receipts tabs.
// It GETs `action` with preset / from / to query params (server side resolves
// them via reports.ResolveRange).
templ RangeForm(action, preset, from, to string) {
	<div class="no-print mb-4 space-y-3">
		<div class="flex flex-wrap gap-2">
			@rangePreset(action, "today", "Today", preset)
			@rangePreset(action, "this-week", "This week", preset)
			@rangePreset(action, "this-month", "This month", preset)
			@rangePreset(action, "last-week", "Last week", preset)
			@rangePreset(action, "last-month", "Last month", preset)
			@rangePreset(action, "this-year", "This year", preset)
		</div>
		<form method="get" action={ templ.SafeURL(action) } class="flex flex-wrap items-end gap-3">
			<div>
				<label class="block text-xs text-slate-500 mb-1">From</label>
				<input type="date" name="from" value={ from } class="border rounded-lg px-3 py-1.5"/>
			</div>
			<div>
				<label class="block text-xs text-slate-500 mb-1">To</label>
				<input type="date" name="to" value={ to } class="border rounded-lg px-3 py-1.5"/>
			</div>
			{ children... }
			<button class="px-4 py-1.5 rounded-lg bg-slate-800 text-white text-sm">Apply</button>
		</form>
	</div>
}

templ rangePreset(action, key, label, active string) {
	if key == active {
		<a href={ templ.SafeURL(action + "?preset=" + key) } class="px-3 py-1.5 rounded-lg bg-indigo-600 text-white text-sm">{ label }</a>
	} else {
		<a href={ templ.SafeURL(action + "?preset=" + key) } class="px-3 py-1.5 rounded-lg border text-slate-600 text-sm hover:bg-slate-50">{ label }</a>
	}
}
```

- [ ] **Step 2: Regenerate templ + build**

Run: `make templ && go build ./...`
Expected: green (new `templates/shared/rangeform_templ.go` generated).

- [ ] **Step 3: Commit**

```bash
git add templates/shared/rangeform.templ
git commit -m "feat(ui): shared RangeForm date-range component

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: Cashier Cash tab — list, view, reprint of CR- receipts

Cashiers get a shop-wide `CR-` list (search + kind + date range), a cashier-rendered receipt view, and slip reprint. Reuses `cashflowReceipts` + `buildReceiptSlip`.

**Files:**
- Create: `internal/web/cashier_receipts.go` (cashier money-receipt handlers)
- Create: `templates/pages/cashier/receipts_cash.templ` (Cash table fragment + cashier receipt view page)
- Modify: `internal/web/web.go` (routes)
- Modify: `internal/web/cashier.go` (extract `(s *Server) receiptImgOptions` — see Step 1)

**Interfaces:**
- Consumes: `s.cashflowReceipts.List/Get`, `buildReceiptSlip(cfg, rec)`, `resolveReceiptRange(c)`, `reports.ResolveRange`, `shared.RangeForm`, `cashflow.ReceiptFilter`, `cashflow.Receipt`.
- Produces:
  - `(h *cashierUI) ReceiptsCash(c) error` → renders `cashierpages.ReceiptsCashTab`
  - `(h *cashierUI) MoneyReceipt(c) error` → renders `cashierpages.MoneyReceiptPage`
  - `(h *cashierUI) MoneyReceiptPrint(c) error` → reprint slip, toast
  - `cashierpages.ReceiptsCashTab(ReceiptsCashData)` and `cashierpages.MoneyReceiptPage(MoneyReceiptViewData)` templ components
  - `(s *Server) receiptImgOptions(ctx, cfg) escpos.Options`

- [ ] **Step 1: Extract receiptImgOptions to Server (DRY for warranty reuse in Task 4)**

In `internal/web/cashier.go`, change `receiptOptions` to delegate to a new Server method. Replace the body of `func (h *cashierUI) receiptOptions(...)` so it calls `h.s.receiptImgOptions(ctx, cfg)`, and add the Server method holding the moved logic:

```go
func (h *cashierUI) receiptOptions(ctx context.Context, cfg *settings.Settings) escpos.Options {
	return h.s.receiptImgOptions(ctx, cfg)
}

// receiptImgOptions builds the logo/sub-name raster options for a thermal slip.
// Shared by sale receipts and warranty/CR- reprints.
func (s *Server) receiptImgOptions(ctx context.Context, cfg *settings.Settings) escpos.Options {
	var opts escpos.Options
	dots := receiptimg.PrinterDots(cfg.ReceiptWidth)
	if src := cfg.LogoSrc(); src != "" {
		if img, err := receiptimg.LoadImage(ctx, src, poststatic.Files); err == nil {
			opts.Logo = receiptimg.Logo(img, dots)
		}
	}
	if cfg.ShopNameSi != nil && *cfg.ShopNameSi != "" {
		opts.SubName = receiptimg.SubName(*cfg.ShopNameSi, dots, dots/14)
	}
	return opts
}
```

Run `go build ./...` to confirm the move compiles (imports `receiptimg`, `poststatic`, `escpos` already live in `cashier.go`; if the Server method needs them and they're file-scoped, they are package-scoped imports so it is fine).

- [ ] **Step 2: Create the cashier money-receipt handlers**

`internal/web/cashier_receipts.go`:

```go
package web

import (
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/internal/escpos"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/middleware"
	"karots-pos/internal/printing"
	"karots-pos/internal/response"
	cashierpages "karots-pos/templates/pages/cashier"
)

// ReceiptsCash renders the Cash tab fragment: shop-wide CR- money receipts with
// search, kind filter, and the shared date-range presets.
func (h *cashierUI) ReceiptsCash(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := resolveReceiptRange(c)
	if err != nil {
		return err
	}
	q := strings.TrimSpace(c.QueryParam("q"))
	kind := strings.TrimSpace(c.QueryParam("kind"))
	rows, err := h.s.cashflowReceipts.List(ctx, cashflow.ReceiptFilter{Query: q, Kind: kind, From: from, To: to})
	if err != nil {
		return err
	}
	return response.RenderFragment(c, cashierpages.ReceiptsCashTab(cashierpages.ReceiptsCashData{
		Symbol: h.cashierSymbol(ctx),
		Rows:   rows,
		Query:  q,
		Kind:   kind,
		Preset: c.QueryParam("preset"),
		From:   fromStr,
		To:     toStr,
	}))
}

// MoneyReceipt renders one CR- receipt as a cashier-accessible print-friendly page.
func (h *cashierUI) MoneyReceipt(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	rec, err := h.s.cashflowReceipts.Get(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := h.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, cashierpages.MoneyReceiptPage(cashierpages.MoneyReceiptViewData{
		CashierName:   middleware.CurrentUserName(c),
		Role:          middleware.CurrentRole(c),
		ShowChangePin: h.showChangePin(c),
		Symbol:        h.cashierSymbol(ctx),
		Settings:      *cfg,
		Receipt:       *rec,
	}))
}

// MoneyReceiptPrint re-sends a CR- receipt's thermal slip from the terminal.
func (h *cashierUI) MoneyReceiptPrint(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	rec, err := h.s.cashflowReceipts.Get(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := h.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	target := h.receiptQueue(c, cfg)
	if strings.TrimSpace(target) == "" {
		c.Response().Header().Set("HX-Trigger", response.Toast("No receipt printer configured", "error"))
		return c.NoContent(200)
	}
	if err := printing.Raw(ctx, target, buildReceiptSlip(cfg, *rec)); err != nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("Print failed: "+err.Error(), "error"))
		return c.NoContent(200)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Slip sent to printer", "success"))
	return c.NoContent(200)
}

var _ = escpos.Send // keep escpos import if unused elsewhere in this file (remove if it causes "imported and not used")
```

Remove the `escpos` import + the `var _ =` line if `go build` reports it unused (it is only a guard; prefer deleting both).

- [ ] **Step 3: Create the Cash tab fragment + cashier receipt view templates**

`templates/pages/cashier/receipts_cash.templ`:

```templ
package cashierpages

import (
	"strconv"

	"karots-pos/internal/datetime"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/settings"
	"karots-pos/internal/money"
	"karots-pos/templates/layouts"
	"karots-pos/templates/shared"
)

type ReceiptsCashData struct {
	Symbol string
	Rows   []cashflow.Receipt
	Query  string
	Kind   string
	Preset string
	From   string
	To     string
}

// ReceiptsCashTab is the Cash tab fragment: filter bar + CR- receipts table.
templ ReceiptsCashTab(d ReceiptsCashData) {
	<div>
		@shared.RangeForm("/cashier/receipts/cash", d.Preset, d.From, d.To) {
			<input type="search" name="q" value={ d.Query } placeholder="No / party / location…" class="border rounded-lg px-3 py-1.5"/>
			<select name="kind" class="border rounded-lg px-3 py-1.5">
				@cashKindOption("", "All kinds", d.Kind)
				@cashKindOption("transfer", "Transfer", d.Kind)
				@cashKindOption("expense", "Expense", d.Kind)
				@cashKindOption("supplier_payment", "Supplier payment", d.Kind)
				@cashKindOption("customer_payment", "Customer payment", d.Kind)
				@cashKindOption("refund", "Refund", d.Kind)
				@cashKindOption("bank_charge", "Bank charge", d.Kind)
			</select>
		}
		<div class="bg-white rounded-2xl shadow-sm overflow-hidden">
			<table class="w-full text-sm">
				<thead class="text-left text-slate-500 border-b bg-slate-50">
					<tr><th class="px-4 py-2">Receipt</th><th class="px-4 py-2">Date</th><th class="px-4 py-2">From → To</th><th class="px-4 py-2">Party</th><th class="px-4 py-2 text-right">Amount</th><th class="px-4 py-2 text-right">Print</th></tr>
				</thead>
				<tbody>
					for _, r := range d.Rows {
						<tr class="border-b last:border-0">
							<td class="px-4 py-2 font-medium">{ r.ReceiptNo }</td>
							<td class="px-4 py-2 text-slate-500">{ datetime.DateTime(r.CreatedAt) }</td>
							<td class="px-4 py-2">{ r.FromLabel } → { r.ToLabel }</td>
							<td class="px-4 py-2">{ r.Party }</td>
							<td class="px-4 py-2 text-right">{ money.Format(d.Symbol, r.Amount) }</td>
							<td class="px-4 py-2 text-right">
								<button type="button" hx-post={ "/cashier/money-receipts/" + strconv.FormatInt(r.ID, 10) + "/print" } hx-swap="none" class="px-3 py-1 rounded-lg bg-indigo-600 text-white text-xs font-medium">🧾 Reprint</button>
								<a href={ templ.SafeURL("/cashier/money-receipts/" + strconv.FormatInt(r.ID, 10)) } target="_blank" class="ml-2 px-3 py-1 rounded-lg border text-slate-600 text-xs font-medium">View</a>
							</td>
						</tr>
					}
					if len(d.Rows) == 0 {
						<tr><td colspan="6" class="px-4 py-8 text-center text-slate-400">No matching cash receipts.</td></tr>
					}
				</tbody>
			</table>
		</div>
	</div>
}

templ cashKindOption(val, label, active string) {
	if val == active {
		<option value={ val } selected>{ label }</option>
	} else {
		<option value={ val }>{ label }</option>
	}
}

type MoneyReceiptViewData struct {
	CashierName   string
	Role          string
	ShowChangePin bool
	Symbol        string
	Settings      settings.Settings
	Receipt       cashflow.Receipt
}

// MoneyReceiptPage shows one CR- receipt print-friendly inside the cashier shell.
templ MoneyReceiptPage(d MoneyReceiptViewData) {
	@layouts.Cashier("Receipt "+d.Receipt.ReceiptNo, d.CashierName, d.Role, "receipts", d.ShowChangePin) {
		<div class="h-full overflow-auto p-5">
			<div class="max-w-sm mx-auto bg-white rounded-2xl shadow-sm p-6">
				<div class="text-center mb-3">
					<div class="font-bold text-lg">{ d.Settings.ShopName }</div>
					if d.Settings.Phone != nil {
						<div class="text-xs text-slate-500">{ *d.Settings.Phone }</div>
					}
				</div>
				<div class="text-center font-semibold uppercase text-sm border-y py-1 mb-3">Cash Receipt · { d.Receipt.ReceiptNo }</div>
				<dl class="text-sm space-y-1">
					<div class="flex justify-between"><dt class="text-slate-500">Date</dt><dd>{ datetime.DateTime(d.Receipt.CreatedAt) }</dd></div>
					<div class="flex justify-between"><dt class="text-slate-500">From</dt><dd>{ d.Receipt.FromLabel }</dd></div>
					<div class="flex justify-between"><dt class="text-slate-500">To</dt><dd>{ d.Receipt.ToLabel }</dd></div>
					if d.Receipt.Party != "" {
						<div class="flex justify-between"><dt class="text-slate-500">Party</dt><dd>{ d.Receipt.Party }</dd></div>
					}
					<div class="flex justify-between font-semibold text-base border-t pt-1 mt-1"><dt>Amount</dt><dd>{ money.Format(d.Symbol, d.Receipt.Amount) }</dd></div>
					if d.Receipt.Note != "" {
						<div class="flex justify-between"><dt class="text-slate-500">Note</dt><dd>{ d.Receipt.Note }</dd></div>
					}
				</dl>
				<div class="flex gap-2 mt-5 no-print">
					<button onclick="window.print()" class="flex-1 py-2 rounded-lg bg-slate-800 text-white text-sm">Print</button>
					<button type="button" hx-post={ "/cashier/money-receipts/" + strconv.FormatInt(d.Receipt.ID, 10) + "/print" } hx-swap="none" class="flex-1 py-2 rounded-lg bg-indigo-600 text-white text-sm">🧾 Reprint slip</button>
				</div>
			</div>
		</div>
	}
}
```

- [ ] **Step 4: Wire routes**

In `internal/web/web.go`, in the cashier group (`cg`), after `cg.GET("/receipts", cashier.Receipts)` add:

```go
	cg.GET("/receipts/cash", cashier.ReceiptsCash)
	cg.GET("/money-receipts/:id", cashier.MoneyReceipt)
	cg.POST("/money-receipts/:id/print", cashier.MoneyReceiptPrint)
```

- [ ] **Step 5: Build**

Run: `make templ && go build ./... && go vet ./...`
Expected: green. Fix any unused-import errors (notably the `escpos` guard in Step 2).

- [ ] **Step 6: Smoke test the fragment**

Start `make run` (background). Then:
Run: `curl -s -c /tmp/cj.txt -b /tmp/cj.txt -X POST localhost:3000/api/auth/login -H 'Content-Type: application/json' -d '{"phone":"0000000001","pin":"2273"}' >/dev/null && curl -s -b /tmp/cj.txt 'localhost:3000/cashier/receipts/cash' | grep -c 'Cash Receipt\|cash receipts\|Receipt'`
Expected: a non-zero count (fragment renders). (If login route differs, use the Playwright flow from Task 7 instead.)

- [ ] **Step 7: Commit**

```bash
gofmt -w internal/web/cashier.go internal/web/cashier_receipts.go internal/web/web.go
git add internal/web/cashier.go internal/web/cashier_receipts.go internal/web/web.go templates/pages/cashier/receipts_cash.templ
git commit -m "feat(cashier): Cash tab — view/reprint CR- money receipts

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 4: Cashier Warranty tab — list + reprint

Cashiers get a recent warranty-claims list (search + date range) and slip reprint, reconstructed from `warranty_claims` via Task 1's methods + the existing `FindUnitByID`.

**Files:**
- Modify: `internal/web/cashier_receipts.go` (warranty handlers + shared slip builder)
- Create: `templates/pages/cashier/receipts_warranty.templ`
- Modify: `internal/web/web.go` (routes)
- Modify: `internal/web/cashier.go` (refactor `printWarrantySlip` to reuse the shared builder — optional, see Step 1)

**Interfaces:**
- Consumes: `h.s.warranty.ListClaims/GetClaim/repo FindUnitByID`, `s.receiptImgOptions` (Task 3), `escpos.WarrantyDocument`, `escpos.WarrantySlip`, `printing.Raw`, `datetime.Date`, `warranty.ClaimFilter`, `warranty.Claim`, `warranty.Unit`.
- Produces:
  - `(h *cashierUI) ReceiptsWarranty(c) error`
  - `(h *cashierUI) WarrantyReprint(c) error`
  - `(s *Server) buildWarrantySlip(ctx, cfg, oldSerial string, u *warranty.Unit) []byte`
  - `cashierpages.ReceiptsWarrantyTab(ReceiptsWarrantyData)` templ

- [ ] **Step 1: Add a Server-level warranty slip builder**

`warranty.Service` exposes `RecordReplacement` and `GetClaim`/`FindUnitByID` (repo). To reprint, load both units. Add a method on `warranty.Service` to fetch a unit by id (the repo has `FindUnitByID` but the service may not expose it — check; if absent, add it):

In `internal/features/warranty/service.go` (only if not already present):

```go
// GetUnit loads one warranty unit (for reprinting a replacement slip).
func (s *Service) GetUnit(ctx context.Context, id int64) (*Unit, error) {
	u, err := s.repo.FindUnitByID(ctx, id)
	if err != nil {
		return nil, apperr.NotFound("warranty unit")
	}
	return u, nil
}
```

In `internal/web/cashier_receipts.go`, add the shared builder + warranty handlers (append to the file; extend the import block with `karots-pos/internal/datetime`, `karots-pos/internal/escpos`, `karots-pos/internal/features/settings`, `karots-pos/internal/features/warranty`):

```go
// buildWarrantySlip renders a replacement slip for reprint (UI-agnostic).
func (s *Server) buildWarrantySlip(ctx context.Context, cfg *settings.Settings, oldSerial string, u *warranty.Unit) []byte {
	slip := escpos.WarrantySlip{
		ProductName:   u.ProductName,
		OldSerial:     oldSerial,
		NewSerial:     u.SerialNo,
		WarrantyUntil: datetime.Date(u.WarrantyUntil),
	}
	if u.CustomerName != nil {
		slip.CustomerName = *u.CustomerName
	}
	return escpos.WarrantyDocument(slip, *cfg, s.receiptImgOptions(ctx, cfg))
}

// ReceiptsWarranty renders the Warranty tab fragment.
func (h *cashierUI) ReceiptsWarranty(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := resolveReceiptRange(c)
	if err != nil {
		return err
	}
	q := strings.TrimSpace(c.QueryParam("q"))
	rows, err := h.s.warranty.ListClaims(ctx, warranty.ClaimFilter{Search: q, From: from, To: to})
	if err != nil {
		return err
	}
	return response.RenderFragment(c, cashierpages.ReceiptsWarrantyTab(cashierpages.ReceiptsWarrantyData{
		Rows: rows, Query: q, Preset: c.QueryParam("preset"), From: fromStr, To: toStr,
	}))
}

// WarrantyReprint re-sends a warranty replacement slip.
func (h *cashierUI) WarrantyReprint(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	cl, err := h.s.warranty.GetClaim(ctx, id)
	if err != nil {
		return err
	}
	if cl.ReplacementUnitID == nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("No replacement slip for this claim", "error"))
		return c.NoContent(200)
	}
	newUnit, err := h.s.warranty.GetUnit(ctx, *cl.ReplacementUnitID)
	if err != nil {
		return err
	}
	cfg, err := h.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	target := h.receiptQueue(c, cfg)
	if strings.TrimSpace(target) == "" {
		c.Response().Header().Set("HX-Trigger", response.Toast("No receipt printer configured", "error"))
		return c.NoContent(200)
	}
	if err := printing.Raw(ctx, target, h.s.buildWarrantySlip(ctx, cfg, cl.OldSerial, newUnit)); err != nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("Print failed: "+err.Error(), "error"))
		return c.NoContent(200)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Warranty slip sent to printer", "success"))
	return c.NoContent(200)
}
```

- [ ] **Step 2: (Optional) Point the original printWarrantySlip at the shared builder**

In `internal/web/cashier.go`, replace `printWarrantySlip`'s body so the create path and reprint share rendering:

```go
func (h *cashierUI) printWarrantySlip(c echo.Context, oldSerial string, u *warranty.Unit) {
	ctx := c.Request().Context()
	cfg, err := h.s.settings.Get(ctx)
	if err != nil {
		log.Printf("warranty slip: load settings: %v", err)
		return
	}
	if err := escpos.Send(ctx, h.receiptQueue(c, cfg), h.s.buildWarrantySlip(ctx, cfg, oldSerial, u)); err != nil {
		log.Printf("warranty slip: print for %s: %v", u.SerialNo, err)
	}
}
```

- [ ] **Step 3: Create the Warranty tab fragment template**

`templates/pages/cashier/receipts_warranty.templ`:

```templ
package cashierpages

import (
	"strconv"

	"karots-pos/internal/datetime"
	"karots-pos/internal/features/warranty"
	"karots-pos/templates/shared"
)

type ReceiptsWarrantyData struct {
	Rows   []warranty.Claim
	Query  string
	Preset string
	From   string
	To     string
}

// ReceiptsWarrantyTab is the Warranty tab fragment: filter bar + claims table.
templ ReceiptsWarrantyTab(d ReceiptsWarrantyData) {
	<div>
		@shared.RangeForm("/cashier/receipts/warranty", d.Preset, d.From, d.To) {
			<input type="search" name="q" value={ d.Query } placeholder="Serial / customer…" class="border rounded-lg px-3 py-1.5"/>
		}
		<div class="bg-white rounded-2xl shadow-sm overflow-hidden">
			<table class="w-full text-sm">
				<thead class="text-left text-slate-500 border-b bg-slate-50">
					<tr><th class="px-4 py-2">Date</th><th class="px-4 py-2">Product</th><th class="px-4 py-2">Old → New serial</th><th class="px-4 py-2">Customer</th><th class="px-4 py-2">By</th><th class="px-4 py-2 text-right">Print</th></tr>
				</thead>
				<tbody>
					for _, cl := range d.Rows {
						<tr class="border-b last:border-0">
							<td class="px-4 py-2 text-slate-500">{ datetime.DateTime(cl.CreatedAt) }</td>
							<td class="px-4 py-2">{ cl.ProductName }</td>
							<td class="px-4 py-2">
								{ cl.OldSerial } →
								if cl.ReplacementSerial != nil {
									{ *cl.ReplacementSerial }
								} else {
									—
								}
							</td>
							<td class="px-4 py-2">
								if cl.CustomerName != nil {
									{ *cl.CustomerName }
								} else {
									Walk-in
								}
							</td>
							<td class="px-4 py-2 text-slate-500">{ cl.HandledByName }</td>
							<td class="px-4 py-2 text-right">
								if cl.ReplacementUnitID != nil {
									<button type="button" hx-post={ "/cashier/warranty/" + strconv.FormatInt(cl.ID, 10) + "/print" } hx-swap="none" class="px-3 py-1 rounded-lg bg-indigo-600 text-white text-xs font-medium">🧾 Reprint</button>
								}
							</td>
						</tr>
					}
					if len(d.Rows) == 0 {
						<tr><td colspan="6" class="px-4 py-8 text-center text-slate-400">No warranty replacements.</td></tr>
					}
				</tbody>
			</table>
		</div>
	</div>
}
```

- [ ] **Step 4: Wire routes**

In `internal/web/web.go` cashier group, after the money-receipts routes from Task 3 add:

```go
	cg.GET("/receipts/warranty", cashier.ReceiptsWarranty)
	cg.POST("/warranty/:claimId/print", cashier.WarrantyReprint)
```

Note: the existing warranty routes use `:` params elsewhere; `:claimId` is distinct from `/cashier/warranty/replace` (POST) — Echo matches the static `replace` separately. Confirm no route conflict with `cg.POST("/warranty/replace", ...)` by building/running; if Echo complains about conflicting routes, rename to `cg.POST("/receipts/warranty/:claimId/print", cashier.WarrantyReprint)` and update the template's hx-post URL to match.

- [ ] **Step 5: Build**

Run: `make templ && go build ./... && go vet ./...`
Expected: green.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/web/cashier.go internal/web/cashier_receipts.go internal/web/web.go internal/features/warranty/service.go
git add internal/web/cashier.go internal/web/cashier_receipts.go internal/web/web.go internal/features/warranty/service.go templates/pages/cashier/receipts_warranty.templ
git commit -m "feat(cashier): Warranty tab — list + reprint replacement slips

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 5: Cashier Receipts page — three tabs

Turn `/cashier/receipts` into a tabbed shell: Sales (existing, default) + lazy-loaded Cash and Warranty tabs. Sales tab gains the shared date range.

**Files:**
- Modify: `templates/pages/cashier/receipts.templ` (tab strip + Sales tab with range form)
- Modify: `internal/web/cashier.go` (`Receipts` handler — add date range + tab param; add `ReceiptsSales` fragment)
- Modify: `internal/web/web.go` (sales fragment route)

**Interfaces:**
- Consumes: `sales.List`, `sales.ListFilter` (has `From`, `To`, `Query`, `Limit`), `resolveReceiptRange`, `shared.RangeForm`, existing `cashierpages.ReceiptsData`.
- Produces: `(h *cashierUI) ReceiptsSales(c) error` (Sales fragment); `cashierpages.Receipts` renders the tab shell; `cashierpages.ReceiptsSalesTab(ReceiptsData)` fragment.

- [ ] **Step 1: Add the Sales fragment handler + date range to Receipts**

In `internal/web/cashier.go`, replace the `Receipts` handler and add `ReceiptsSales`:

```go
// Receipts renders the tabbed Receipts shell (Sales tab loaded inline as default).
func (h *cashierUI) Receipts(c echo.Context) error {
	ctx := c.Request().Context()
	data, err := h.salesReceiptData(c)
	if err != nil {
		return err
	}
	return response.RenderPage(c, cashierpages.Receipts(cashierpages.ReceiptsPageData{
		CashierName:   middleware.CurrentUserName(c),
		Role:          middleware.CurrentRole(c),
		ShowChangePin: h.showChangePin(c),
		Sales:         data,
	}))
	_ = ctx
}

// ReceiptsSales renders just the Sales tab fragment (search + date range).
func (h *cashierUI) ReceiptsSales(c echo.Context) error {
	data, err := h.salesReceiptData(c)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, cashierpages.ReceiptsSalesTab(data))
}

func (h *cashierUI) salesReceiptData(c echo.Context) (cashierpages.ReceiptsData, error) {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := resolveReceiptRange(c)
	if err != nil {
		return cashierpages.ReceiptsData{}, err
	}
	q := strings.TrimSpace(c.QueryParam("q"))
	rows, err := h.s.sales.List(ctx, sales.ListFilter{Query: q, From: from, To: to, Limit: 50})
	if err != nil {
		return cashierpages.ReceiptsData{}, err
	}
	return cashierpages.ReceiptsData{
		Symbol: h.cashierSymbol(ctx),
		Query:  q,
		Sales:  rows,
		Preset: c.QueryParam("preset"),
		From:   fromStr,
		To:     toStr,
	}, nil
}
```

Remove the stray `_ = ctx` / `ctx` if it triggers an unused error — simplify `Receipts` to not bind `ctx` at all:

```go
func (h *cashierUI) Receipts(c echo.Context) error {
	data, err := h.salesReceiptData(c)
	if err != nil {
		return err
	}
	return response.RenderPage(c, cashierpages.Receipts(cashierpages.ReceiptsPageData{
		CashierName:   middleware.CurrentUserName(c),
		Role:          middleware.CurrentRole(c),
		ShowChangePin: h.showChangePin(c),
		Sales:         data,
	}))
}
```

Confirm `sales.ListFilter` has `From *time.Time` and `To *time.Time` fields (grep `type ListFilter` in `internal/features/sales/`); if the field names differ (e.g. `Start`/`End`), use the actual names. If `ListFilter` has NO date fields, add them following the existing query's WHERE pattern (small, in `sales` repo) OR drop the date range from the Sales tab and note it — but prefer adding them for consistency.

- [ ] **Step 2: Rewrite the Receipts template as a tabbed shell**

Replace `templates/pages/cashier/receipts.templ` entirely:

```templ
package cashierpages

import (
	"strconv"

	"karots-pos/internal/datetime"
	"karots-pos/internal/features/sales"
	"karots-pos/internal/money"
	"karots-pos/templates/layouts"
	"karots-pos/templates/shared"
)

type ReceiptsData struct {
	Symbol string
	Query  string
	Sales  []sales.Sale
	Preset string
	From   string
	To     string
}

type ReceiptsPageData struct {
	CashierName   string
	Role          string
	ShowChangePin bool
	Sales         ReceiptsData
}

// Receipts is the tabbed receipts shell: Sales (default, inline) · Cash · Warranty.
templ Receipts(d ReceiptsPageData) {
	@layouts.Cashier("Receipts", d.CashierName, d.Role, "receipts", d.ShowChangePin) {
		<div class="h-full overflow-auto p-5" x-data="{ tab: 'sales' }">
			<div class="max-w-4xl mx-auto">
				<h1 class="text-xl font-bold mb-3">Receipts</h1>
				<div class="flex gap-1 mb-4 border-b">
					@receiptTab("sales", "Sales", "/cashier/receipts/sales")
					@receiptTab("cash", "Cash", "/cashier/receipts/cash")
					@receiptTab("warranty", "Warranty", "/cashier/receipts/warranty")
				</div>
				<div id="receipts-sales" x-show="tab === 'sales'">
					@ReceiptsSalesTab(d.Sales)
				</div>
				<div id="receipts-cash" x-show="tab === 'cash'" hx-get="/cashier/receipts/cash" hx-trigger="load once"></div>
				<div id="receipts-warranty" x-show="tab === 'warranty'" hx-get="/cashier/receipts/warranty" hx-trigger="load once"></div>
			</div>
		</div>
	}
}

templ receiptTab(key, label, _ string) {
	<button type="button" x-on:click={ "tab = '" + key + "'" }
		class="px-4 py-2 text-sm font-medium border-b-2 -mb-px"
		x-bind:class={ "tab === '" + key + "' ? 'border-indigo-600 text-indigo-600' : 'border-transparent text-slate-500'" }>
		{ label }
	</button>
}

// ReceiptsSalesTab is the Sales tab body (search + date range + sales table).
templ ReceiptsSalesTab(d ReceiptsData) {
	<div>
		@shared.RangeForm("/cashier/receipts/sales", d.Preset, d.From, d.To) {
			<input type="search" name="q" value={ d.Query } placeholder="Receipt no / customer…" class="border rounded-lg px-3 py-1.5"/>
		}
		<div class="bg-white rounded-2xl shadow-sm overflow-hidden">
			<table class="w-full text-sm">
				<thead class="text-left text-slate-500 border-b bg-slate-50">
					<tr><th class="px-4 py-2">Receipt</th><th class="px-4 py-2">Date</th><th class="px-4 py-2">Type</th><th class="px-4 py-2 text-right">Total</th><th class="px-4 py-2 text-right">Print</th></tr>
				</thead>
				<tbody>
					for _, s := range d.Sales {
						<tr class="border-b last:border-0">
							<td class="px-4 py-2 font-medium">{ s.ReceiptNo }</td>
							<td class="px-4 py-2 text-slate-500">{ datetime.DateTime(s.CreatedAt) }</td>
							<td class="px-4 py-2 capitalize">{ s.SaleType }</td>
							<td class="px-4 py-2 text-right">{ money.Format(d.Symbol, s.Total) }</td>
							<td class="px-4 py-2 text-right">
								<button type="button" hx-post={ "/cashier/print/" + strconv.FormatInt(s.ID, 10) } hx-swap="none" class="px-3 py-1 rounded-lg bg-indigo-600 text-white text-xs font-medium">🧾 Reprint</button>
								<a href={ templ.SafeURL("/cashier/receipt/" + strconv.FormatInt(s.ID, 10)) } target="_blank" class="ml-2 px-3 py-1 rounded-lg border text-slate-600 text-xs font-medium">View</a>
							</td>
						</tr>
					}
					if len(d.Sales) == 0 {
						<tr><td colspan="5" class="px-4 py-8 text-center text-slate-400">No matching sales.</td></tr>
					}
				</tbody>
			</table>
		</div>
	</div>
}
```

Note: the Sales `RangeForm`/search form is a `GET` to `/cashier/receipts/sales`, which returns the Sales fragment; HTMX is not strictly required for that form (full navigation reloads the page on Sales). To keep filtering inside the tab without a full reload, add `hx-get="/cashier/receipts/sales" hx-target="#receipts-sales" hx-swap="innerHTML"` to the form and search — but the shared `RangeForm` uses plain `<form method=get>`. Acceptable for v1: the preset links/Apply do a full-page GET landing back on Sales (tab defaults to sales). Leave as-is; do not over-engineer.

- [ ] **Step 3: Wire the Sales fragment route**

In `internal/web/web.go` cashier group, after `cg.GET("/receipts", cashier.Receipts)`:

```go
	cg.GET("/receipts/sales", cashier.ReceiptsSales)
```

- [ ] **Step 4: Build**

Run: `make templ && go build ./... && go vet ./...`
Expected: green. (`ReceiptsData` shape changed — `CashierName`/`Role`/`ShowChangePin` moved to `ReceiptsPageData`; ensure no other caller references the old `ReceiptsData` fields. Grep `cashierpages.ReceiptsData{` / `ReceiptsData{` to confirm only the receipts handlers use it.)

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/web/cashier.go internal/web/web.go
make css   # in case tab utility classes are new; do NOT stage tailwind.css
git add internal/web/cashier.go internal/web/web.go templates/pages/cashier/receipts.templ
git commit -m "feat(cashier): tabbed Receipts (Sales/Cash/Warranty)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 6: Admin Receipts hub — three tabs

Mirror the cashier tabs in admin under `/admin/receipts`, reusing existing admin money-receipts list/view/reprint for Cash, a new admin warranty list+reprint for Warranty, and the cashier sale view/print endpoints (available to all roles) for Sales.

**Files:**
- Create: `internal/web/admin_receipts.go` (hub + tab fragments + admin warranty reprint)
- Create: `templates/pages/admin/receipts.templ` (hub shell + Sales/Warranty fragments; Cash reuses existing money-receipts table partial via a thin fragment)
- Modify: `internal/web/web.go` (routes)
- Modify: `templates/layouts/admin.templ` (Money nav: point "Cash Receipts" at `/admin/receipts`)

**Interfaces:**
- Consumes: `a.s.cashflowReceipts.List`, `a.s.warranty.ListClaims/GetClaim/GetUnit`, `a.s.sales.List`, `a.s.buildWarrantySlip` (Task 4), `resolveReceiptRange`, `shared.RangeForm`, `reports.ResolveRange`.
- Produces: `(a *adminUI) Receipts`, `ReceiptsSales`, `ReceiptsCash`, `ReceiptsWarranty`, `WarrantyReprint` (admin); `adminpages.ReceiptsHub` + fragment components.

- [ ] **Step 1: Add admin hub + fragment handlers**

`internal/web/admin_receipts.go`:

```go
package web

import (
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/sales"
	"karots-pos/internal/features/warranty"
	"karots-pos/internal/middleware"
	"karots-pos/internal/printing"
	"karots-pos/internal/response"
	adminpages "karots-pos/templates/pages/admin"
)

// Receipts renders the admin Receipts hub shell (Sales tab inline as default).
func (a *adminUI) Receipts(c echo.Context) error {
	ctx := c.Request().Context()
	sd, err := a.salesReceiptData(c)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.ReceiptsHub(adminpages.ReceiptsHubData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Sales:    sd,
	}))
}

func (a *adminUI) salesReceiptData(c echo.Context) (adminpages.RcSalesData, error) {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := resolveReceiptRange(c)
	if err != nil {
		return adminpages.RcSalesData{}, err
	}
	q := strings.TrimSpace(c.QueryParam("q"))
	rows, err := a.s.sales.List(ctx, sales.ListFilter{Query: q, From: from, To: to, Limit: 100})
	if err != nil {
		return adminpages.RcSalesData{}, err
	}
	return adminpages.RcSalesData{Symbol: a.symbol(ctx), Rows: rows, Query: q, Preset: c.QueryParam("preset"), From: fromStr, To: toStr}, nil
}

func (a *adminUI) ReceiptsSales(c echo.Context) error {
	d, err := a.salesReceiptData(c)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.RcSalesTab(d))
}

func (a *adminUI) ReceiptsCash(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := resolveReceiptRange(c)
	if err != nil {
		return err
	}
	q := strings.TrimSpace(c.QueryParam("q"))
	kind := strings.TrimSpace(c.QueryParam("kind"))
	rows, err := a.s.cashflowReceipts.List(ctx, cashflow.ReceiptFilter{Query: q, Kind: kind, From: from, To: to})
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.RcCashTab(adminpages.RcCashData{
		Symbol: a.symbol(ctx), Rows: rows, Query: q, Kind: kind, Preset: c.QueryParam("preset"), From: fromStr, To: toStr,
	}))
}

func (a *adminUI) ReceiptsWarranty(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := resolveReceiptRange(c)
	if err != nil {
		return err
	}
	q := strings.TrimSpace(c.QueryParam("q"))
	rows, err := a.s.warranty.ListClaims(ctx, warranty.ClaimFilter{Search: q, From: from, To: to})
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.RcWarrantyTab(adminpages.RcWarrantyData{
		Rows: rows, Query: q, Preset: c.QueryParam("preset"), From: fromStr, To: toStr,
	}))
}

// WarrantyReprint (admin) re-sends a warranty replacement slip.
func (a *adminUI) WarrantyReprint(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("claimId"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	cl, err := a.s.warranty.GetClaim(ctx, id)
	if err != nil {
		return err
	}
	if cl.ReplacementUnitID == nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("No replacement slip for this claim", "error"))
		return c.NoContent(200)
	}
	newUnit, err := a.s.warranty.GetUnit(ctx, *cl.ReplacementUnitID)
	if err != nil {
		return err
	}
	cfg, err := a.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.ReceiptPrinter) == "" {
		c.Response().Header().Set("HX-Trigger", response.Toast("No receipt printer configured", "error"))
		return c.NoContent(200)
	}
	if err := printing.Raw(ctx, cfg.ReceiptPrinter, a.s.buildWarrantySlip(ctx, cfg, cl.OldSerial, newUnit)); err != nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("Print failed: "+err.Error(), "error"))
		return c.NoContent(200)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Warranty slip sent to printer", "success"))
	return c.NoContent(200)
}
```

(Confirm `a.symbol(ctx)` exists — it is used by `MoneyReceipts`; if the method is named differently, match it.)

- [ ] **Step 2: Create the admin hub template + fragments**

`templates/pages/admin/receipts.templ`:

```templ
package adminpages

import (
	"strconv"

	"karots-pos/internal/datetime"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/sales"
	"karots-pos/internal/features/warranty"
	"karots-pos/internal/money"
	"karots-pos/templates/layouts"
	"karots-pos/templates/shared"
)

type RcSalesData struct {
	Symbol string
	Rows   []sales.Sale
	Query  string
	Preset string
	From   string
	To     string
}
type RcCashData struct {
	Symbol string
	Rows   []cashflow.Receipt
	Query  string
	Kind   string
	Preset string
	From   string
	To     string
}
type RcWarrantyData struct {
	Rows   []warranty.Claim
	Query  string
	Preset string
	From   string
	To     string
}
type ReceiptsHubData struct {
	UserName string
	Symbol   string
	Sales    RcSalesData
}

templ ReceiptsHub(d ReceiptsHubData) {
	@layouts.Admin("Receipts", d.UserName, "money") {
		<div class="p-6" x-data="{ tab: 'sales' }">
			<h1 class="text-2xl font-bold mb-4">Receipts</h1>
			<div class="flex gap-1 mb-4 border-b">
				@rcTab("sales", "Sales")
				@rcTab("cash", "Cash")
				@rcTab("warranty", "Warranty")
			</div>
			<div x-show="tab === 'sales'">
				@RcSalesTab(d.Sales)
			</div>
			<div x-show="tab === 'cash'" hx-get="/admin/receipts/cash" hx-trigger="load once"></div>
			<div x-show="tab === 'warranty'" hx-get="/admin/receipts/warranty" hx-trigger="load once"></div>
		</div>
	}
}

templ rcTab(key, label string) {
	<button type="button" x-on:click={ "tab = '" + key + "'" }
		class="px-4 py-2 text-sm font-medium border-b-2 -mb-px"
		x-bind:class={ "tab === '" + key + "' ? 'border-indigo-600 text-indigo-600' : 'border-transparent text-slate-500'" }>
		{ label }
	</button>
}

templ RcSalesTab(d RcSalesData) {
	<div>
		@shared.RangeForm("/admin/receipts/sales", d.Preset, d.From, d.To) {
			<input type="search" name="q" value={ d.Query } placeholder="Receipt no / customer…" class="border rounded-lg px-3 py-1.5"/>
		}
		<div class="bg-white rounded-2xl shadow-sm overflow-hidden">
			<table class="w-full text-sm">
				<thead class="text-left text-slate-500 border-b bg-slate-50">
					<tr><th class="px-4 py-2">Receipt</th><th class="px-4 py-2">Date</th><th class="px-4 py-2">Type</th><th class="px-4 py-2 text-right">Total</th><th class="px-4 py-2 text-right">Print</th></tr>
				</thead>
				<tbody>
					for _, s := range d.Rows {
						<tr class="border-b last:border-0">
							<td class="px-4 py-2 font-medium">{ s.ReceiptNo }</td>
							<td class="px-4 py-2 text-slate-500">{ datetime.DateTime(s.CreatedAt) }</td>
							<td class="px-4 py-2 capitalize">{ s.SaleType }</td>
							<td class="px-4 py-2 text-right">{ money.Format(d.Symbol, s.Total) }</td>
							<td class="px-4 py-2 text-right">
								<button type="button" hx-post={ "/cashier/print/" + strconv.FormatInt(s.ID, 10) } hx-swap="none" class="px-3 py-1 rounded-lg bg-indigo-600 text-white text-xs font-medium">🧾 Reprint</button>
								<a href={ templ.SafeURL("/cashier/receipt/" + strconv.FormatInt(s.ID, 10)) } target="_blank" class="ml-2 px-3 py-1 rounded-lg border text-slate-600 text-xs font-medium">View</a>
							</td>
						</tr>
					}
					if len(d.Rows) == 0 {
						<tr><td colspan="5" class="px-4 py-8 text-center text-slate-400">No matching sales.</td></tr>
					}
				</tbody>
			</table>
		</div>
	</div>
}

templ RcCashTab(d RcCashData) {
	<div>
		@shared.RangeForm("/admin/receipts/cash", d.Preset, d.From, d.To) {
			<input type="search" name="q" value={ d.Query } placeholder="No / party / location…" class="border rounded-lg px-3 py-1.5"/>
			<select name="kind" class="border rounded-lg px-3 py-1.5">
				@rcKindOption("", "All kinds", d.Kind)
				@rcKindOption("transfer", "Transfer", d.Kind)
				@rcKindOption("expense", "Expense", d.Kind)
				@rcKindOption("supplier_payment", "Supplier payment", d.Kind)
				@rcKindOption("customer_payment", "Customer payment", d.Kind)
				@rcKindOption("refund", "Refund", d.Kind)
				@rcKindOption("bank_charge", "Bank charge", d.Kind)
			</select>
		}
		<div class="bg-white rounded-2xl shadow-sm overflow-hidden">
			<table class="w-full text-sm">
				<thead class="text-left text-slate-500 border-b bg-slate-50">
					<tr><th class="px-4 py-2">Receipt</th><th class="px-4 py-2">Date</th><th class="px-4 py-2">From → To</th><th class="px-4 py-2">Party</th><th class="px-4 py-2 text-right">Amount</th><th class="px-4 py-2 text-right">Print</th></tr>
				</thead>
				<tbody>
					for _, r := range d.Rows {
						<tr class="border-b last:border-0">
							<td class="px-4 py-2 font-medium">{ r.ReceiptNo }</td>
							<td class="px-4 py-2 text-slate-500">{ datetime.DateTime(r.CreatedAt) }</td>
							<td class="px-4 py-2">{ r.FromLabel } → { r.ToLabel }</td>
							<td class="px-4 py-2">{ r.Party }</td>
							<td class="px-4 py-2 text-right">{ money.Format(d.Symbol, r.Amount) }</td>
							<td class="px-4 py-2 text-right">
								<button type="button" hx-post={ "/admin/money-receipts/" + strconv.FormatInt(r.ID, 10) + "/print" } hx-swap="none" class="px-3 py-1 rounded-lg bg-indigo-600 text-white text-xs font-medium">🧾 Reprint</button>
								<a href={ templ.SafeURL("/admin/money-receipts/" + strconv.FormatInt(r.ID, 10)) } target="_blank" class="ml-2 px-3 py-1 rounded-lg border text-slate-600 text-xs font-medium">View</a>
							</td>
						</tr>
					}
					if len(d.Rows) == 0 {
						<tr><td colspan="6" class="px-4 py-8 text-center text-slate-400">No matching cash receipts.</td></tr>
					}
				</tbody>
			</table>
		</div>
	</div>
}

templ rcKindOption(val, label, active string) {
	if val == active {
		<option value={ val } selected>{ label }</option>
	} else {
		<option value={ val }>{ label }</option>
	}
}

templ RcWarrantyTab(d RcWarrantyData) {
	<div>
		@shared.RangeForm("/admin/receipts/warranty", d.Preset, d.From, d.To) {
			<input type="search" name="q" value={ d.Query } placeholder="Serial / customer…" class="border rounded-lg px-3 py-1.5"/>
		}
		<div class="bg-white rounded-2xl shadow-sm overflow-hidden">
			<table class="w-full text-sm">
				<thead class="text-left text-slate-500 border-b bg-slate-50">
					<tr><th class="px-4 py-2">Date</th><th class="px-4 py-2">Product</th><th class="px-4 py-2">Old → New serial</th><th class="px-4 py-2">Customer</th><th class="px-4 py-2">By</th><th class="px-4 py-2 text-right">Print</th></tr>
				</thead>
				<tbody>
					for _, cl := range d.Rows {
						<tr class="border-b last:border-0">
							<td class="px-4 py-2 text-slate-500">{ datetime.DateTime(cl.CreatedAt) }</td>
							<td class="px-4 py-2">{ cl.ProductName }</td>
							<td class="px-4 py-2">
								{ cl.OldSerial } →
								if cl.ReplacementSerial != nil {
									{ *cl.ReplacementSerial }
								} else {
									—
								}
							</td>
							<td class="px-4 py-2">
								if cl.CustomerName != nil {
									{ *cl.CustomerName }
								} else {
									Walk-in
								}
							</td>
							<td class="px-4 py-2 text-slate-500">{ cl.HandledByName }</td>
							<td class="px-4 py-2 text-right">
								if cl.ReplacementUnitID != nil {
									<button type="button" hx-post={ "/admin/receipts/warranty/" + strconv.FormatInt(cl.ID, 10) + "/print" } hx-swap="none" class="px-3 py-1 rounded-lg bg-indigo-600 text-white text-xs font-medium">🧾 Reprint</button>
								}
							</td>
						</tr>
					}
					if len(d.Rows) == 0 {
						<tr><td colspan="6" class="px-4 py-8 text-center text-slate-400">No warranty replacements.</td></tr>
					}
				</tbody>
			</table>
		</div>
	</div>
}
```

(Confirm `layouts.Admin` signature — match the exact param list used by `MoneyReceiptsPage`'s template, e.g. `@layouts.Admin(title, userName, navKey)`. Adjust the call if it differs.)

- [ ] **Step 3: Wire admin routes**

In `internal/web/web.go` admin group (`ag`), near the existing money-receipts routes add:

```go
	ag.GET("/receipts", admin.Receipts)
	ag.GET("/receipts/sales", admin.ReceiptsSales)
	ag.GET("/receipts/cash", admin.ReceiptsCash)
	ag.GET("/receipts/warranty", admin.ReceiptsWarranty)
	ag.POST("/receipts/warranty/:claimId/print", admin.WarrantyReprint)
```

- [ ] **Step 4: Point the Money nav "Cash Receipts" entry at the hub**

In `templates/layouts/admin.templ`, find the Money-section nav link whose href is `/admin/money-receipts` (label "Cash Receipts") and change its href to `/admin/receipts`. Leave the deep `/admin/money-receipts` routes intact (still reachable for view/reprint).

- [ ] **Step 5: Build**

Run: `make templ && go build ./... && go vet ./...`
Expected: green. Resolve any `layouts.Admin`/`a.symbol` signature mismatches.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/web/admin_receipts.go internal/web/web.go
git add internal/web/admin_receipts.go internal/web/web.go templates/pages/admin/receipts.templ templates/layouts/admin.templ
git commit -m "feat(admin): unified Receipts hub (Sales/Cash/Warranty)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 7: End-to-end verification

Prove every tab + filter + view + reprint works on both panels, with evidence.

**Files:** none (verification only)

- [ ] **Step 1: Restart the server**

Run: `fuser -k 3000/tcp; sleep 1; (set -a && . ./.env && set +a && go run ./cmd/server &) ; sleep 5`
Confirm: log shows `POST server listening on :3000`.

- [ ] **Step 2: Seed at least one warranty replacement if none exist**

Check: `docker exec pos_db psql -U pos_user -d pos_db -c "SELECT count(*) FROM warranty_claims;"`
If 0, create one via the cashier Warranty screen (`/cashier/warranty`) — look up a serialized unit and record a replacement — so the Warranty tab has a row. (CR- receipts already exist from prior cashflow testing.)

- [ ] **Step 3: Drive the cashier Receipts UI (Playwright)**

Log in (`0000000001`/`2273`), open `/cashier/receipts`. Verify:
- Three tabs render: Sales, Cash, Warranty.
- Cash tab lists `CR-` receipts; searching a customer name filters; searching a supplier name filters; the kind dropdown filters; a date preset filters.
- Click **View** on a `CR-` → cashier receipt page shows shop header + amount.
- Click **Reprint** on a `CR-` → success toast (or "No receipt printer configured" toast — both prove the path; printer is unset in dev, so the toast is expected and acceptable).
- Warranty tab lists the replacement; **Reprint** → toast.
- Sales tab still lists sales and reprint/view still work.

- [ ] **Step 4: Drive the admin Receipts hub (Playwright)**

Open `/admin/receipts`. Verify the same three tabs, Cash view links to `/admin/money-receipts/:id`, Cash + Warranty reprint produce toasts, and the existing `/admin/money-receipts` deep page still loads.

- [ ] **Step 5: Confirm filters at the DB level (party covers supplier + customer)**

Run: `docker exec pos_db psql -U pos_user -d pos_db -c "SELECT kind, party FROM money_receipts WHERE party <> '' ORDER BY id DESC LIMIT 10;"`
Confirm both supplier_payment rows (supplier name) and customer_payment/refund rows (customer name) appear — proving one party search box covers both.

- [ ] **Step 6: Final build + gofmt sweep**

Run: `gofmt -l internal/ ; make templ && go build ./... && go vet ./...`
Expected: no files listed by `gofmt -l`; build + vet green.

- [ ] **Step 7: Confirm protected files are still unstaged**

Run: `git status --short`
Expected: only `cmd/server/enabled_plugins.go` and `static/css/tailwind.css` modified/unstaged; everything else committed.

---

## Self-Review

**Spec coverage:**
- Three tabs (Sales/Cash/Warranty) cashier — Tasks 3,4,5. ✓
- Three tabs admin — Task 6. ✓
- Cashier sees all CR- shop-wide + view + reprint — Task 3. ✓
- Warranty listable + reprintable from warranty_claims (no new table) — Tasks 1,4,6. ✓
- Per-tab fragments (no schema/UNION) — Tasks 3–6. ✓
- Unified filter bar: date presets every tab (shared.RangeForm) — Tasks 2,3,4,5,6; party search covers customer+supplier (verified, no code change) — Task 7 Step 5; kind filter on Cash — Tasks 3,6. ✓
- Reprint reuse (buildReceiptSlip, buildWarrantySlip via shared receiptImgOptions) — Tasks 3,4. ✓
- Existing deep pages unbroken — Task 6 Step 4 keeps `/admin/money-receipts`. ✓
- Error handling: print failures non-fatal toasts; invalid id → 400/404 — Tasks 3,4,6. ✓

**Placeholder scan:** No TBD/TODO. Each code step shows full code. Conditional checks ("confirm signature", "remove if unused") name the exact grep + fallback. ✓

**Type consistency:**
- `warranty.ClaimFilter{Search,From,To,Limit}` defined Task 1, used Tasks 4,6. ✓
- `Claim.OldSerial/ProductName/CustomerName/ReplacementSerial` defined Task 1, used Tasks 4,6 templ. ✓
- `cashflow.ReceiptFilter{Query,Kind,From,To,Limit}` (existing) used Tasks 3,6. ✓
- `shared.RangeForm(action,preset,from,to)` defined Task 2, used Tasks 3,4,5,6. ✓
- `(s *Server) receiptImgOptions` defined Task 3, used Task 4. ✓
- `(s *Server) buildWarrantySlip` defined Task 4, used Tasks 4,6. ✓
- `warranty.Service.GetUnit` defined Task 4 Step 1, used Tasks 4,6. ✓
- Cashier `ReceiptsData` reshaped + `ReceiptsPageData` added Task 5 — Task 5 Step 4 greps for other consumers. ✓

**Open risk flagged inline:** `sales.ListFilter` date field names (Task 5 Step 1) and `layouts.Admin`/`a.symbol` signatures (Task 6) are verify-then-match steps, not assumptions.
