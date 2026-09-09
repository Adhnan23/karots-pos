package repairs

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"karots-pos/internal/apperr"
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
// pay-repairer picker, mirroring the core expense/bill-pay location choices.
func (a *adminUI) cashLocations(ctx context.Context) []LocChoice {
	var out []LocChoice
	if lks, err := a.p.core.Lockers.List(ctx, true); err == nil {
		for _, l := range lks {
			out = append(out, LocChoice{Value: "locker:" + strconv.FormatInt(l.ID, 10), Label: l.Name, Group: "Lockers"})
		}
	}
	if tills, err := a.p.core.CashRegister.OpenSessions(ctx); err == nil {
		for _, t := range tills {
			out = append(out, LocChoice{Value: "till:" + strconv.FormatInt(t.UserID, 10), Label: "Till — " + t.UserName, Group: "Tills"})
		}
	}
	return out
}

// parseCashLocation turns a picker value ("locker:ID" / "till:UID" / "external")
// into a cashflow endpoint — same encoding as the core location picker.
func parseCashLocation(v string) (cashflow.Location, bool, error) {
	v = strings.TrimSpace(v)
	if v == "" || v == "external" {
		return cashflow.Location{}, false, nil // untracked: expense only, no cash move
	}
	kind, idStr, ok := strings.Cut(v, ":")
	if !ok {
		return cashflow.Location{}, false, apperr.Validation("invalid cash location")
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return cashflow.Location{}, false, apperr.Validation("invalid cash location")
	}
	switch kind {
	case "locker":
		return cashflow.Locker(id), true, nil
	case "till":
		return cashflow.Till(id), true, nil
	}
	return cashflow.Location{}, false, apperr.Validation("invalid cash location")
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
	jobs, err := a.p.store.ListByStatuses(ctx, statuses)
	if err != nil {
		return err
	}
	rows := make([]ListRow, 0, len(jobs))
	for _, j := range jobs {
		label, urgent := dueLabel(j.PromisedDate)
		rows = append(rows, ListRow{
			ID: j.ID, TicketNo: j.TicketNo, Customer: j.CustomerName, Phone: j.CustomerPhone,
			Device: j.DeviceModel, RepairType: j.RepairType, Status: j.Status,
			DueLabel: label, Urgent: urgent || j.Urgent,
		})
	}
	return response.RenderPage(c, RepairsListPage(ListData{
		UserName: middleware.CurrentUserName(c),
		Show:     show,
		Rows:     rows,
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
	label, urgent := dueLabel(d.Job.PromisedDate)
	warranty := ""
	if d.Job.WarrantyUntil != nil {
		warranty = d.Job.WarrantyUntil.Format("2006-01-02")
	} else if d.Job.WarrantyDays > 0 {
		warranty = fmt.Sprintf("%d days from pickup", d.Job.WarrantyDays)
	}
	return DetailData{
		UserName:      middleware.CurrentUserName(c),
		Symbol:        sym,
		D:             d,
		Total:         money.Format(sym, total),
		Deposit:       money.Format(sym, dep),
		Balance:       money.Format(sym, bal),
		RepairerPaid:  money.Format(sym, d.Job.RepairerPaid),
		DueLabel:      label,
		Urgent:        urgent || d.Job.Urgent,
		WarrantyLabel: warranty,
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
	if err := a.p.store.UpdateJobFields(ctx, id, jobInputFromForm(c, a.p.defWarranty)); err != nil {
		return err
	}
	return c.Redirect(http.StatusSeeOther, "/admin/repairs/"+strconv.FormatInt(id, 10))
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
	if err := a.p.store.AddPart(ctx, id, PartInput{
		ProductID: pid, Qty: qty, UnitCharge: prod.SellingPrice,
		DiscountType: c.FormValue("discount_type"), DiscountValue: dval,
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
	return c.Redirect(http.StatusSeeOther, "/admin/repairs/"+strconv.FormatInt(id, 10))
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
	loc, tracked, err := parseCashLocation(c.FormValue("source"))
	if err != nil {
		return err
	}
	in := expenses.CreateInput{
		Category: "Repairs", Amount: amount.StringFixed(2), Description: &note,
		ExpenseDate: time.Now().Format("2006-01-02"),
	}
	// Book the expense, debit the chosen cash location, and stamp the job — all in
	// one tx (mirrors the core expense-with-location flow). An untracked source
	// books the expense only, no cash move.
	err = appdb.WithTx(ctx, a.p.core.DB, func(tx *sqlx.Tx) error {
		e, err := a.p.core.Expenses.CreateInTx(ctx, tx, in, uid)
		if err != nil {
			return err
		}
		if tracked {
			if _, err := a.p.core.Cashflow.MoveTx(ctx, tx, cashflow.MoveInput{
				From: loc, To: cashflow.External(), Amount: e.Amount, Reason: note,
				ReceiptKind: "expense", Ref: &cashflow.Ref{Kind: "expense", ID: e.ID}, ActorID: uid,
			}); err != nil {
				return err
			}
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
	grand := decimal.Zero
	for i := range details {
		d := &details[i]
		total, _, _ := JobTotals(d)
		grand = grand.Add(total)
		collected, warranty := "", ""
		if d.Job.CollectedAt != nil {
			collected = d.Job.CollectedAt.Format("2006-01-02")
		}
		if d.Job.WarrantyUntil != nil {
			warranty = d.Job.WarrantyUntil.Format("2006-01-02")
		}
		rows = append(rows, ReportRow{
			TicketNo: d.Job.TicketNo, Device: d.Job.DeviceModel, Collected: collected,
			Total: money.Format(sym, total), Warranty: warranty,
		})
	}
	return response.RenderPage(c, RepairsReportPage(ReportData{
		UserName: middleware.CurrentUserName(c),
		Preset:   preset, FromLbl: fromStr, ToLbl: toStr,
		Rows: rows, GrandTot: money.Format(sym, grand), Count: len(rows),
	}))
}

func (a *adminUI) Receipts(c echo.Context) error {
	jobs, err := a.p.store.ListCollected(c.Request().Context(), 100)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, RepairsReceiptsTab(ReceiptsTabData{
		Symbol: a.symbol(c), BaseURL: "/admin/repairs", Jobs: jobs,
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
