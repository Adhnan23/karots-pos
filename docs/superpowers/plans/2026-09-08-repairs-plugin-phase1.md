# Repairs Plugin — Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A self-contained `repairs` plugin that tracks a repair job from drop-off to pickup and settles the money as a normal core sale (parts + charges), with optional deposits, warranty, a repair receipt, a Repairs report, admin pages and a cashier menu.

**Architecture:** A compile-time plugin (`plugins/repairs/`) attaching ONLY through existing plugin hooks — no core backend change, inert when not built. Money settles by calling `Core.Sales.Create` (parts as product lines, each charge as a hidden `is_service` "Repair Service" product line via `PriceOverride`); deposits are `Core.CashRegister.PayIn` cash tracked on the job and applied at collection as a non-cash `wallet` tender so the drawer reconciles. Customers are picked/registered from the plugin's own front-end via the existing `/api/customers` endpoints.

**Tech Stack:** Go, sqlx + goose migrations, templ + HTMX + Alpine + Tailwind v3, `github.com/shopspring/decimal`. Plugin framework in `internal/plugin`.

**Spec:** `docs/superpowers/specs/2026-09-08-repairs-plugin-design.md`

## Global Constraints

- **No core backend behaviour change in Phase 1.** Only existing hooks are used: `AddAdminNav`, `AddCashierMenuRoot`, `AddReceiptTab`, `AddReportCard`, `AddActivityContributor`, `AddDashboardCard`, `AddPaletteEntry`. (The `ScanResolver` hook is Phase 2.)
- **Inert without the plugin.** A build with `plugins/repairs` not imported must behave exactly as today; existing tests stay green.
- Plugin package `repairs`, migration prefix `repairs`, `plugin.json` key `repairs`.
- Money uses `decimal` throughout; never float. All amounts round to 2 dp for money, quantities to their own precision.
- `Core.Sales.Create(ctx, in sales.CreateInput, cashierID int64) (*sales.Detail, error)`.
- `Core.CashRegister.PayIn(ctx, userID int64, in cashregister.MovementInput) (*Summary, error)` and `.Withdraw(...)` same shape. `MovementInput{Amount string, Reason string}`.
- Sale payment methods enum: `cash card online wallet credit`; only `cash` touches the drawer; `wallet` settles without a drawer move (verified in `internal/features/sales/tender.go`).
- Follow the existing plugin patterns exactly (see `plugins/documents` and `plugins/clearance`): `init()`→`plugin.Register`, `Name()`, `Migrations() (fs.FS, "repairs")`, `Setup(reg)`, `migrations/embed.go` with `//go:embed *.sql`, handlers use `response.RenderPage` / `response.RenderFragment`.

---

## File Structure

- `plugins/repairs/repairs.go` — plugin registration + `Setup` (routes + hook registration + ensure config/labour product).
- `plugins/repairs/migrations/embed.go` — `//go:embed *.sql`.
- `plugins/repairs/migrations/0001_repairs.sql` — tables.
- `plugins/repairs/store.go` — types + all DB access (jobs, parts, charges, payments, config, distinct lists, activity rows).
- `plugins/repairs/calc.go` — pure money logic: totals + build the collection `sales.CreateInput`. Unit-tested.
- `plugins/repairs/calc_test.go` — money math tests.
- `plugins/repairs/collect_test.go` — DB rollback test of the full collect path.
- `plugins/repairs/admin.go` — admin handlers (list, new, detail, edit, add/remove part+charge, deposit, collect, cancel, report, receipts).
- `plugins/repairs/cashier.go` — cashier-menu handlers (menu root, apply/detail fragment, add part/charge, take advance, collect via cart, record, receipts).
- `plugins/repairs/pages.templ` — admin pages/fragments + the repair receipt component + dashboard card.
- `plugins/repairs/pos.templ` — cashier-menu fragments + the collect Alpine glue (mirrors `plugins/documents/pos.templ`).
- `cmd/server/enabled_plugins.go` — add the blank import.

---

## Task 1: Inert scaffold — plugin registers, migrates, builds, changes nothing

**Files:**
- Create: `plugins/repairs/repairs.go`, `plugins/repairs/migrations/embed.go`, `plugins/repairs/migrations/0001_repairs.sql`, `plugins/repairs/store.go`
- Modify: `cmd/server/enabled_plugins.go`

**Interfaces:**
- Produces: `repairs.Plugin` (implements `plugin.Plugin`); `repairs.Store` with `NewStore(db *sqlx.DB) *Store`.

- [ ] **Step 1: Write the migration** `plugins/repairs/migrations/0001_repairs.sql`

```sql
-- +goose Up
CREATE TABLE repair_jobs (
    id             BIGSERIAL PRIMARY KEY,
    ticket_no      TEXT NOT NULL,
    ticket_code    TEXT NOT NULL UNIQUE,
    customer_id    BIGINT,
    customer_name  TEXT NOT NULL DEFAULT '',
    customer_phone TEXT NOT NULL DEFAULT '',
    repair_type    TEXT NOT NULL DEFAULT '',
    device_model   TEXT NOT NULL DEFAULT '',
    fault          TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'received',
    promised_date  DATE,
    urgent         BOOLEAN NOT NULL DEFAULT false,
    warranty_days  INT NOT NULL DEFAULT 0,
    warranty_until DATE,
    sale_id        BIGINT,
    rework_of      BIGINT,
    notes          TEXT NOT NULL DEFAULT '',
    created_by     BIGINT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    ready_at       TIMESTAMPTZ,
    collected_at   TIMESTAMPTZ,
    cancelled_at   TIMESTAMPTZ
);
CREATE INDEX repair_jobs_status_idx ON repair_jobs (status);

CREATE TABLE repair_parts (
    id             BIGSERIAL PRIMARY KEY,
    job_id         BIGINT NOT NULL REFERENCES repair_jobs(id) ON DELETE CASCADE,
    product_id     BIGINT NOT NULL,
    qty            NUMERIC NOT NULL DEFAULT 1,
    unit_charge    NUMERIC NOT NULL DEFAULT 0,
    discount       NUMERIC NOT NULL DEFAULT 0,
    discount_type  TEXT NOT NULL DEFAULT 'fixed',
    discount_value NUMERIC NOT NULL DEFAULT 0
);

CREATE TABLE repair_charges (
    id     BIGSERIAL PRIMARY KEY,
    job_id BIGINT NOT NULL REFERENCES repair_jobs(id) ON DELETE CASCADE,
    label  TEXT NOT NULL,
    amount NUMERIC NOT NULL DEFAULT 0
);

CREATE TABLE repair_payments (
    id         BIGSERIAL PRIMARY KEY,
    job_id     BIGINT NOT NULL REFERENCES repair_jobs(id) ON DELETE CASCADE,
    amount     NUMERIC NOT NULL,
    kind       TEXT NOT NULL DEFAULT 'deposit',
    user_id    BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Single-row config: the hidden labour/service product + the default warranty.
CREATE TABLE repair_config (
    id                 INT PRIMARY KEY DEFAULT 1,
    labour_product_id  BIGINT NOT NULL DEFAULT 0,
    default_warranty_days INT NOT NULL DEFAULT 0,
    ticket_seq         BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT repair_config_singleton CHECK (id = 1)
);
INSERT INTO repair_config (id) VALUES (1);

-- +goose Down
DROP TABLE repair_payments;
DROP TABLE repair_charges;
DROP TABLE repair_parts;
DROP TABLE repair_config;
DROP TABLE repair_jobs;
```

- [ ] **Step 2: Write `migrations/embed.go`**

```go
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
```

- [ ] **Step 3: Write a minimal `store.go`** (just enough to compile; expanded in Task 2)

```go
package repairs

import "github.com/jmoiron/sqlx"

type Store struct{ db *sqlx.DB }

func NewStore(db *sqlx.DB) *Store { return &Store{db: db} }
```

- [ ] **Step 4: Write `repairs.go`** with registration + an empty Setup

```go
// Package repairs tracks device repair jobs from drop-off to pickup and settles
// the money as a normal core sale. Core never imports it; it attaches only
// through generic plugin hooks and is inert when not built.
package repairs

import (
	"io/fs"

	"karots-pos/internal/plugin"
	"karots-pos/plugins/repairs/migrations"
)

func init() { plugin.Register(&Plugin{}) }

type Plugin struct {
	core  plugin.Core
	store *Store
}

func (p *Plugin) Name() string               { return "Repairs" }
func (p *Plugin) Migrations() (fs.FS, string) { return migrations.FS, "repairs" }

func (p *Plugin) Setup(reg *plugin.Registry) {
	p.core = reg.Core
	p.store = NewStore(reg.Core.DB)
	// routes + hooks added in later tasks
}
```

- [ ] **Step 5: Enable the plugin** — add to `cmd/server/enabled_plugins.go` in the import block, keeping alphabetical order (after `productplus`, before `recharge`):

```go
	_ "karots-pos/plugins/repairs"
```

- [ ] **Step 6: Build and verify migrations apply, core unaffected**

Run: `go build ./... && templ generate ./plugins/repairs/ 2>/dev/null; go build ./...`
Then run the server once against the dev DB to apply the migration:
Run: `bash -c 'set -a && . ./.env && set +a && go run ./cmd/server -h >/dev/null 2>&1'` is not enough — instead start briefly and confirm the log line `plugin "Repairs": migrations applied`. 
Expected: build OK; on startup, migration `0001_repairs` applies with no error; core routes unchanged.

- [ ] **Step 7: Verify the existing suite is green (nothing broken)**

Run: `go test ./... 2>&1 | grep -v '^ok\|no test files'`
Expected: no failures.

- [ ] **Step 8: Commit**

```bash
git add plugins/repairs cmd/server/enabled_plugins.go
git commit -m "feat(repairs): inert plugin scaffold + schema (phase 1)"
```

---

## Task 2: Store — types, job/part/charge/payment access, config, distinct lists

**Files:**
- Modify: `plugins/repairs/store.go`

**Interfaces:**
- Produces:
  - Types `Job`, `Part`, `Charge`, `Payment`, `Detail{Job Job; Parts []Part; Charges []Charge; Payments []Payment}`.
  - `(*Store).EnsureConfig(ctx, ensureLabour func() (int64, error)) (labourProductID int64, defaultWarrantyDays int, err error)`
  - `(*Store).NextTicket(ctx) (no string, code string, err error)` → `R-0001` / `RPR000001`
  - `(*Store).CreateJob(ctx, in JobInput) (int64, error)` where `JobInput{CustomerID *int64; CustomerName, CustomerPhone, RepairType, DeviceModel, Fault, Notes string; PromisedDate *time.Time; WarrantyDays int; ReworkOf *int64; CreatedBy int64}`
  - `(*Store).GetJob(ctx, id int64) (*Detail, error)`
  - `(*Store).JobByTicketCode(ctx, code string) (*Job, error)`
  - `(*Store).UpdateJobFields(ctx, id int64, in JobInput) error`
  - `(*Store).SetStatus(ctx, id int64, status string, stamp *time.Time) error`
  - `(*Store).AddPart(ctx, jobID int64, p PartInput) error` / `RemovePart(ctx, id int64) error`; `PartInput{ProductID int64; Qty, UnitCharge, DiscountValue decimal.Decimal; DiscountType string}`
  - `(*Store).AddCharge(ctx, jobID int64, label string, amount decimal.Decimal) error` / `RemoveCharge(ctx, id int64) error`
  - `(*Store).AddPayment(ctx, jobID int64, amount decimal.Decimal, kind string, userID int64) error`
  - `(*Store).MarkCollected(ctx, id, saleID int64, warrantyUntil *time.Time) error`
  - `(*Store).ListByStatuses(ctx, statuses []string) ([]Job, error)` and `ListForReport(ctx, from, to time.Time) ([]Detail, error)`
  - `(*Store).DistinctTypes(ctx) ([]string, error)` / `DistinctModels(ctx) ([]string, error)`

- [ ] **Step 1: Write the failing test** `plugins/repairs/store_test.go`

```go
package repairs

import (
	"context"
	"os"
	"testing"

	appdb "karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

func testDB(t *testing.T) *sqlx.DB {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	db, err := appdb.Connect(url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return db
}

func TestCreateJobAndTicketSequence(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	tx, _ := db.Beginx()
	defer tx.Rollback() //nolint:errcheck
	s := &Store{db: nil} // store methods must accept a Queryer; see note below
	_ = s
	_ = decimal.Zero
	_ = context.Background()
}
```

Note: the existing plugins take `*sqlx.DB`, but for a rollback test the store methods must run on a `*sqlx.Tx`. Mirror the core pattern: give `Store` a `db.Queryer` field (see `internal/db`), and add `newStoreQ(q db.Queryer) *Store`. Rewrite this test to build `newStoreQ(tx)` and assert `NextTicket` returns `R-0001`/`RPR000001` on first call and increments on the second.

- [ ] **Step 2: Run the test to verify it fails**

Run: `bash -c 'set -a && . ./.env && set +a && go test ./plugins/repairs/ -run TestCreateJobAndTicketSequence -v'`
Expected: FAIL (compile error / undefined methods).

- [ ] **Step 3: Implement `store.go`** — change `Store` to hold a `db.Queryer`; add `newStoreQ`; define the types (columns mirror the migration exactly, `db:"…"` tags); implement each method above.

Ticket sequence (atomic): 
```go
func (s *Store) NextTicket(ctx context.Context) (string, string, error) {
	var seq int64
	err := s.q.GetContext(ctx, &seq,
		`UPDATE repair_config SET ticket_seq = ticket_seq + 1 WHERE id = 1 RETURNING ticket_seq`)
	if err != nil {
		return "", "", err
	}
	return fmt.Sprintf("R-%04d", seq), fmt.Sprintf("RPR%06d", seq), nil
}
```
Distinct lists: `SELECT DISTINCT repair_type FROM repair_jobs WHERE repair_type <> '' ORDER BY 1` (and the same for `device_model`). `GetJob` loads the job then parts, charges, payments.

- [ ] **Step 4: Run the test to verify it passes**

Run: `bash -c 'set -a && . ./.env && set +a && go test ./plugins/repairs/ -run TestCreateJobAndTicketSequence -v'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugins/repairs/store.go plugins/repairs/store_test.go
git commit -m "feat(repairs): store — jobs, parts, charges, payments, config"
```

---

## Task 3: Money logic — totals + build the collection sale (the money path)

**Files:**
- Create: `plugins/repairs/calc.go`, `plugins/repairs/calc_test.go`

**Interfaces:**
- Consumes: `Detail`, `Part`, `Charge`, `Payment` from Task 2.
- Produces:
  - `resolvePartDiscount(dtype string, value, gross, qty decimal.Decimal) decimal.Decimal` (fixed = per-unit × qty; percent = off the gross; clamped to [0, gross]).
  - `JobTotals(d *Detail) (total, depositPaid, balance decimal.Decimal)` — `total = Σ charge.amount + Σ(part.qty×part.unit_charge − partDiscount)`; `depositPaid = Σ(deposit) − Σ(refund)`; `balance = max(0, total − depositPaid)`.
  - `BuildCollectionSale(d *Detail, labourProductID int64, customerID *int64, balanceMethod string) sales.CreateInput` — one `ItemInput` per part `{ProductID, Quantity, Discount, DiscountType}`; one `ItemInput` per charge `{ProductID: labourProductID, Quantity:"1", PriceOverride: amount}`; payments = `[{wallet, depositPaid}]` (only if > 0) + `[{balanceMethod, balance}]` (only if > 0); `SaleType:"retail"`.

- [ ] **Step 1: Write the failing test** `plugins/repairs/calc_test.go`

```go
package repairs

import (
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestJobTotals(t *testing.T) {
	det := &Detail{
		Parts: []Part{
			{Qty: d("2"), UnitCharge: d("500"), DiscountType: "percent", DiscountValue: d("10")}, // 1000 - 100 = 900
			{Qty: d("1"), UnitCharge: d("300"), DiscountType: "fixed", DiscountValue: d("0")},     // 300
		},
		Charges:  []Charge{{Label: "Labour", Amount: d("700")}},
		Payments: []Payment{{Amount: d("400"), Kind: "deposit"}},
	}
	total, dep, bal := JobTotals(det)
	if !total.Equal(d("1900")) {
		t.Fatalf("total = %s, want 1900", total)
	}
	if !dep.Equal(d("400")) || !bal.Equal(d("1500")) {
		t.Fatalf("dep/bal = %s/%s, want 400/1500", dep, bal)
	}
}

func TestBuildCollectionSaleTenders(t *testing.T) {
	det := &Detail{
		Parts:    []Part{{ProductID: 5, Qty: d("1"), UnitCharge: d("1000")}},
		Charges:  []Charge{{Amount: d("500"), Label: "Labour"}},
		Payments: []Payment{{Amount: d("400"), Kind: "deposit"}},
	}
	in := BuildCollectionSale(det, 99, nil, "cash")
	// total 1500, deposit 400 -> wallet 400 + cash 1100
	if len(in.Payments) != 2 {
		t.Fatalf("want 2 tenders, got %d", len(in.Payments))
	}
	var wallet, cash string
	for _, p := range in.Payments {
		if p.Method == "wallet" {
			wallet = p.Amount
		}
		if p.Method == "cash" {
			cash = p.Amount
		}
	}
	if wallet != "400" || cash != "1100" {
		t.Fatalf("tenders wallet/cash = %s/%s, want 400/1100", wallet, cash)
	}
	// one part line + one charge line (charge uses the labour product id)
	if len(in.Items) != 2 {
		t.Fatalf("want 2 items, got %d", len(in.Items))
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./plugins/repairs/ -run 'TestJobTotals|TestBuildCollectionSale' -v`
Expected: FAIL (undefined `JobTotals` / `BuildCollectionSale`).

- [ ] **Step 3: Implement `calc.go`** with `resolvePartDiscount`, `JobTotals`, `BuildCollectionSale` exactly to the interface above. Money rounds to 2 dp. Guard: if `balance` is 0, omit the cash tender; if `depositPaid` is 0, omit the wallet tender.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./plugins/repairs/ -run 'TestJobTotals|TestBuildCollectionSale' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugins/repairs/calc.go plugins/repairs/calc_test.go
git commit -m "feat(repairs): money — totals + wallet/cash collection sale"
```

---

## Task 4: Setup wiring — labour product, routes, and all Phase-1 hooks

**Files:**
- Modify: `plugins/repairs/repairs.go`

**Interfaces:**
- Consumes: `Store` (Task 2), the admin/cashier handler types (Tasks 5–9, referenced by method name here; those tasks implement them).
- Produces: `(*Plugin).ensureLabourProduct(ctx) (int64, error)` — `FindByName(ctx, "Repair Service")`, else `Products.Create(products.CreateInput{Name:"Repair Service", IsService:true, SellingPrice:"0", CostPrice:"0", ...})`; store id in `repair_config.labour_product_id`.

- [ ] **Step 1: Implement `ensureLabourProduct` + config load in `Setup`**

Call `p.store.EnsureConfig(ctx, p.ensureLabourProduct)` at Setup; cache `labourProductID` and `defaultWarrantyDays` on `p`.

- [ ] **Step 2: Register routes + hooks** (mirror `plugins/documents` Setup):

```go
a := &adminUI{p: p}
reg.Admin().GET("/repairs", a.List)
reg.Admin().GET("/repairs/new", a.NewForm)
reg.Admin().POST("/repairs", a.Create)
reg.Admin().GET("/repairs/:id", a.Detail)
reg.Admin().POST("/repairs/:id", a.Update)
reg.Admin().POST("/repairs/:id/part", a.AddPart)
reg.Admin().POST("/repairs/part/:pid/delete", a.RemovePart)
reg.Admin().POST("/repairs/:id/charge", a.AddCharge)
reg.Admin().POST("/repairs/charge/:cid/delete", a.RemoveCharge)
reg.Admin().POST("/repairs/:id/deposit", a.TakeDeposit)
reg.Admin().POST("/repairs/:id/status", a.SetStatus)
reg.Admin().POST("/repairs/:id/collect", a.Collect)
reg.Admin().POST("/repairs/:id/cancel", a.Cancel)
reg.Admin().GET("/repairs/report", a.Report)
reg.Admin().GET("/repairs/receipts", a.Receipts)
reg.Admin().GET("/repairs/:id/receipt", a.RepairReceipt)
reg.Admin().GET("/repairs/suggest", a.Suggest) // distinct types/models for datalists

ch := &cashierUI{p: p}
reg.Cashier().GET("/repairs/menu", ch.MenuRoot)
reg.Cashier().GET("/repairs/apply", ch.ApplyForm)
reg.Cashier().POST("/repairs", ch.Create)
reg.Cashier().GET("/repairs/:id", ch.Detail)
reg.Cashier().POST("/repairs/:id/part", ch.AddPart)
reg.Cashier().POST("/repairs/:id/charge", ch.AddCharge)
reg.Cashier().POST("/repairs/:id/deposit", ch.TakeDeposit)
reg.Cashier().POST("/repairs/record", ch.Record)
reg.Cashier().GET("/repairs/receipts", ch.Receipts)
reg.Cashier().GET("/repairs/:id/receipt", ch.RepairReceipt)

reg.AddAdminNav(plugin.AdminNavEntry{
	SectionLabel: "Repairs", Icon: "🔧",
	Href: "/admin/repairs", Label: "Repairs", Key: "repairs",
	Desc: "Track repair jobs, parts, warranty",
})
reg.AddCashierMenuRoot(plugin.CashierMenuRoot{
	Key: "repairs", Emoji: "🔧", Label: "Repairs", ChildrenURL: "/cashier/repairs/menu",
})
reg.AddReceiptTab(plugin.ReceiptTab{
	Key: "repairs", Label: "Repairs",
	CashierHref: "/cashier/repairs/receipts", AdminHref: "/admin/repairs/receipts",
})
reg.AddReportCard(plugin.ReportCard{
	Href: "/admin/repairs/report", Label: "🔧 Repairs", Desc: "Repair jobs, revenue & warranty",
})
reg.AddActivityContributor(plugin.ActivityContributor{Source: "repairs", List: p.store.ActivityRows})
reg.AddPaletteEntry(plugin.PaletteEntry{Href: "/admin/repairs", Label: "Repairs", Group: "Repairs"})
reg.AddDashboardCard(plugin.DashboardCard{Component: pages.ReadyCard(...)}) // wired in Task 9
```

- [ ] **Step 3: Add stub handler types** so it compiles: `type adminUI struct{ p *Plugin }` and `type cashierUI struct{ p *Plugin }` with each referenced method returning `c.NoContent(http.StatusOK)` for now (real bodies in Tasks 5–9). Add `(*Store).ActivityRows` returning `(nil, nil)` for now.

- [ ] **Step 4: Build + inert check**

Run: `go build ./... && go test ./... 2>&1 | grep -v '^ok\|no test files'`
Expected: builds; no failures. Start the server once and confirm "🔧 Repairs" appears in the admin nav and cashier menu, and existing screens are unchanged.

- [ ] **Step 5: Commit**

```bash
git add plugins/repairs/repairs.go plugins/repairs/store.go
git commit -m "feat(repairs): wire routes, labour product, phase-1 hooks (stub handlers)"
```

---

## Task 5: Admin — job list (queue) + new job + detail page

**Files:**
- Create: `plugins/repairs/pages.templ`
- Modify: `plugins/repairs/admin.go` (create), `plugins/repairs/repairs.go` (imports)

**Interfaces:**
- Consumes: `Store` methods, `JobTotals`.
- Produces: templ `RepairsListPage(d ListData)`, `RepairFormPage(d FormData)`, `RepairDetailPage(d DetailData)`, and fragments used by later tasks.

- [ ] **Step 1: Implement `admin.go` handlers** `List`, `NewForm`, `Create`, `Detail`, `Update`, `SetStatus`, `Suggest`.
  - `List`: `store.ListByStatuses(ctx, []string{"received","in_progress","ready"})` grouped; compute per-job days-to-promised + urgent (overdue OR `urgent` flag). Renders `RepairsListPage`.
  - `NewForm`/`Create`: `Create` binds `JobInput` (customer, type, model, fault, promised date, warranty days default from config), calls `store.NextTicket` + `store.CreateJob`, audits via `p.core.Audit.Record(ctx, uid, audit.ActionCreate, "repair", ticketNo, "opened repair")`, redirects to `Detail`.
  - `Detail`: `store.GetJob`; compute totals; render `RepairDetailPage` (job fields, parts table, charges, payments, deposit box, status buttons, Collect/Cancel).
  - `Suggest`: returns JSON `{types:[…], models:[…]}` from `DistinctTypes`/`DistinctModels` for the datalists.

- [ ] **Step 2: Implement `pages.templ`** for the three pages. Follow `templates/pages/admin/*.templ` conventions and the `@layouts.Admin("Repairs", d.UserName, "repairs")` wrapper. The form's type/model inputs use `<input list="repair-types">` + `<datalist id="repair-types">` populated from `/admin/repairs/suggest`. The customer field uses the existing customer picker pattern (Task 10 wires the JS).

- [ ] **Step 3: Generate templ + build**

Run: `templ generate ./plugins/repairs/ && go build ./...`
Expected: OK.

- [ ] **Step 4: Manual verify**

Start the server; go to Admin → Repairs → New; create a job; confirm it lists with a promised-date countdown and opens on the detail page.

- [ ] **Step 5: Commit**

```bash
git add plugins/repairs/admin.go plugins/repairs/pages.templ plugins/repairs/pages_templ.go plugins/repairs/repairs.go
git commit -m "feat(repairs): admin job list, create, detail"
```

---

## Task 6: Admin — add/remove parts (with discount) and custom charges

**Files:**
- Modify: `plugins/repairs/admin.go`, `plugins/repairs/pages.templ`

**Interfaces:**
- Consumes: `Store.AddPart/RemovePart/AddCharge/RemoveCharge`, `/api/products` search from the front-end.
- Produces: `RepairLinesFragment(d DetailData)` returned by add/remove so HTMX swaps the parts+charges+totals block.

- [ ] **Step 1: Implement handlers** `AddPart` (bind `product_id, qty, unit_charge, discount, discount_type`), `RemovePart`, `AddCharge` (bind `label, amount`), `RemoveCharge`. Each ends by re-rendering `RepairLinesFragment` via `response.RenderFragment`.
- [ ] **Step 2: Implement the fragment + form rows in `pages.templ`** — a parts table (product search input reusing the `poProductSearch`/`/api/products` pattern, qty, unit charge, and a Disc. cell with the Rs/% toggle exactly like the receiving form), a charges list (label + amount), and a live totals box (subtotal, deposit, balance) computed server-side from `JobTotals`.
- [ ] **Step 3: Generate + build**

Run: `templ generate ./plugins/repairs/ && go build ./...`
Expected: OK.

- [ ] **Step 4: Manual verify** — add a part with a 10% discount and a custom "Labour" charge; confirm the totals box updates and `RemovePart` removes the row.
- [ ] **Step 5: Commit**

```bash
git add plugins/repairs/admin.go plugins/repairs/pages.templ plugins/repairs/pages_templ.go
git commit -m "feat(repairs): add/remove parts (with discount) and charges"
```

---

## Task 7: Admin — deposit, collect (rings the sale), cancel

**Files:**
- Modify: `plugins/repairs/admin.go`
- Create: `plugins/repairs/collect_test.go`

**Interfaces:**
- Consumes: `BuildCollectionSale`, `Core.Sales.Create`, `Core.CashRegister.PayIn/Withdraw`, `Store.AddPayment/MarkCollected/SetStatus`.
- Produces: `(*Plugin).collect(ctx, jobID int64, balanceMethod string, userID int64) (*sales.Detail, error)` — loads job, builds the sale, `Core.Sales.Create`, `MarkCollected(sale.ID, warrantyUntil)` where `warrantyUntil = today + warranty_days`, audits.

- [ ] **Step 1: Write the failing DB test** `collect_test.go` (rollback), modelled on `internal/web/receiving_discount_test.go`:

```go
package repairs

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"
)

func TestCollectRingsSaleWithDepositTender(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	tx, _ := db.Beginx()
	defer tx.Rollback() //nolint:errcheck
	ctx := context.Background()
	// seed: a product (part), the labour service product, a job with a part +
	// charge + a 400 deposit; then collect and assert:
	//  - a core sale exists linked on the job (sale_id set, status 'collected')
	//  - the sale's payments are wallet=deposit + cash=balance
	//  - stock for the part decremented by qty
	//  - warranty_until stamped
	_ = ctx
	_ = decimal.Zero
}
```
Fill it in: insert a product with stock, ensure a labour service product, `CreateJob`, `AddPart`, `AddCharge`, `AddPayment(deposit)`, then call the collect logic against `tx` (use a tx-bound Store + a `sales.Service`/`cashregister.Service` built on `tx` the same way `internal/web/receiving_discount_test.go` builds services), and assert the four facts.

- [ ] **Step 2: Run the test to verify it fails**

Run: `bash -c 'set -a && . ./.env && set +a && go test ./plugins/repairs/ -run TestCollectRingsSaleWithDepositTender -v'`
Expected: FAIL.

- [ ] **Step 3: Implement `TakeDeposit`, `collect`/`Collect`, `Cancel`**
  - `TakeDeposit`: parse amount; `p.core.CashRegister.PayIn(ctx, uid, cashregister.MovementInput{Amount: amt, Reason: "Repair "+ticket+" deposit"})`; `store.AddPayment(jobID, amt, "deposit", uid)`; re-render totals.
  - `collect`: `BuildCollectionSale`; `p.core.Sales.Create(ctx, in, uid)`; `store.MarkCollected(jobID, sale.ID, warrantyUntil)` + `SetStatus "collected"`; audit.
  - `Cancel`: for each deposit, `CashRegister.Withdraw` the net deposit + `AddPayment(kind:"refund")`; `SetStatus "cancelled"`.

- [ ] **Step 4: Run the test to verify it passes**

Run: `bash -c 'set -a && . ./.env && set +a && go test ./plugins/repairs/ -run TestCollectRingsSaleWithDepositTender -v'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugins/repairs/admin.go plugins/repairs/collect_test.go
git commit -m "feat(repairs): deposit, collect via core sale (wallet+cash), cancel"
```

---

## Task 8: Cashier menu — apply/add/advance + collect via cart injection + record

**Files:**
- Create: `plugins/repairs/cashier.go`, `plugins/repairs/pos.templ`

**Interfaces:**
- Consumes: the same store + collect building blocks; the till's `pos-add-service` event and post-checkout record convention (see `plugins/documents/pos.templ` + `cashier.go Record`).
- Produces: `MenuRoot` (menu-node JSON `{"nodes":[…]}`), `ApplyForm`/`Detail` fragments, `Record` (post-checkout link).

- [ ] **Step 1: Implement `cashier.go`** — `MenuRoot` returns nodes: an "Apply a repair" detail leaf (`/cashier/repairs/apply`) + one leaf per open job (`/cashier/repairs/:id`). `Create`, `AddPart`, `AddCharge`, `TakeDeposit` mirror the admin handlers but render inline cashier fragments. `Record(saleID, jobID)` links the sale to the job + marks collected + stamps warranty (the cashier collection path completes through the normal till, so parts+charges are injected as cart lines and this runs post-checkout — mirrors `documents.Record`).
- [ ] **Step 2: Implement `pos.templ`** — the apply/detail fragments (Alpine), and the "Collect" button that dispatches `pos-add-service` once per part (`{id: product_id, name, price, qty}`) and once per charge (`{id: labourProductID, name: label, price: amount}`), tags the cart with `repairJob:{job_id}` (like `docJob`), so the post-checkout hook posts `/cashier/repairs/record`. Deposit taken here posts `/cashier/repairs/:id/deposit`.
- [ ] **Step 3: Generate + build**

Run: `templ generate ./plugins/repairs/ && go build ./...`
Expected: OK.

- [ ] **Step 4: Manual verify** — from the till, open 🔧 Repairs → Apply, create a job, take an advance, add a part + labour, Collect → the lines land in the cart, checkout completes a normal sale, and the job flips to collected linked to that sale.
- [ ] **Step 5: Commit**

```bash
git add plugins/repairs/cashier.go plugins/repairs/pos.templ plugins/repairs/pos_templ.go plugins/repairs/repairs.go
git commit -m "feat(repairs): cashier menu — apply, advance, collect via cart"
```

---

## Task 9: Receipts, report, activity, dashboard card

**Files:**
- Modify: `plugins/repairs/admin.go`, `plugins/repairs/cashier.go`, `plugins/repairs/pages.templ`, `plugins/repairs/store.go`

**Interfaces:**
- Produces: `RepairReceipt(d ReceiptData)` templ, `RepairsReportPage(d ReportData)` templ, `ReadyCard(count int)` templ, `(*Store).ActivityRows(ctx, f activity.Filter) ([]activity.Row, error)`.

- [ ] **Step 1: Repair receipt** — `RepairReceipt` prints ticket no, repair type + model, fault, parts (with discount), charge lines, deposit + balance, and "Warranty until YYYY-MM-DD (N days)". Reuse the shared escpos header/title/footer used by other receipts. `Receipts` (admin + cashier) lists repair jobs/sales for the ReceiptTab; `RepairReceipt` handler renders one.
- [ ] **Step 2: Checkout prompt** — in `pos.templ`, when the completed sale carried a `repairJob`, after the record call show a toast/confirm "Also print the repair receipt?" that opens `/cashier/repairs/:id/receipt`.
- [ ] **Step 3: Report** — `Report` handler: `store.ListForReport(ctx, from, to)`; `RepairsReportPage` shows jobs in range with revenue (parts + charges), parts cost (from product cost), profit, outstanding deposits, and warranty jobs. Date range via the core datetime presets pattern.
- [ ] **Step 4: Activity + dashboard** — implement `Store.ActivityRows` (job created/ready/collected/cancelled, excluding the hidden system account) and the `ReadyCard` dashboard component (count of `ready` jobs), and wire the real `DashboardCard` in Setup.
- [ ] **Step 5: Generate + build + test**

Run: `templ generate ./plugins/repairs/ && go build ./... && go test ./plugins/repairs/ 2>&1 | tail`
Expected: OK / PASS.

- [ ] **Step 6: Manual verify** — collect a job, open Receipts → Repairs and reprint; open Reports → Repairs; see the dashboard "ready for pickup" card and an Activity row.
- [ ] **Step 7: Commit**

```bash
git add plugins/repairs/
git commit -m "feat(repairs): repair receipt, receipts tab, report, activity, dashboard"
```

---

## Task 10: Customer pick / register (front-end, existing API)

**Files:**
- Modify: `plugins/repairs/pages.templ`, `plugins/repairs/pos.templ`

**Interfaces:**
- Consumes: existing `GET /api/customers` (search) and `POST /api/customers` (create) — no core change.

- [ ] **Step 1: Add the customer picker** to the admin new/detail form and the cashier apply fragment: an Alpine block that searches `GET /api/customers?search=…`, lets you pick an existing customer (sets `customer_id` + fills name/phone), or fills name/phone for a new walk-in and, on save, `POST /api/customers` (deduped by phone) then uses the returned id. Store `customer_id` + the name/phone snapshot on the job (`Create`/`Update` already accept them).
- [ ] **Step 2: Generate + build**

Run: `templ generate ./plugins/repairs/ && go build ./...`
Expected: OK.

- [ ] **Step 3: Manual verify** — create a job by picking an existing customer; create another by registering a new one; confirm both link `customer_id` and the phone shows on the job.
- [ ] **Step 4: Commit**

```bash
git add plugins/repairs/pages.templ plugins/repairs/pages_templ.go plugins/repairs/pos.templ plugins/repairs/pos_templ.go
git commit -m "feat(repairs): pick or register customer via existing /api/customers"
```

---

## Task 11: Final verification — inert core, full build, formatting

**Files:** none (verification only)

- [ ] **Step 1: gofmt**

Run: `gofmt -l plugins/repairs cmd/server/enabled_plugins.go`
Expected: empty (fix with `gofmt -w` if not).

- [ ] **Step 2: Full build + generate**

Run: `templ generate ./plugins/repairs/ && go build ./...`
Expected: OK.

- [ ] **Step 3: Inert core-only check** — temporarily build with the repairs import commented out and confirm the app is unchanged:

Run: `sed 's#_ "karots-pos/plugins/repairs"#// _ "karots-pos/plugins/repairs"#' -i cmd/server/enabled_plugins.go && go build ./... && go test ./... 2>&1 | grep -v '^ok\|no test files'; git checkout cmd/server/enabled_plugins.go`
Expected: builds and all tests green WITHOUT repairs; then the import is restored.

- [ ] **Step 4: Full suite**

Run: `bash -c 'set -a && . ./.env && set +a && go test ./... 2>&1 | grep -v "^ok\|no test files"'`
Expected: no failures (repairs DB tests run against the dev DB and pass).

- [ ] **Step 5: Commit any formatting/fixups**

```bash
git add -A && git commit -m "chore(repairs): phase 1 verification (inert core, tests green)"
```

---

## Self-Review notes

- **Spec coverage:** job lifecycle (T2,5,7,8), customer pick/register (T10), type+model datalists (T5), parts+discount & custom charges (T6), real-sale settlement + deposit wallet tender (T3,7,8), warranty until + on receipt (T7,9), two receipts + Receipts tab + checkout prompt (T9), cashier menu + admin (T5–8), report + activity + dashboard (T9), no core change / inert (T1,4,11). Phase-2-only items (scan `ScanResolver` hook, urgent-scan countdown, deadline-extension audit, warranty rework) are intentionally NOT in this plan.
- **Type consistency:** store methods take a `db.Queryer` so both `*sqlx.DB` and `*sqlx.Tx` work (needed by the rollback tests); `labourProductID` is the same value produced in T4 and consumed by `BuildCollectionSale` in T3/T7/T8; charge lines always use `PriceOverride`; deposit tender is always `wallet`, balance is the chosen method.
- **Deferred (ponytail):** the cashier collect path relies on the till's existing `pos-add-service` + post-checkout record convention rather than a bespoke sale call, matching documents — one code path per surface.
