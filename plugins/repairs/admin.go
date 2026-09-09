package repairs

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/datetime"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/expenses"
	"karots-pos/internal/features/reports"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

type adminUI struct{ p *Plugin }

func (a *adminUI) symbol(c echo.Context) string {
	if sc, err := a.p.core.Settings.Get(c.Request().Context()); err == nil && sc != nil && sc.CurrencySymbol != "" {
		return sc.CurrencySymbol
	}
	return "Rs."
}

// parseOptDate parses an optional YYYY-MM-DD date (nil when blank/invalid).
func parseOptDate(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if d, err := time.Parse("2006-01-02", s); err == nil {
		return &d
	}
	return nil
}

// jobDue is dueLabel for a whole job: a finished (collected/cancelled) job is
// never "due" or urgent, so its promised-date warning is dropped.
// canRework reports whether a job is eligible for a free warranty re-repair:
// collected and still inside its warranty window.
func canRework(j Job) bool {
	return j.Status == "collected" && j.WarrantyUntil != nil && !time.Now().After(*j.WarrantyUntil)
}

// promisedInput formats a promised date for a date input (blank when none).
func promisedInput(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}

func jobDue(status string, promised *time.Time, urgentFlag bool) (string, bool) {
	if status == "collected" || status == "cancelled" {
		return "—", false
	}
	label, urgent := dueLabel(promised)
	return label, urgent || urgentFlag
}

// dueLabel describes how long until (or past) the promised date, and whether
// the job should read as urgent (overdue or due today/tomorrow).
func dueLabel(promised *time.Time) (string, bool) {
	if promised == nil {
		return "—", false
	}
	today := time.Now().Truncate(24 * time.Hour)
	due := promised.Truncate(24 * time.Hour)
	days := int(due.Sub(today).Hours() / 24)
	switch {
	case days < 0:
		return fmt.Sprintf("overdue %dd", -days), true
	case days == 0:
		return "due today", true
	case days == 1:
		return "due tomorrow", true
	default:
		return fmt.Sprintf("in %dd", days), false
	}
}

// cashLocations lists the real cash sources (lockers + open tills) for the
// pay-repairer picker, mirroring the core expense/bill-pay location choices —
// including each locker's balance so the owner sees what's available.
func (a *adminUI) cashLocations(ctx context.Context) []LocChoice {
	sym := "Rs."
	if cfg, err := a.p.core.Settings.Get(ctx); err == nil && cfg != nil && cfg.CurrencySymbol != "" {
		sym = cfg.CurrencySymbol
	}
	var out []LocChoice
	if lks, err := a.p.core.Lockers.List(ctx, true); err == nil {
		for _, l := range lks {
			out = append(out, LocChoice{
				Value: "locker:" + strconv.FormatInt(l.ID, 10),
				Label: l.Name + " (" + money.Format(sym, l.Balance) + ")", Group: "Lockers",
			})
		}
	}
	if tills, err := a.p.core.CashRegister.OpenSessions(ctx); err == nil {
		for _, t := range tills {
			out = append(out, LocChoice{Value: "till:" + strconv.FormatInt(t.UserID, 10), Label: "Till — " + t.UserName, Group: "Tills"})
		}
	}
	return out
}

// parseCashLocation turns a picker value ("locker:ID" / "till:UID") into a
// cashflow endpoint — same encoding as the core location picker. A real source
// is required: money always moves from a tracked location (no untracked option).
func parseCashLocation(v string) (cashflow.Location, error) {
	v = strings.TrimSpace(v)
	if v == "" || v == "external" {
		return cashflow.Location{}, apperr.Validation("pick where the cash comes from")
	}
	kind, idStr, ok := strings.Cut(v, ":")
	if !ok {
		return cashflow.Location{}, apperr.Validation("invalid cash location")
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return cashflow.Location{}, apperr.Validation("invalid cash location")
	}
	switch kind {
	case "locker":
		return cashflow.Locker(id), nil
	case "till":
		return cashflow.Till(id), nil
	}
	return cashflow.Location{}, apperr.Validation("invalid cash location")
}

// jobInputFromForm reads the shared job fields from a POST form.
func jobInputFromForm(c echo.Context, defWarranty int) JobInput {
	in := JobInput{
		CustomerName:  strings.TrimSpace(c.FormValue("customer_name")),
		CustomerPhone: strings.TrimSpace(c.FormValue("customer_phone")),
		RepairType:    strings.TrimSpace(c.FormValue("repair_type")),
		DeviceModel:   strings.TrimSpace(c.FormValue("device_model")),
		RepairedBy:    strings.TrimSpace(c.FormValue("repaired_by")),
		Fault:         strings.TrimSpace(c.FormValue("fault")),
		Notes:         strings.TrimSpace(c.FormValue("notes")),
		PromisedDate:  parseOptDate(c.FormValue("promised_date")),
		Urgent:        c.FormValue("urgent") != "",
		CreatedBy:     middleware.CurrentUserID(c),
	}
	if cid, err := strconv.ParseInt(c.FormValue("customer_id"), 10, 64); err == nil && cid > 0 {
		in.CustomerID = &cid
	}
	in.WarrantyDays = defWarranty
	if w, err := strconv.Atoi(strings.TrimSpace(c.FormValue("warranty_days"))); err == nil && w >= 0 {
		in.WarrantyDays = w
	}
	return in
}

func (a *adminUI) List(c echo.Context) error {
	ctx := c.Request().Context()
	// Default view is the OPEN queue; collected/cancelled are hidden but reachable
	// via the filter tabs (not vanished).
	show := c.QueryParam("show")
	var statuses []string
	switch show {
	case "collected":
		statuses = []string{"collected"}
	case "cancelled":
		statuses = []string{"cancelled"}
	case "all":
		statuses = []string{"received", "in_progress", "ready", "collected", "cancelled"}
	default:
		show = "open"
		statuses = []string{"received", "in_progress", "ready"}
	}
	// Scan/search wins over the status+date filter: a typed or scanned query
	// looks across every status so a collected job is still findable by ticket.
	q := strings.TrimSpace(c.QueryParam("q"))
	preset, fromStr, toStr := c.QueryParam("preset"), "", ""
	var jobs []Job
	var err error
	switch {
	case q != "":
		jobs, err = a.p.store.Search(ctx, q, 200)
	case preset != "" || c.QueryParam("from") != "" || c.QueryParam("to") != "":
		from, to, fs, ts, rerr := reports.ResolveRange(preset, c.QueryParam("from"), c.QueryParam("to"))
		if rerr != nil {
			return apperr.Validation(rerr.Error())
		}
		fromStr, toStr = fs, ts
		jobs, err = a.p.store.ListByStatusesRange(ctx, statuses, from, to)
	default:
		jobs, err = a.p.store.ListByStatuses(ctx, statuses)
	}
	if err != nil {
		return err
	}
	rows := make([]ListRow, 0, len(jobs))
	for _, j := range jobs {
		label, urgent := jobDue(j.Status, j.PromisedDate, j.Urgent)
		rows = append(rows, ListRow{
			ID: j.ID, TicketNo: j.TicketNo, Customer: j.CustomerName, Phone: j.CustomerPhone,
			Device: j.DeviceModel, RepairType: j.RepairType, Status: j.Status,
			DueLabel: label, Urgent: urgent,
		})
	}
	return response.RenderPage(c, RepairsListPage(ListData{
		UserName: middleware.CurrentUserName(c),
		Show:     show,
		Rows:     rows,
		Query:        q,
		Preset:       preset,
		From:         fromStr,
		To:           toStr,
		WarrantyMode: a.p.warrantyMode,
	}))
}

func (a *adminUI) NewForm(c echo.Context) error {
	ctx := c.Request().Context()
	types, _ := a.p.store.DistinctTypes(ctx)
	models, _ := a.p.store.DistinctModels(ctx)
	repairers, _ := a.p.store.DistinctRepairers(ctx)
	return response.RenderPage(c, RepairFormPage(FormData{
		UserName:     middleware.CurrentUserName(c),
		Types:        types,
		Models:       models,
		Repairers:    repairers,
		WarrantyDays: a.p.defWarranty,
		WarrantyMode: a.p.warrantyMode,
	}))
}

func (a *adminUI) Create(c echo.Context) error {
	ctx := c.Request().Context()
	in := jobInputFromForm(c, a.p.defWarranty)
	if in.DeviceModel == "" && in.RepairType == "" {
		return apperr.Validation("enter at least a repair type or device model")
	}
	id, err := a.p.store.CreateJob(ctx, in)
	if err != nil {
		return err
	}
	a.p.core.Audit.Record(ctx, middleware.CurrentUserID(c), audit.ActionCreate,
		"repair", strconv.FormatInt(id, 10), "opened repair job")
	return c.Redirect(http.StatusSeeOther, "/admin/repairs/"+strconv.FormatInt(id, 10))
}

func (a *adminUI) Detail(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	d, err := a.p.store.GetJob(ctx, id)
	if err != nil {
		return apperr.NotFound("repair")
	}
	return response.RenderPage(c, RepairDetailPage(a.detailData(c, d)))
}

// detailData assembles the view model for the detail page + the lines fragment.
func (a *adminUI) detailData(c echo.Context, d *Detail) DetailData {
	ctx := c.Request().Context()
	sym := a.symbol(c)
	total, dep, bal := JobTotals(d)
	label, urgent := jobDue(d.Job.Status, d.Job.PromisedDate, d.Job.Urgent)
	warranty := ""
	if d.Job.WarrantyUntil != nil {
		warranty = datetime.Date(*d.Job.WarrantyUntil)
	} else if d.Job.WarrantyDays > 0 {
		warranty = fmt.Sprintf("%d days from pickup", d.Job.WarrantyDays)
	}
	// Empty (not "Rs. 0.00") when nothing has been paid, so the detail page can
	// tell paid from unpaid — the audit trail for a "you never paid me" dispute.
	repairerPaid := ""
	if d.Job.RepairerPaid.IsPositive() {
		repairerPaid = money.Format(sym, d.Job.RepairerPaid)
	}
	return DetailData{
		UserName:      middleware.CurrentUserName(c),
		Symbol:        sym,
		D:             d,
		Total:         money.Format(sym, total),
		Deposit:       money.Format(sym, dep),
		Balance:       money.Format(sym, bal),
		RepairerPaid:  repairerPaid,
		DueLabel:      label,
		Urgent:        urgent,
		PromisedInput: promisedInput(d.Job.PromisedDate),
		WarrantyLabel: warranty,
		CanRework:     canRework(d.Job),
		WarrantyMode:  a.p.warrantyMode,
		Editable:      d.Job.Status != "collected" && d.Job.Status != "cancelled",
		Locations:     a.cashLocations(ctx),
	}
}

func (a *adminUI) Update(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	in := jobInputFromForm(c, a.p.defWarranty)
	old, _ := a.p.store.GetJob(ctx, id)
	if err := a.p.store.UpdateJobFields(ctx, id, in); err != nil {
		return err
	}
	if old != nil {
		a.p.auditDeadline(ctx, middleware.CurrentUserID(c), id, old.Job.PromisedDate, in.PromisedDate, old.Job.TicketNo)
	}
	return c.Redirect(http.StatusSeeOther, "/admin/repairs/"+strconv.FormatInt(id, 10))
}

// SetWarrantyMode switches the shop between day-based and part-based warranty.
func (a *adminUI) SetWarrantyMode(c echo.Context) error {
	mode := strings.TrimSpace(c.FormValue("mode"))
	if err := a.p.store.SetWarrantyMode(c.Request().Context(), mode); err != nil {
		return err
	}
	a.p.warrantyMode = mode
	return c.Redirect(http.StatusSeeOther, "/admin/repairs")
}

// WarrantyRework opens a free re-repair linked to a collected, in-warranty job
// and jumps to the new job to add the redo parts/labour.
func (a *adminUI) WarrantyRework(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	newID, err := a.p.startRework(ctx, id, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	return a.redirectDetail(c, newID)
}

// SetUrgent adds or removes the urgent flag on an existing job (optionally
// setting a rush date). Both admin and cashier can do it — a customer often asks
// to rush at the counter, and it's a harmless, customer-facing change.
func (a *adminUI) SetUrgent(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	newP := parseOptDate(c.FormValue("promised_date"))
	old, _ := a.p.store.GetJob(ctx, id)
	if err := a.p.store.SetUrgent(ctx, id, c.FormValue("urgent") == "1", newP); err != nil {
		return err
	}
	if old != nil {
		a.p.auditDeadline(ctx, middleware.CurrentUserID(c), id, old.Job.PromisedDate, newP, old.Job.TicketNo)
	}
	return a.redirectDetail(c, id)
}

func (a *adminUI) SetStatus(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	status := c.FormValue("status")
	switch status {
	case "received", "in_progress", "ready":
	default:
		return apperr.Validation("invalid status")
	}
	if err := a.p.store.SetStatus(ctx, id, status, nil); err != nil {
		return err
	}
	return c.Redirect(http.StatusSeeOther, "/admin/repairs/"+strconv.FormatInt(id, 10))
}

func (a *adminUI) Suggest(c echo.Context) error {
	ctx := c.Request().Context()
	types, _ := a.p.store.DistinctTypes(ctx)
	models, _ := a.p.store.DistinctModels(ctx)
	return c.JSON(http.StatusOK, map[string]any{"types": types, "models": models})
}

func (a *adminUI) AddPart(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	pid, err := strconv.ParseInt(c.FormValue("product_id"), 10, 64)
	if err != nil || pid <= 0 {
		return apperr.Validation("pick a product")
	}
	qty, err := money.Parse(c.FormValue("qty"))
	if err != nil || !qty.IsPositive() {
		return apperr.Validation("quantity must be greater than zero")
	}
	// A real product is always priced by the catalogue on the collection sale, so
	// seed unit_charge from it — the discount is the only per-repair lever.
	prod, err := a.p.core.Products.Get(ctx, pid)
	if err != nil {
		return apperr.NotFound("product")
	}
	dval := decimal.Zero
	if s := strings.TrimSpace(c.FormValue("discount")); s != "" {
		if dval, err = money.Parse(s); err != nil || dval.IsNegative() {
			return apperr.Validation("discount is invalid")
		}
	}
	wd, _ := strconv.Atoi(strings.TrimSpace(c.FormValue("warranty_days")))
	if err := a.p.store.AddPart(ctx, id, PartInput{
		ProductID: pid, Qty: qty, UnitCharge: prod.SellingPrice,
		DiscountType: c.FormValue("discount_type"), DiscountValue: dval, WarrantyDays: wd,
	}); err != nil {
		return err
	}
	return a.redirectDetail(c, id)
}

func (a *adminUI) RemovePart(c echo.Context) error {
	ctx := c.Request().Context()
	pid, err := strconv.ParseInt(c.Param("pid"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.p.store.RemovePart(ctx, pid); err != nil {
		return err
	}
	return a.redirectDetailForm(c)
}

func (a *adminUI) AddCharge(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	label := strings.TrimSpace(c.FormValue("label"))
	if label == "" {
		label = "Labour"
	}
	amount, err := money.Parse(c.FormValue("amount"))
	if err != nil || amount.IsNegative() {
		return apperr.Validation("amount is invalid")
	}
	if err := a.p.store.AddCharge(ctx, id, label, amount); err != nil {
		return err
	}
	return a.redirectDetail(c, id)
}

func (a *adminUI) RemoveCharge(c echo.Context) error {
	ctx := c.Request().Context()
	cid, err := strconv.ParseInt(c.Param("cid"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.p.store.RemoveCharge(ctx, cid); err != nil {
		return err
	}
	return a.redirectDetailForm(c)
}

// redirectDetail sends the browser back to a job's detail page.
func (a *adminUI) redirectDetail(c echo.Context, id int64) error {
	url := "/admin/repairs/" + strconv.FormatInt(id, 10)
	// An HTMX form post can't follow a 303 into a full page cleanly — hand it a
	// client-side redirect instead. On error the central handler already turns an
	// HTMX request into an inline toast (no page swap), so a failed pay-repairer
	// shows the reason inline rather than a jarring full 409 error page.
	if c.Request().Header.Get("HX-Request") == "true" {
		c.Response().Header().Set("HX-Redirect", url)
		return c.NoContent(http.StatusOK)
	}
	return c.Redirect(http.StatusSeeOther, url)
}

// redirectDetailForm redirects using the job_id carried on a remove form (whose
// route has only the child id, not the job id).
func (a *adminUI) redirectDetailForm(c echo.Context) error {
	jid, _ := strconv.ParseInt(c.FormValue("job_id"), 10, 64)
	if jid <= 0 {
		return c.Redirect(http.StatusSeeOther, "/admin/repairs")
	}
	return a.redirectDetail(c, jid)
}

// PayRepairer pays an outside repairer for a job. It books a core Expense (with
// a generated note) and, when a source till is chosen, withdraws the cash from
// it — mirroring how the documents plugin pays its labour. Customer money is
// never touched here; that happens at the cashier.
func (a *adminUI) PayRepairer(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	amount, err := money.Parse(c.FormValue("amount"))
	if err != nil || !amount.IsPositive() {
		return apperr.Validation("amount must be greater than zero")
	}
	uid := middleware.CurrentUserID(c)
	d, err := a.p.store.GetJob(ctx, id)
	if err != nil {
		return apperr.NotFound("repair")
	}
	who := d.Job.RepairedBy
	if who == "" {
		who = "repairer"
	}
	note := "Repair " + d.Job.TicketNo + " — paid " + who
	if n := strings.TrimSpace(c.FormValue("note")); n != "" {
		note += " (" + n + ")"
	}
	loc, err := parseCashLocation(c.FormValue("source"))
	if err != nil {
		return err
	}
	in := expenses.CreateInput{
		Category: "Repairs", Amount: amount.StringFixed(2), Description: &note,
		ExpenseDate: time.Now().Format("2006-01-02"),
	}
	// Book the expense, debit the chosen cash location, and stamp the job — all in
	// one tx (mirrors the core expense-with-location flow). Money always moves from
	// a tracked location; there is no untracked option.
	err = appdb.WithTx(ctx, a.p.core.DB, func(tx *sqlx.Tx) error {
		e, err := a.p.core.Expenses.CreateInTx(ctx, tx, in, uid)
		if err != nil {
			return err
		}
		if _, err := a.p.core.Cashflow.MoveTx(ctx, tx, cashflow.MoveInput{
			From: loc, To: cashflow.External(), Amount: e.Amount, Reason: note,
			ReceiptKind: "expense", Ref: &cashflow.Ref{Kind: "expense", ID: e.ID}, ActorID: uid,
		}); err != nil {
			return err
		}
		return newStoreQ(tx).AddRepairerPayment(ctx, id, amount)
	})
	if err != nil {
		return err
	}
	a.p.core.Audit.Record(ctx, uid, audit.ActionCreate, "repair", strconv.FormatInt(id, 10), note)
	return a.redirectDetail(c, id)
}

func (a *adminUI) Cancel(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	d, err := a.p.store.GetJob(ctx, id)
	if err != nil {
		return apperr.NotFound("repair")
	}
	if d.Job.Status == "collected" {
		return apperr.Validation("a collected repair cannot be cancelled")
	}
	// Customer money lives at the cashier, so any deposit refund is handled there;
	// cancelling here just closes the job.
	if err := a.p.store.SetStatus(ctx, id, "cancelled", nil); err != nil {
		return err
	}
	a.p.core.Audit.Record(ctx, middleware.CurrentUserID(c), audit.ActionUpdate, "repair", strconv.FormatInt(id, 10), "cancelled repair")
	return a.redirectDetail(c, id)
}
func (a *adminUI) Report(c echo.Context) error {
	ctx := c.Request().Context()
	sym := a.symbol(c)
	// Shared preset/range resolver (today / this week / this month …), same as the
	// core reports; default to this month when nothing is chosen.
	preset := c.QueryParam("preset")
	if preset == "" && c.QueryParam("from") == "" && c.QueryParam("to") == "" {
		preset = "this-month"
	}
	from, to, fromStr, toStr, err := reports.ResolveRange(preset, c.QueryParam("from"), c.QueryParam("to"))
	if err != nil {
		return err
	}
	details, err := a.p.store.ListForReport(ctx, from, to)
	if err != nil {
		return err
	}
	rows := make([]ReportRow, 0, len(details))
	grand, grandParts, grandRepairer, grandProfit := decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
	for i := range details {
		d := &details[i]
		total, _, _ := JobTotals(d)
		parts := PartsCost(d)
		repairer := d.Job.RepairerPaid
		profit := total.Sub(parts).Sub(repairer)
		grand = grand.Add(total)
		grandParts = grandParts.Add(parts)
		grandRepairer = grandRepairer.Add(repairer)
		grandProfit = grandProfit.Add(profit)
		collected, warranty := "", ""
		if d.Job.CollectedAt != nil {
			collected = datetime.Date(*d.Job.CollectedAt)
		}
		if d.Job.WarrantyUntil != nil {
			warranty = datetime.Date(*d.Job.WarrantyUntil)
		}
		rows = append(rows, ReportRow{
			TicketNo: d.Job.TicketNo, Device: d.Job.DeviceModel, Collected: collected,
			Total: money.Format(sym, total), Parts: money.Format(sym, parts),
			Repairer: money.Format(sym, repairer), Profit: money.Format(sym, profit),
			Warranty: warranty,
		})
	}
	return response.RenderPage(c, RepairsReportPage(ReportData{
		UserName: middleware.CurrentUserName(c),
		Preset:   preset, FromLbl: fromStr, ToLbl: toStr,
		Rows: rows, GrandTot: money.Format(sym, grand),
		GrandParts: money.Format(sym, grandParts), GrandRepairer: money.Format(sym, grandRepairer),
		GrandProfit: money.Format(sym, grandProfit), Count: len(rows),
	}))
}

func (a *adminUI) Receipts(c echo.Context) error {
	preset := c.QueryParam("preset")
	if preset == "" && c.QueryParam("from") == "" && c.QueryParam("to") == "" {
		preset = "this-month"
	}
	from, to, fromStr, toStr, err := reports.ResolveRange(preset, c.QueryParam("from"), c.QueryParam("to"))
	if err != nil {
		return apperr.Validation(err.Error())
	}
	jobs, err := a.p.store.ListCollectedRange(c.Request().Context(), from, to)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, RepairsReceiptsTab(ReceiptsTabData{
		Symbol: a.symbol(c), BaseURL: "/admin/repairs", Jobs: jobs,
		Preset: preset, From: fromStr, To: toStr,
	}))
}

func (a *adminUI) RepairReceipt(c echo.Context) error { return a.p.renderReceipt(c) }

func (a *adminUI) ReadyCount(c echo.Context) error {
	n, err := a.p.store.CountReady(c.Request().Context())
	if err != nil {
		return err
	}
	return c.String(http.StatusOK, strconv.Itoa(n))
}
