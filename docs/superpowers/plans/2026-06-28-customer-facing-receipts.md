# Customer-facing Receipts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the customer a proper detailed slip for debt payments (new, reprintable), returns (enhanced), and warranty claims (enhanced), while the internal money tracking (CR-) stays unchanged.

**Architecture:** The debt receipt keys off the `customer_payments` row (not the CR- money receipt) because card/online debt payments never create a CR-. A migration adds a `DP-` number + balance snapshot to `customer_payments`; a new `escpos.DebtDocument` renders the slip; cashier + admin print it at payment time replacing the generic money slip; a new "Credit" tab in the unified Receipts page lists/views/reprints them. Returns and warranty get small additions to their existing `escpos` documents.

**Tech Stack:** Go, Echo v4, sqlx, lib/pq, Goose migrations, Templ, Alpine/HTMX, shopspring/decimal, ESC/POS (`internal/escpos`).

## Global Constraints

- Do NOT commit `cmd/server/enabled_plugins.go` (keep remote core-only).
- Do NOT stage `static/css/tailwind.css`; run `make css` if new utility classes appear, leave unstaged.
- `_templ.go` files are generated — run `make templ`, never stage them.
- Commit messages end with `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`.
- Receipt number prefix is exactly `DP-` with 6-digit zero padding (`DP-000123`).
- Dev DB: Postgres via `make migrate` (loads `.env`); psql runs inside the docker container as
  `docker exec <postgres-cid> psql -U pos_user -d pos_db`. Dev server on port 3000; admin login
  0771234567/1234, cashier 0771111111/1111.
- Run after Go/templ changes: `make templ && go build ./... && go vet ./...` must be green.

---

### Task 1: Migration + payment snapshot (number + balances)

**Files:**
- Create: `migrations/0042_debt_receipts.sql`
- Modify: `internal/features/customers/customers.go` (struct `CustomerPayment`, `PaymentResult`, `RecordPaymentTx` INSERT ~line 172 and ~442)

**Interfaces:**
- Produces: `PaymentResult{ PaymentID int64; Amount decimal.Decimal; Method string; ReceiptNo string; BalanceBefore, BalanceAfter decimal.Decimal }`
- Produces: `CustomerPayment` gains `ReceiptNo *string`, `BalanceBefore *decimal.Decimal`, `BalanceAfter *decimal.Decimal` (db tags `receipt_no`, `balance_before`, `balance_after`).

- [ ] **Step 1: Write the migration**

`migrations/0042_debt_receipts.sql`:
```sql
-- +goose Up
CREATE SEQUENCE debt_receipt_seq;
ALTER TABLE customer_payments
  ADD COLUMN receipt_no     TEXT,
  ADD COLUMN balance_before NUMERIC(14,2),
  ADD COLUMN balance_after  NUMERIC(14,2);
UPDATE customer_payments
  SET receipt_no = 'DP-' || lpad(nextval('debt_receipt_seq')::text, 6, '0')
  WHERE receipt_no IS NULL;
CREATE UNIQUE INDEX customer_payments_receipt_no_key ON customer_payments(receipt_no);

-- +goose Down
DROP INDEX IF EXISTS customer_payments_receipt_no_key;
ALTER TABLE customer_payments
  DROP COLUMN receipt_no,
  DROP COLUMN balance_before,
  DROP COLUMN balance_after;
DROP SEQUENCE IF EXISTS debt_receipt_seq;
```

- [ ] **Step 2: Apply it and verify columns + backfill**

Run: `make migrate`
Then: `docker exec $(docker compose ps -q postgres) psql -U pos_user -d pos_db -c "\d customer_payments" | grep -E "receipt_no|balance_"`
Expected: three new columns listed. (If there are seeded payments, they have a `DP-` number; a fresh dev DB has none — fine.)

- [ ] **Step 3: Extend the structs**

In `internal/features/customers/customers.go`, add to `CustomerPayment` (around line 64):
```go
	ReceiptNo     *string          `db:"receipt_no"     json:"receipt_no"`
	BalanceBefore *decimal.Decimal `db:"balance_before" json:"balance_before"`
	BalanceAfter  *decimal.Decimal `db:"balance_after"  json:"balance_after"`
```
Add to `PaymentResult` (around line 410):
```go
	ReceiptNo     string
	BalanceBefore decimal.Decimal
	BalanceAfter  decimal.Decimal
```

- [ ] **Step 4: Snapshot in RecordPaymentTx**

In `RecordPaymentTx` (`internal/features/customers/customers.go` ~419-442): the method already
loads/locks the customer to compute the new balance. Capture `before := <current outstanding>`
and `after := before.Sub(creditReduction)` (use the same reduction value it already applies to
the customer row — the portion of the payment that reduces credit). Change the INSERT
(~line 172) to assign the number + store balances and RETURN them:
```go
	var payID int64
	var receiptNo string
	err = tx.QueryRowxContext(ctx, `
		INSERT INTO customer_payments
			(customer_id, amount, method, reference, note, created_by,
			 receipt_no, balance_before, balance_after)
		VALUES ($1,$2,$3,$4,$5,$6,
			'DP-' || lpad(nextval('debt_receipt_seq')::text, 6, '0'), $7, $8)
		RETURNING id, receipt_no`,
		id, amt, method, ref, note, createdBy, before, after,
	).Scan(&payID, &receiptNo)
	if err != nil {
		return nil, err
	}
```
Return them:
```go
	return &PaymentResult{
		PaymentID: payID, Amount: amt, Method: method,
		ReceiptNo: receiptNo, BalanceBefore: before, BalanceAfter: after,
	}, nil
```
(Match the existing variable names for `amt`, `method`, `ref`, `note`, `creditReduction` in the
current function — read it first; do not rename them.)

- [ ] **Step 5: Build + vet**

Run: `make templ && go build ./... && go vet ./...`
Expected: no output (success).

- [ ] **Step 6: Commit**

```bash
git add migrations/0042_debt_receipts.sql internal/features/customers/customers.go
git commit -m "feat(receipts): DP- number + balance snapshot on customer_payments

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: `escpos.DebtDocument` slip

**Files:**
- Modify: `internal/escpos/escpos.go` (add `DebtSlip` + `DebtDocument`, near `WarrantyDocument` ~line 318)
- Test: `internal/escpos/escpos_test.go`

**Interfaces:**
- Produces: `type DebtSlip struct { ReceiptNo, Date, CustomerName, CustomerPhone, Method, CashierName string; Amount decimal.Decimal; BalanceBefore, BalanceAfter, CreditLimit *decimal.Decimal }`
- Produces: `func DebtDocument(s DebtSlip, cfg settings.Settings, opts Options) []byte`

- [ ] **Step 1: Write the failing test**

In `internal/escpos/escpos_test.go`:
```go
func TestDebtDocument(t *testing.T) {
	d := decimal.RequireFromString
	before, after, limit := d("5000.00"), d("3000.00"), d("10000.00")
	out := DebtDocument(DebtSlip{
		ReceiptNo: "DP-000123", Date: "2026-06-28 14:05",
		CustomerName: "Nimal Perera", CustomerPhone: "0771239876",
		Method: "Cash", CashierName: "Kamal", Amount: d("2000.00"),
		BalanceBefore: &before, BalanceAfter: &after, CreditLimit: &limit,
	}, cfg("80"), Options{})
	s := string(out)
	for _, want := range []string{"CREDIT PAYMENT", "DP-000123", "Nimal Perera", "0771239876", "2,000.00", "3,000.00"} {
		if !strings.Contains(s, want) {
			t.Errorf("debt slip missing %q", want)
		}
	}
	if out[0] != esc || out[1] != '@' {
		t.Fatalf("expected ESC @ init")
	}
	if !strings.HasSuffix(s, string([]byte{gs, 'V', 1})) {
		t.Fatalf("expected partial-cut at end")
	}
}

func TestDebtDocumentOmitsNullBalances(t *testing.T) {
	d := decimal.RequireFromString
	out := DebtDocument(DebtSlip{
		ReceiptNo: "DP-000099", Date: "2026-06-28 10:00",
		CustomerName: "Old Row", Method: "Cash", Amount: d("500.00"),
	}, cfg("80"), Options{})
	if strings.Contains(string(out), "Remaining balance") {
		t.Errorf("expected no balance block when balances are nil")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/escpos/ -run TestDebtDocument -v`
Expected: FAIL (`DebtDocument`/`DebtSlip` undefined).

- [ ] **Step 3: Implement `DebtSlip` + `DebtDocument`**

In `internal/escpos/escpos.go` after `WarrantyDocument`. Mirror `ReturnDocument`'s header
(ESC @, codepage, centered shop name via `gs ! 0x11`, optional `opts.SubName`, `*** CREDIT
PAYMENT ***` bold), then left-aligned body using the existing `leftRight`, `divider`, `line`,
`wrap`, `ascii`, `money.Format` helpers and `columns(cfg.ReceiptWidth)`:
```go
type DebtSlip struct {
	ReceiptNo, Date, CustomerName, CustomerPhone, Method, CashierName string
	Amount                                   decimal.Decimal
	BalanceBefore, BalanceAfter, CreditLimit *decimal.Decimal
}

func DebtDocument(s DebtSlip, cfg settings.Settings, opts Options) []byte {
	w := columns(cfg.ReceiptWidth)
	sym := cfg.CurrencySymbol
	if sym == "" {
		sym = "Rs."
	}
	var b bytes.Buffer
	b.Write([]byte{esc, '@'})
	b.Write([]byte{esc, 't', 0})
	// header
	b.Write([]byte{esc, 'a', 1})
	if len(opts.Logo) > 0 {
		b.Write(opts.Logo)
		line(&b, "")
	}
	b.Write([]byte{esc, 'E', 1})
	b.Write([]byte{gs, '!', 0x11})
	line(&b, ascii(cfg.ShopName))
	b.Write([]byte{gs, '!', 0x00})
	b.Write([]byte{esc, 'E', 0})
	if len(opts.SubName) > 0 {
		b.Write(opts.SubName)
	}
	line(&b, "")
	b.Write([]byte{esc, 'E', 1})
	line(&b, "*** CREDIT PAYMENT ***")
	b.Write([]byte{esc, 'E', 0})
	// meta
	b.Write([]byte{esc, 'a', 0})
	divider(&b, w)
	line(&b, leftRight("Receipt:", s.ReceiptNo, w))
	line(&b, leftRight("Date:", s.Date, w))
	line(&b, leftRight("Customer:", ascii(s.CustomerName), w))
	if s.CustomerPhone != "" {
		line(&b, leftRight("Phone:", ascii(s.CustomerPhone), w))
	}
	divider(&b, w)
	// amount
	b.Write([]byte{esc, 'E', 1})
	line(&b, leftRight("Amount paid", money.Format(sym, s.Amount), w))
	b.Write([]byte{esc, 'E', 0})
	line(&b, leftRight("Method:", s.Method, w))
	// balances (omitted for backfilled rows)
	if s.BalanceBefore != nil && s.BalanceAfter != nil {
		divider(&b, w)
		line(&b, leftRight("Previous balance", money.Format(sym, *s.BalanceBefore), w))
		b.Write([]byte{esc, 'E', 1})
		line(&b, leftRight("Remaining balance", money.Format(sym, *s.BalanceAfter), w))
		b.Write([]byte{esc, 'E', 0})
		if s.CreditLimit != nil {
			line(&b, leftRight("Credit limit", money.Format(sym, *s.CreditLimit), w))
		}
	}
	divider(&b, w)
	b.Write([]byte{esc, 'a', 1})
	if s.CashierName != "" {
		line(&b, "Served by: "+ascii(s.CashierName))
	}
	line(&b, "Thank you - please retain.")
	b.Write([]byte{esc, 'd', feedBeforeCut})
	b.Write([]byte{gs, 'V', 1})
	return b.Bytes()
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/escpos/ -run TestDebtDocument -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add internal/escpos/escpos.go internal/escpos/escpos_test.go
git commit -m "feat(receipts): escpos DebtDocument credit-payment slip

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: Print debt slip at payment time (cashier + admin)

**Files:**
- Modify: `internal/web/cashier_receipts.go` (add `buildDebtSlip` beside `buildWarrantySlip`)
- Modify: `internal/web/cashier.go` (`CreditPay` ~556-584: print debt slip, drop generic)
- Modify: `internal/web/admin_more.go` (credit collection flow ~1342: print debt slip)

**Interfaces:**
- Consumes: `customers.PaymentResult` (Task 1), `escpos.DebtDocument`/`DebtSlip` (Task 2).
- Produces: `func (s *Server) buildDebtSlip(ctx context.Context, cfg *settings.Settings, p customers.CustomerPayment, cust *customers.Customer) []byte` — assembles a `DebtSlip` and calls `escpos.DebtDocument` with `s.receiptImgOptions(ctx, cfg)`.

- [ ] **Step 1: Add `buildDebtSlip`**

In `internal/web/cashier_receipts.go` (beside `buildWarrantySlip`):
```go
// buildDebtSlip renders a credit-payment slip for printing/reprint (UI-agnostic).
func (s *Server) buildDebtSlip(ctx context.Context, cfg *settings.Settings, p customers.CustomerPayment, cust *customers.Customer) []byte {
	slip := escpos.DebtSlip{
		Date:          datetime.DateTime(p.CreatedAt),
		Method:        txMethodLabel(p.Method),
		Amount:        p.Amount,
		BalanceBefore: p.BalanceBefore,
		BalanceAfter:  p.BalanceAfter,
	}
	if p.ReceiptNo != nil {
		slip.ReceiptNo = *p.ReceiptNo
	}
	if cust != nil {
		slip.CustomerName = cust.Name
		if cust.Phone != nil {
			slip.CustomerPhone = *cust.Phone
		}
		cl := cust.CreditLimit
		slip.CreditLimit = &cl
	}
	if p.CreatedByName != nil { // if CustomerPayment exposes the cashier name; else omit
		slip.CashierName = *p.CreatedByName
	}
	return escpos.DebtDocument(slip, *cfg, s.receiptImgOptions(ctx, cfg))
}
```
Use a method-label helper consistent with the codebase: if `txMethodLabel` does not already
exist, inline a small `map[string]string{"cash":"Cash","card":"Card","online":"Online"}` lookup
defaulting to the raw value. (Read `cashier_receipts.go` for an existing label helper first and
reuse it.) If `CustomerPayment` has no cashier-name field, drop the `CashierName` line here and
set it in the reprint/print caller where the name is known.

- [ ] **Step 2: Wire cashier `CreditPay` to print the debt slip**

In `internal/web/cashier.go` `CreditPay` (~556-584): the handler already has `cust` (the
customer) and gets `res *customers.PaymentResult` from `RecordPaymentTx`. After the tx commits,
replace the `if rec != nil { h.s.printMoneyReceipt(...) }` block with a debt-slip print that
runs for **every** method:
```go
	// Hand the customer a detailed credit-payment slip (all methods). The CR-
	// money record is still created for cash inside the tx (tracking unchanged);
	// it is just no longer the paper handed over.
	pay := customers.CustomerPayment{
		Amount: res.Amount, Method: res.Method, CreatedAt: time.Now(),
		ReceiptNo: &res.ReceiptNo, BalanceBefore: &res.BalanceBefore, BalanceAfter: &res.BalanceAfter,
	}
	cfg, _ := h.s.settings.Get(ctx)
	if cfg != nil {
		slip := h.s.buildDebtSlip(ctx, cfg, pay, cust)
		slip = withCashier(slip, middleware.CurrentUserName(c)) // or set CashierName before building
		_ = escpos.Send(ctx, h.receiptQueue(c, cfg), slip)
	}
	msg := "Payment recorded · " + res.ReceiptNo
```
Simpler if `buildDebtSlip` takes the cashier name as a param: change its signature to
`buildDebtSlip(ctx, cfg, pay, cust, cashierName string)` and set `slip.CashierName = cashierName`
inside it (drop the `CreatedByName` guess from Step 1). Pass `middleware.CurrentUserName(c)`.
Keep the `logAudit` + `htmxDone(c, msg, "reload-ccredit")` lines.

- [ ] **Step 3: Wire admin credit collection to print the debt slip**

In `internal/web/admin_more.go` credit flow (~1342, the `RecordPaymentTx` + `MoveTx` block that
currently ends with `return a.s.afterMoneyMove(c, rec)`): keep the money move (CR- tracking),
but print the debt slip instead of the generic one. After the tx, honoring the existing
`ask_to_print` policy in that area:
```go
	cfg, _ := a.s.settings.Get(ctx)
	if cfg != nil {
		pay := customers.CustomerPayment{
			Amount: res.Amount, Method: res.Method, CreatedAt: time.Now(),
			ReceiptNo: &res.ReceiptNo, BalanceBefore: &res.BalanceBefore, BalanceAfter: &res.BalanceAfter,
		}
		_ = printing.Raw(ctx, cfg.ReceiptPrinter, a.s.buildDebtSlip(ctx, cfg, pay, cust, middleware.CurrentUserName(c)))
	}
	a.s.logAudit(c, audit.ActionPayment, "customer", strconv.FormatInt(id, 10), "credit payment "+in.Amount)
	return htmxDone(c, "Payment recorded · "+res.ReceiptNo, "reload-customers")
```
Remove the now-unused `afterMoneyMove(c, rec)` return for this flow only (other flows keep it).
If `rec` becomes unused, keep the `MoveTx` call but assign `_`.

- [ ] **Step 4: Build + vet**

Run: `make templ && go build ./... && go vet ./...`
Expected: success. Fix any unused-import/var fallout (e.g. `printMoneyReceipt` no longer used by
cashier — leave it if still referenced elsewhere; otherwise it stays defined for reprint).

- [ ] **Step 5: E2E — cash debt payment prints DP- slip, not generic**

Start server (`go build -o /tmp/posserver ./cmd/server` then run with `.env`). Log in as cashier
(0771111111/1111), go to Credit collection, record a cash repayment for a customer that owes.
Verify via psql the row has a `DP-` `receipt_no` and both balances, and a CR- money receipt row
still exists for the cash leg:
```
docker exec $(docker compose ps -q postgres) psql -U pos_user -d pos_db -c \
 "SELECT receipt_no,balance_before,balance_after,method FROM customer_payments ORDER BY id DESC LIMIT 1;"
docker exec $(docker compose ps -q postgres) psql -U pos_user -d pos_db -c \
 "SELECT receipt_no,kind FROM money_receipts ORDER BY id DESC LIMIT 1;"
```
Expected: `DP-...` with before/after; a `CR-...` `customer_payment` row present. (Printing is
best-effort/no-op without a printer; the server log shows no print error path taken.)

- [ ] **Step 6: Commit**

```bash
git add internal/web/cashier_receipts.go internal/web/cashier.go internal/web/admin_more.go
git commit -m "feat(receipts): print DP- credit slip at payment (cashier+admin), drop generic

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 4: Payment reads for the Credit tab

**Files:**
- Modify: `internal/features/customers/customers.go` (add `DebtReceipt`, `DebtFilter`, `ListPayments`, `GetPayment`)

**Interfaces:**
- Produces:
  ```go
  type DebtReceipt struct {
      ID            int64
      ReceiptNo     string
      CreatedAt     time.Time
      CustomerName  string
      CustomerPhone *string
      Amount        decimal.Decimal
      Method        string
      BalanceBefore *decimal.Decimal
      BalanceAfter  *decimal.Decimal
      CreditLimit   decimal.Decimal
      CashierName   *string
  }
  type DebtFilter struct { Query string; From, To time.Time; Limit int }
  func (s *Service) ListPayments(ctx context.Context, f DebtFilter) ([]DebtReceipt, error)
  func (s *Service) GetPayment(ctx context.Context, id int64) (*DebtReceipt, error)
  ```

- [ ] **Step 1: Add the view struct + reads**

In `internal/features/customers/customers.go`. The query joins `customers` and `users`:
```go
const debtSelect = `
	SELECT cp.id, cp.receipt_no, cp.created_at, c.name AS customer_name, c.phone AS customer_phone,
	       cp.amount, cp.method, cp.balance_before, cp.balance_after, c.credit_limit,
	       u.name AS cashier_name
	FROM customer_payments cp
	JOIN customers c ON c.id = cp.customer_id
	LEFT JOIN users u ON u.id = cp.created_by`

func (s *Service) ListPayments(ctx context.Context, f DebtFilter) ([]DebtReceipt, error) {
	q := debtSelect + ` WHERE cp.receipt_no IS NOT NULL`
	args := []any{}
	n := 1
	if t := strings.TrimSpace(f.Query); t != "" {
		q += fmt.Sprintf(` AND (cp.receipt_no ILIKE $%d OR c.name ILIKE $%d OR c.phone ILIKE $%d)`, n, n, n)
		args = append(args, "%"+t+"%")
		n++
	}
	if !f.From.IsZero() {
		q += fmt.Sprintf(` AND cp.created_at >= $%d`, n); args = append(args, f.From); n++
	}
	if !f.To.IsZero() {
		q += fmt.Sprintf(` AND cp.created_at < $%d`, n); args = append(args, f.To); n++
	}
	q += ` ORDER BY cp.id DESC`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT $%d`, n); args = append(args, f.Limit)
	}
	var rows []DebtReceipt
	if err := s.db.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, apperr.Internal("failed to list credit payments", err)
	}
	return rows, nil
}

func (s *Service) GetPayment(ctx context.Context, id int64) (*DebtReceipt, error) {
	var r DebtReceipt
	if err := s.db.GetContext(ctx, &r, debtSelect+` WHERE cp.id = $1`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("payment")
		}
		return nil, apperr.Internal("failed to load credit payment", err)
	}
	return &r, nil
}
```
Add `db` tags on `DebtReceipt` fields to match the column aliases (`receipt_no`, `customer_name`,
`customer_phone`, `balance_before`, `balance_after`, `credit_limit`, `cashier_name`). Add any
missing imports (`fmt`, `errors`, `database/sql`, `strings`, `time`).

- [ ] **Step 2: Build + vet**

Run: `make templ && go build ./... && go vet ./...`
Expected: success.

- [ ] **Step 3: E2E read check**

With the dev server's DB holding the Task 3 payment, confirm the query path works via a tiny
psql mirror of the select:
```
docker exec $(docker compose ps -q postgres) psql -U pos_user -d pos_db -c \
 "SELECT cp.receipt_no, c.name, cp.amount FROM customer_payments cp JOIN customers c ON c.id=cp.customer_id ORDER BY cp.id DESC LIMIT 3;"
```
Expected: the recorded `DP-` payment with the customer name + amount.

- [ ] **Step 4: Commit**

```bash
git add internal/features/customers/customers.go
git commit -m "feat(receipts): customers ListPayments/GetPayment for the Credit tab

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 5: Credit tab — cashier (list/search/view/reprint)

**Files:**
- Modify: `templates/pages/cashier/receipts.templ` (add "Credit" tab button + lazy panel)
- Create: `templates/pages/cashier/receipts_credit.templ` (`ReceiptsCreditTab` fragment + row markup)
- Create: `templates/pages/cashier/debt_receipt.templ` (HTML View page)
- Modify: `internal/web/cashier_receipts.go` (`ReceiptsCredit`, `DebtReceiptView`, `DebtReceiptPrint` handlers)
- Modify: `internal/web/web.go` (routes)

**Interfaces:**
- Consumes: `customers.ListPayments`/`GetPayment`/`DebtReceipt` (Task 4), `Server.buildDebtSlip` (Task 3), `shared.RangeForm`, `resolveReceiptRange` (existing).
- Produces routes: `GET /cashier/receipts/credit`, `GET /cashier/receipts/credit/:id`, `POST /cashier/receipts/credit/:id/print`.

- [ ] **Step 1: Add the fragment + handlers**

Mirror the existing Warranty tab. In `internal/web/cashier_receipts.go`:
```go
func (h *cashierUI) ReceiptsCredit(c echo.Context) error {
	ctx := c.Request().Context()
	from, to, fromStr, toStr, err := resolveReceiptRange(c)
	if err != nil {
		return err
	}
	q := strings.TrimSpace(c.QueryParam("q"))
	rows, err := h.s.customers.ListPayments(ctx, customers.DebtFilter{Query: q, From: from, To: to, Limit: 50})
	if err != nil {
		return err
	}
	return response.RenderFragment(c, cashierpages.ReceiptsCreditTab(cashierpages.DebtData{
		Symbol: h.cashierSymbol(ctx), Query: q, FromStr: fromStr, ToStr: toStr, Rows: rows,
	}))
}

func (h *cashierUI) DebtReceiptView(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	r, err := h.s.customers.GetPayment(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.RenderPage(c, cashierpages.DebtReceiptPage(*r, h.cashierSymbol(c.Request().Context())))
}

func (h *cashierUI) DebtReceiptPrint(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	r, err := h.s.customers.GetPayment(ctx, id)
	if err != nil {
		return err
	}
	cfg, err := h.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	pay := customers.CustomerPayment{
		Amount: r.Amount, Method: r.Method, CreatedAt: r.CreatedAt,
		ReceiptNo: &r.ReceiptNo, BalanceBefore: r.BalanceBefore, BalanceAfter: r.BalanceAfter,
	}
	cust := &customers.Customer{Name: r.CustomerName, Phone: r.CustomerPhone, CreditLimit: r.CreditLimit}
	name := ""
	if r.CashierName != nil {
		name = *r.CashierName
	}
	if err := escpos.Send(ctx, h.receiptQueue(c, cfg), h.s.buildDebtSlip(ctx, cfg, pay, cust, name)); err != nil {
		return apperr.Internal("could not print receipt", err)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Receipt sent to printer", "success"))
	return response.OK(c, map[string]bool{"ok": true})
}
```
(Adjust `DebtData` field names to whatever the templ fragment in Step 2 expects; keep them
consistent.)

- [ ] **Step 2: Add the templ fragments**

`templates/pages/cashier/receipts_credit.templ` — `DebtData` struct (`Symbol, Query, FromStr,
ToStr string; Rows []customers.DebtReceipt`) and `ReceiptsCreditTab(d DebtData)`: a search box +
`@shared.RangeForm(...)` (copy the Warranty tab's form attrs, pointing `hx-get` at
`/cashier/receipts/credit`), then a table (Date / DP- no / Customer / Amount / Method / By) with
per-row **View** (`hx-get` the `/credit/:id` page or a link) and **Reprint**
(`hx-post=/cashier/receipts/credit/:id/print`, `hx-swap=none`). Empty-state row "No credit
payments.".
`templates/pages/cashier/debt_receipt.templ` — `DebtReceiptPage(r customers.DebtReceipt, symbol
string)`: a print-friendly page (shop header is rendered by the layout; reuse the warranty/cash
view page as the template) showing all fields incl. balance before→after + credit limit, a
browser **Print** button (`onclick="window.print()"`) and a **Reprint slip** button posting to
`/cashier/receipts/credit/:id/print`.

- [ ] **Step 3: Add the tab button to the Receipts shell**

In `templates/pages/cashier/receipts.templ`, add a "Credit" tab button beside Warranty that
lazy-loads `/cashier/receipts/credit` into the tab panel (copy the exact Warranty tab button +
`hx-get`/`hx-target` wiring).

- [ ] **Step 4: Register routes**

In `internal/web/web.go` (cashier group, beside the warranty receipt routes):
```go
	cg.GET("/receipts/credit", cu.ReceiptsCredit)
	cg.GET("/receipts/credit/:id", cu.DebtReceiptView)
	cg.POST("/receipts/credit/:id/print", cu.DebtReceiptPrint)
```
(Use the actual cashier group variable name and middleware already used for `/receipts/...`.)

- [ ] **Step 5: Build + vet**

Run: `make templ && go build ./... && go vet ./...`
Expected: success.

- [ ] **Step 6: E2E — Credit tab lists, searches, views, reprints**

Restart server. As cashier open Receipts → Credit tab. Verify with Playwright:
- the `DP-` payment from Task 3 is listed with customer + amount;
- searching the customer's name filters to it; searching the `DP-` number finds it;
- View opens the HTML page showing balance before→after + credit limit;
- Reprint returns a success toast (network 200 on the print POST).

- [ ] **Step 7: Commit**

```bash
git add templates/pages/cashier/receipts.templ templates/pages/cashier/receipts_credit.templ templates/pages/cashier/debt_receipt.templ internal/web/cashier_receipts.go internal/web/web.go
git commit -m "feat(receipts): cashier Credit tab (list/search/view/reprint DP-)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 6: Credit tab — admin

**Files:**
- Modify: `internal/web/admin_receipts.go` (admin `ReceiptsCredit`, `DebtReceiptView`, `DebtReceiptPrint`)
- Modify: the admin Receipts page templ (add "Credit" tab — same file the admin Sales/Cash/Warranty tabs live in)
- Modify: `internal/web/web.go` (admin routes)

**Interfaces:**
- Consumes: same `customers` reads + `buildDebtSlip` as Task 5; reuses `cashierpages.ReceiptsCreditTab`/`DebtReceiptPage` (shared templ) or an admin equivalent if the admin page uses its own components.

- [ ] **Step 1: Add admin handlers**

In `internal/web/admin_receipts.go`, mirror Task 5's three handlers but under the admin UI
receiver (`a *adminUI` / `a.s`), rendering the same shared fragments. For print use
`printing.Raw(ctx, cfg.ReceiptPrinter, a.s.buildDebtSlip(...))` (admin has no till queue), matching
how admin warranty reprint works (`admin_receipts.go:119`).

- [ ] **Step 2: Add the admin tab + routes**

Add the "Credit" tab button to the admin Receipts page templ (copy the admin Warranty tab), and
register the admin routes in `internal/web/web.go` beside the admin warranty receipt routes:
```go
	ag.GET("/receipts/credit", au.ReceiptsCredit)
	ag.GET("/receipts/credit/:id", au.DebtReceiptView)
	ag.POST("/receipts/credit/:id/print", au.DebtReceiptPrint)
```
(Use the real admin group var + handler receiver names.)

- [ ] **Step 3: Build + vet**

Run: `make templ && go build ./... && go vet ./...`
Expected: success.

- [ ] **Step 4: E2E — admin Credit tab**

As admin (0771234567/1234) open the admin Receipts page → Credit tab; verify the same `DP-`
payment lists, View works, and Reprint returns 200.

- [ ] **Step 5: Commit**

```bash
git add internal/web/admin_receipts.go internal/web/web.go templates/...
git commit -m "feat(receipts): admin Credit tab (list/view/reprint DP-)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 7: Return slip — show remaining balance

**Files:**
- Modify: `internal/features/sales/sales.go` (`ReturnReceipt` struct + `LatestReturn` query ~227)
- Modify: `internal/features/sales/service.go` (`ReturnReceipt(...)` ~479 — populate customer fields)
- Modify: `internal/escpos/escpos.go` (`ReturnDocument` ~245 — render remaining balance)
- Test: `internal/escpos/escpos_test.go`

**Interfaces:**
- Produces: `ReturnReceipt` gains `CustomerName *string` and `RemainingBalance *decimal.Decimal`.

- [ ] **Step 1: Write the failing test**

In `internal/escpos/escpos_test.go`:
```go
func TestReturnDocumentShowsRemainingBalance(t *testing.T) {
	d := decimal.RequireFromString
	name := "Nimal Perera"
	rem := d("3000.00")
	rr := sales.ReturnReceipt{
		ReceiptNo: "R-0001", Refund: d("500.00"),
		CustomerName: &name, RemainingBalance: &rem,
	}
	out := string(ReturnDocument(rr, cfg("80"), Options{}))
	if !strings.Contains(out, "Nimal Perera") || !strings.Contains(out, "3,000.00") {
		t.Errorf("return slip should show customer + remaining balance")
	}
}

func TestReturnDocumentWalkInOmitsBalance(t *testing.T) {
	d := decimal.RequireFromString
	out := string(ReturnDocument(sales.ReturnReceipt{ReceiptNo: "R-0002", Refund: d("500.00")}, cfg("80"), Options{}))
	if strings.Contains(out, "Remaining balance") {
		t.Errorf("walk-in return must not show a balance line")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/escpos/ -run TestReturnDocument -v`
Expected: FAIL (unknown fields `CustomerName`/`RemainingBalance`).

- [ ] **Step 3: Add the fields + render**

In `internal/features/sales/sales.go` add to `ReturnReceipt`:
```go
	CustomerName     *string          `db:"customer_name"`
	RemainingBalance *decimal.Decimal `db:"-"`
```
Populate in `service.go` `ReturnReceipt(...)`: after loading `rr`, if the sale has a customer,
set `rr.CustomerName` and `rr.RemainingBalance` from the customer's current outstanding balance
(reuse `customers.Get` via an injected service or a direct query already available to sales — use
whatever the sales service already has access to; if none, add a small `SELECT name,
outstanding_balance FROM customers WHERE id = (SELECT customer_id FROM sales WHERE id=$1)` and
leave both nil when `customer_id` is NULL). In `escpos.go` `ReturnDocument`, after the
"Credit reduced" block and before the footer divider:
```go
	if rr.CustomerName != nil {
		line(&b, leftRight("Customer:", ascii(*rr.CustomerName), w))
	}
	if rr.RemainingBalance != nil {
		line(&b, leftRight("Remaining balance", money.Format(sym, *rr.RemainingBalance), w))
	}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/escpos/ -run TestReturnDocument -v`
Expected: PASS.

- [ ] **Step 5: Build + vet + commit**

```bash
make templ && go build ./... && go vet ./...
git add internal/features/sales/sales.go internal/features/sales/service.go internal/escpos/escpos.go internal/escpos/escpos_test.go
git commit -m "feat(receipts): return slip shows customer remaining balance

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 8: Warranty slip — show remaining months

**Files:**
- Modify: `internal/escpos/escpos.go` (`WarrantySlip` + `WarrantyDocument` ~318-360)
- Modify: `internal/web/cashier_receipts.go` (`buildWarrantySlip` ~154 — compute months left)
- Test: `internal/escpos/escpos_test.go`

**Interfaces:**
- Produces: `WarrantySlip` gains `WarrantyLeft string` (preformatted, e.g. "8 mo left" / "expired").

- [ ] **Step 1: Write the failing test**

```go
func TestWarrantyDocumentShowsMonthsLeft(t *testing.T) {
	out := string(WarrantyDocument(WarrantySlip{
		ProductName: "Drill", NewSerial: "SN2", WarrantyUntil: "2027-06-13", WarrantyLeft: "8 mo left",
	}, cfg("80"), Options{}))
	if !strings.Contains(out, "2027-06-13") || !strings.Contains(out, "8 mo left") {
		t.Errorf("warranty slip should show end date and months left")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/escpos/ -run TestWarrantyDocumentShowsMonthsLeft -v`
Expected: FAIL (unknown field `WarrantyLeft`).

- [ ] **Step 3: Add field + render + compute**

In `escpos.go` add `WarrantyLeft string` to `WarrantySlip`; in `WarrantyDocument` change the
warranty-until line to append it when set:
```go
	wu := s.WarrantyUntil
	if s.WarrantyLeft != "" {
		wu = s.WarrantyUntil + " (" + s.WarrantyLeft + ")"
	}
	line(&b, leftRight("Warranty until:", wu, w))
```
In `internal/web/cashier_receipts.go` `buildWarrantySlip`, compute the label from
`u.WarrantyUntil`:
```go
	slip.WarrantyLeft = monthsLeftLabel(u.WarrantyUntil)
```
Add the pure helper (same file) and a quick unit test in `internal/web` is optional; the escpos
test covers rendering:
```go
// monthsLeftLabel renders the whole months remaining until t ("3 mo left"),
// or "expired" once past. Used on the warranty replacement slip.
func monthsLeftLabel(t time.Time) string {
	now := time.Now()
	if !t.After(now) {
		return "expired"
	}
	months := int(t.Sub(now).Hours() / 24 / 30) // ~30-day months; good enough for a slip
	if months <= 0 {
		return "under 1 mo left"
	}
	return strconv.Itoa(months) + " mo left"
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/escpos/ -run TestWarrantyDocumentShowsMonthsLeft -v`
Expected: PASS.

- [ ] **Step 5: Build + vet + commit**

```bash
make templ && go build ./... && go vet ./...
git add internal/escpos/escpos.go internal/escpos/escpos_test.go internal/web/cashier_receipts.go
git commit -m "feat(receipts): warranty slip shows months left next to end date

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 9: Full E2E sweep + memory

- [ ] **Step 1: Run the whole suite**

Run: `go test ./... 2>&1 | tail -20`
Expected: all packages PASS (or pre-existing-only failures unrelated to this work).

- [ ] **Step 2: Manual E2E of all four slips**

With the dev server: (a) cash debt payment → DP- slip + DB snapshot + CR- present; (b) card debt
payment → DP- slip, no CR-; (c) Credit tab on cashier + admin (list/search/view/reprint);
(d) a return on a sale with a customer → refund slip shows remaining balance, walk-in omits it;
(e) a warranty claim → slip shows "(N mo left)". Capture psql confirmation for (a)/(b).

- [ ] **Step 3: Update memory**

Add `customer-facing-receipts.md` (debt DP- receipts + return/warranty enhancements; migration
0042; Credit tab on both roles) and a one-line pointer in `MEMORY.md`.

- [ ] **Step 4: make css if needed**

If any new Tailwind utility classes were introduced in the new templ files, run `make css`
(leave `static/css/tailwind.css` unstaged).

---

## Self-Review

- **Spec coverage:** §1 data → Task 1; §3 DebtDocument → Task 2; §4 print-at-time → Task 3; §2
  service reads → Task 4; §3/§5 Credit tab → Tasks 5–6; §6 return enhancement → Task 7; §7
  warranty enhancement → Task 8; testing matrix → Task 9. All sections covered.
- **Placeholder scan:** No TBD/TODO; code shown for each code step. Two spots say "read the
  existing function first" for variable-name fidelity (`RecordPaymentTx` locals; cashier/admin
  group vars) — these are deliberate fidelity checks against real code, not missing content.
- **Type consistency:** `PaymentResult.ReceiptNo/BalanceBefore/BalanceAfter` (Task 1) consumed in
  Task 3; `DebtSlip` fields (Task 2) consumed by `buildDebtSlip` (Task 3) and reprint (Task 5);
  `DebtReceipt`/`DebtFilter` (Task 4) consumed by Tasks 5–6; `buildDebtSlip(ctx,cfg,pay,cust,name
  string)` signature settled in Task 3 Step 2 and used consistently in Tasks 5–6;
  `ReturnReceipt.CustomerName/RemainingBalance` (Task 7) and `WarrantySlip.WarrantyLeft` (Task 8)
  consistent with their tests.
