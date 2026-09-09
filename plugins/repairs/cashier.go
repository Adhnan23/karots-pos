package repairs

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/cashregister"
	"karots-pos/internal/features/reports"
	"karots-pos/internal/features/sales"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

type cashierUI struct{ p *Plugin }

// errMsg is a short, human message from an error for an inline warning banner.
func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (h *cashierUI) symbol(c echo.Context) string {
	if sc, err := h.p.core.Settings.Get(c.Request().Context()); err == nil && sc != nil && sc.CurrencySymbol != "" {
		return sc.CurrencySymbol
	}
	return "Rs."
}

// menuNode mirrors the cashier menu-node protocol (see plugins/documents).
type menuNode struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Emoji     string `json:"emoji,omitempty"`
	Action    string `json:"action,omitempty"`
	DetailURL string `json:"detail_url,omitempty"`
}

// MenuRoot lists "Apply a repair" + each open job as inline detail leaves.
func (h *cashierUI) MenuRoot(c echo.Context) error {
	ctx := c.Request().Context()
	nodes := []menuNode{{
		Kind: "leaf", Name: "Apply a repair", Emoji: "➕", Action: "detail",
		DetailURL: "/cashier/repairs/apply",
	}, {
		Kind: "leaf", Name: "Find / scan a ticket", Emoji: "🔍", Action: "detail",
		DetailURL: "/cashier/repairs/find",
	}}
	jobs, err := h.p.store.ListByStatuses(ctx, []string{"received", "in_progress", "ready"})
	if err != nil {
		return err
	}
	for _, j := range jobs {
		label := j.TicketNo + " — " + j.DeviceModel + " (" + j.Status + ")"
		emoji := "🔧"
		// Urgent/overdue jobs jump out in the till list: red emoji + the countdown.
		if due, urgent := jobDue(j.Status, j.PromisedDate, j.Urgent); urgent {
			emoji = "🔴"
			label += " · " + due
		}
		nodes = append(nodes, menuNode{
			Kind: "leaf", Name: label, Emoji: emoji, Action: "detail",
			DetailURL: "/cashier/repairs/" + strconv.FormatInt(j.ID, 10),
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"nodes": nodes})
}

// FindForm renders the scan/search panel: a box the cashier types into or a
// scanner fills from the ticket barcode, listing matching jobs to open.
func (h *cashierUI) FindForm(c echo.Context) error {
	return response.RenderFragment(c, RepairFindFragment())
}

// SearchJobs returns the matching-jobs list for the scan/search box.
func (h *cashierUI) SearchJobs(c echo.Context) error {
	jobs, err := h.p.store.Search(c.Request().Context(), c.QueryParam("q"), 25)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, RepairFindResults(jobs))
}

func (h *cashierUI) ApplyForm(c echo.Context) error {
	ctx := c.Request().Context()
	types, _ := h.p.store.DistinctTypes(ctx)
	models, _ := h.p.store.DistinctModels(ctx)
	repairers, _ := h.p.store.DistinctRepairers(ctx)
	return response.RenderFragment(c, RepairApplyFragment(FormData{
		Types: types, Models: models, Repairers: repairers, WarrantyDays: h.p.defWarranty,
		WarrantyMode: h.p.warrantyMode,
	}))
}

func (h *cashierUI) Create(c echo.Context) error {
	ctx := c.Request().Context()
	in := jobInputFromForm(c, h.p.defWarranty)
	if in.DeviceModel == "" && in.RepairType == "" {
		return apperr.Validation("enter at least a repair type or device model")
	}
	id, err := h.p.store.CreateJob(ctx, in)
	if err != nil {
		return err
	}
	return h.renderJob(c, id)
}

func (h *cashierUI) Detail(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	return h.renderJob(c, id)
}

// renderJob loads a job and returns the cashier job fragment.
func (h *cashierUI) renderJob(c echo.Context, id int64) error {
	return h.renderJobWarn(c, id, "")
}

// renderJobWarn is renderJob with an inline warning banner (e.g. "open your till
// first"), so a failed deposit/collect keeps the panel instead of swapping in a
// raw error page.
func (h *cashierUI) renderJobWarn(c echo.Context, id int64, warn string) error {
	ctx := c.Request().Context()
	d, err := h.p.store.GetJob(ctx, id)
	if err != nil {
		return apperr.NotFound("repair")
	}
	sym := h.symbol(c)
	total, dep, bal := JobTotals(d)
	dueLabel, urgent := jobDue(d.Job.Status, d.Job.PromisedDate, d.Job.Urgent)
	return response.RenderFragment(c, RepairCashierJob(DetailData{
		Symbol:   sym,
		D:        d,
		Total:    money.Format(sym, total),
		Deposit:  money.Format(sym, dep),
		Balance:       money.Format(sym, bal),
		DueLabel:      dueLabel,
		Urgent:        urgent,
		PromisedInput: promisedInput(d.Job.PromisedDate),
		CanRework:     canRework(d.Job),
		RefundLines:   h.refundLines(ctx, sym, d),
		WarrantyMode:  h.p.warrantyMode,
		Editable:      d.Job.Status != "collected" && d.Job.Status != "cancelled",
		Warning:       warn,
	}))
}

// WarrantyRework opens a free re-repair from a collected, in-warranty job and
// shows the new job panel to add the redo work.
func (h *cashierUI) WarrantyRework(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	newID, err := h.p.startRework(ctx, id, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	return h.renderJob(c, newID)
}

// refundLines lists the collection sale's part lines that can still be sent back
// under warranty (labour/charges are is_service and never returnable). Empty
// unless the job is collected and still in warranty — so the picker only appears
// where a warranty part refund is valid.
func (h *cashierUI) refundLines(ctx context.Context, sym string, d *Detail) []RefundLine {
	if !canRework(d.Job) || d.Job.SaleID == nil {
		return nil
	}
	sale, err := h.p.core.Sales.Get(ctx, *d.Job.SaleID)
	if err != nil {
		return nil
	}
	var out []RefundLine
	for _, it := range sale.Items {
		if it.IsService || it.ProductID == h.p.labourProdID || !it.ReturnableQty().IsPositive() {
			continue
		}
		out = append(out, RefundLine{
			SaleItemID: it.ID,
			Name:       it.ProductName,
			Price:      money.Format(sym, it.Subtotal),
		})
	}
	return out
}

// WarrantyRefund takes a failed part back under warranty: it refunds ONLY the
// part price (labour is kept) on the collection sale and books the returned part
// as a damage loss (Losses & Recovery) — the credit portion reduces the
// customer's balance, the cash portion leaves the till. One transaction.
func (h *cashierUI) WarrantyRefund(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	userID := middleware.CurrentUserID(c)

	d, err := h.p.store.GetJob(ctx, id)
	if err != nil {
		return apperr.NotFound("repair")
	}
	if !canRework(d.Job) || d.Job.SaleID == nil {
		return apperr.Validation("only a collected, in-warranty repair can be refunded under warranty")
	}

	// Refundable part lines keyed to their remaining qty, so a posted id can only
	// ever be a real part of THIS sale (never labour, a charge, or another sale's
	// line) and the full remaining qty of that part is what goes back.
	sale, serr := h.p.core.Sales.Get(ctx, *d.Job.SaleID)
	if serr != nil {
		return serr
	}
	refundableQty := map[int64]string{}
	for _, it := range sale.Items {
		if it.IsService || it.ProductID == h.p.labourProdID || !it.ReturnableQty().IsPositive() {
			continue
		}
		refundableQty[it.ID] = it.ReturnableQty().String()
	}
	if err := c.Request().ParseForm(); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	var lines []sales.ReturnLineInput
	for _, v := range c.Request().PostForm["item"] {
		itemID, perr := strconv.ParseInt(v, 10, 64)
		qty, ok := refundableQty[itemID]
		if perr != nil || !ok {
			continue
		}
		lines = append(lines, sales.ReturnLineInput{SaleItemID: itemID, Quantity: qty, Disposition: "damage"})
	}
	if len(lines) == 0 {
		return h.renderJobWarn(c, id, "select at least one part to refund")
	}
	reason := "warranty part refund — " + d.Job.TicketNo
	in := sales.PartialReturnInput{Reason: &reason, Lines: lines}

	err = appdb.WithTx(ctx, h.p.core.DB, func(tx *sqlx.Tx) error {
		detail, cashRefund, returnID, terr := h.p.core.Sales.PartialReturnTx(ctx, tx, *d.Job.SaleID, in, userID)
		if terr != nil {
			return terr
		}
		if cashRefund.IsPositive() {
			party := ""
			if detail.Sale.CustomerName != nil {
				party = *detail.Sale.CustomerName
			}
			if _, terr := h.p.core.Cashflow.MoveTx(ctx, tx, cashflow.MoveInput{
				From: cashflow.Till(userID), To: cashflow.External(), Amount: cashRefund,
				Reason: reason, ReceiptKind: "refund", Party: party,
				Ref: &cashflow.Ref{Kind: "sale_return", ID: returnID}, ActorID: userID,
			}); terr != nil {
				return terr
			}
		}
		return nil
	})
	if err != nil {
		return h.renderJobWarn(c, id, errMsg(err))
	}
	h.p.core.Audit.Record(ctx, userID, audit.ActionReturn, "repair",
		strconv.FormatInt(id, 10), reason)
	return h.renderJobWarn(c, id, "Refunded the returned part(s) — labour charge kept.")
}

// SetUrgent lets the cashier flag/unflag a job urgent at the counter (a customer
// asking to rush) — customer-facing and harmless, so not admin-only.
func (h *cashierUI) SetUrgent(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	newP := parseOptDate(c.FormValue("promised_date"))
	old, _ := h.p.store.GetJob(ctx, id)
	if err := h.p.store.SetUrgent(ctx, id, c.FormValue("urgent") == "1", newP); err != nil {
		return err
	}
	if old != nil {
		h.p.auditDeadline(ctx, middleware.CurrentUserID(c), id, old.Job.PromisedDate, newP, old.Job.TicketNo)
	}
	return h.renderJob(c, id)
}

// RemovePart / RemoveCharge let the cashier take a mistaken line off a job.
func (h *cashierUI) RemovePart(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	pid, err := strconv.ParseInt(c.Param("pid"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := h.p.store.RemovePart(c.Request().Context(), pid); err != nil {
		return err
	}
	return h.renderJob(c, id)
}

func (h *cashierUI) RemoveCharge(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	cid, err := strconv.ParseInt(c.Param("cid"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := h.p.store.RemoveCharge(c.Request().Context(), cid); err != nil {
		return err
	}
	return h.renderJob(c, id)
}

func (h *cashierUI) AddPart(c echo.Context) error {
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
	prod, err := h.p.core.Products.Get(ctx, pid)
	if err != nil {
		return apperr.NotFound("product")
	}
	dval, _ := money.Parse(c.FormValue("discount"))
	wd, _ := strconv.Atoi(strings.TrimSpace(c.FormValue("warranty_days")))
	if err := h.p.store.AddPart(ctx, id, PartInput{
		ProductID: pid, Qty: qty, UnitCharge: prod.SellingPrice,
		DiscountType: c.FormValue("discount_type"), DiscountValue: dval, WarrantyDays: wd,
	}); err != nil {
		return err
	}
	return h.renderJob(c, id)
}

func (h *cashierUI) AddCharge(c echo.Context) error {
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
	if err := h.p.store.AddCharge(ctx, id, label, amount); err != nil {
		return err
	}
	return h.renderJob(c, id)
}

// TakeDeposit takes a customer advance into the cashier's open till.
func (h *cashierUI) TakeDeposit(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	amount, err := money.Parse(c.FormValue("amount"))
	if err != nil || !amount.IsPositive() {
		return apperr.Validation("deposit must be greater than zero")
	}
	uid := middleware.CurrentUserID(c)
	job, err := h.p.store.GetJob(ctx, id)
	if err != nil {
		return apperr.NotFound("repair")
	}
	if _, err := h.p.core.CashRegister.PayIn(ctx, uid, cashregister.MovementInput{
		Amount: amount.StringFixed(2), Reason: "Repair " + job.Job.TicketNo + " deposit",
	}); err != nil {
		return h.renderJobWarn(c, id, "Couldn't take the deposit: "+errMsg(err)+" Open your till first, then try again.")
	}
	if err := h.p.store.AddPayment(ctx, id, amount, "deposit", uid); err != nil {
		return err
	}
	// Deposit slip follows the shop's print policy, same as every core receipt:
	// "ask to print" ON → the shared Print/Skip prompt (drawer pops now for cash,
	// slip prints on click); OFF → auto-print best-effort with the kick folded in.
	// Either way, nudge the POS to refresh its "expected cash" widget in place —
	// the deposit just added cash to the open till.
	cfg, _ := h.p.core.Settings.Get(ctx)
	if cfg != nil && cfg.AskToPrint {
		h.p.kickDrawer(ctx, uid) // prompt mode: pop the drawer now; slip prints on click
		printURL := "/cashier/repairs/" + strconv.FormatInt(id, 10) + "/print?kind=advance&amount=" + amount.StringFixed(2)
		c.Response().Header().Set("HX-Trigger", response.PrintPrompt("Deposit received", printURL, false, "pos-refresh-summary"))
		return h.renderJob(c, id)
	}
	if d, gerr := h.p.store.GetJob(ctx, id); gerr == nil {
		h.p.printAdvanceSlip(ctx, uid, d, amount)
	}
	c.Response().Header().Set("HX-Trigger", "pos-refresh-summary")
	return h.renderJob(c, id)
}

// Collect rings the settling sale (deposit as wallet tender + balance), then
// shows the collected panel with a link to the repair receipt.
func (h *cashierUI) Collect(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	uid := middleware.CurrentUserID(c)
	cashPaid, onAccount, err := h.p.collect(ctx, id, c.FormValue("pay_method"), c.FormValue("pay_now"), uid)
	if err != nil {
		return h.renderJobWarn(c, id, "Couldn't collect: "+errMsg(err))
	}
	if cashPaid.IsPositive() {
		h.p.kickDrawer(ctx, uid) // cash landed in the drawer on completion
	}
	d, err := h.p.store.GetJob(ctx, id)
	if err != nil {
		return apperr.NotFound("repair")
	}
	// The settling sale moved cash into the till — refresh the POS expected-cash
	// widget in place (same reason as a deposit).
	c.Response().Header().Set("HX-Trigger", "pos-refresh-summary")
	left := ""
	if onAccount.IsPositive() {
		left = money.Format(h.symbol(c), onAccount)
	}
	return response.RenderFragment(c, RepairCollected(d.Job, left))
}

func (h *cashierUI) Receipts(c echo.Context) error {
	preset := c.QueryParam("preset")
	if preset == "" && c.QueryParam("from") == "" && c.QueryParam("to") == "" {
		preset = "this-month"
	}
	from, to, fromStr, toStr, err := reports.ResolveRange(preset, c.QueryParam("from"), c.QueryParam("to"))
	if err != nil {
		return apperr.Validation(err.Error())
	}
	jobs, err := h.p.store.ListCollectedRange(c.Request().Context(), from, to)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, RepairsReceiptsTab(ReceiptsTabData{
		Symbol: h.symbol(c), BaseURL: "/cashier/repairs", Jobs: jobs,
		Preset: preset, From: fromStr, To: toStr,
	}))
}

func (h *cashierUI) RepairReceipt(c echo.Context) error { return h.p.renderReceipt(c) }
