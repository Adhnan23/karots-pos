package repairs

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"

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

// jobInputFromForm reads the shared job fields from a POST form.
func jobInputFromForm(c echo.Context, defWarranty int) JobInput {
	in := JobInput{
		CustomerName:  strings.TrimSpace(c.FormValue("customer_name")),
		CustomerPhone: strings.TrimSpace(c.FormValue("customer_phone")),
		RepairType:    strings.TrimSpace(c.FormValue("repair_type")),
		DeviceModel:   strings.TrimSpace(c.FormValue("device_model")),
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
	jobs, err := a.p.store.ListByStatuses(ctx, []string{"received", "in_progress", "ready"})
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
		Rows:     rows,
	}))
}

func (a *adminUI) NewForm(c echo.Context) error {
	ctx := c.Request().Context()
	types, _ := a.p.store.DistinctTypes(ctx)
	models, _ := a.p.store.DistinctModels(ctx)
	return response.RenderPage(c, RepairFormPage(FormData{
		UserName:     middleware.CurrentUserName(c),
		Types:        types,
		Models:       models,
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
		DueLabel:      label,
		Urgent:        urgent || d.Job.Urgent,
		WarrantyLabel: warranty,
		Editable:      d.Job.Status != "collected" && d.Job.Status != "cancelled",
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

// ---- filled in later tasks (stubs keep routes live) ----

func (a *adminUI) TakeDeposit(c echo.Context) error   { return c.NoContent(http.StatusOK) }
func (a *adminUI) Collect(c echo.Context) error       { return c.NoContent(http.StatusOK) }
func (a *adminUI) Cancel(c echo.Context) error        { return c.NoContent(http.StatusOK) }
func (a *adminUI) Report(c echo.Context) error        { return c.NoContent(http.StatusOK) }
func (a *adminUI) Receipts(c echo.Context) error      { return c.NoContent(http.StatusOK) }
func (a *adminUI) RepairReceipt(c echo.Context) error { return c.NoContent(http.StatusOK) }
