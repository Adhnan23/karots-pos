package repairs

import (
	"net/http"
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/cashregister"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"

	"github.com/labstack/echo/v4"
)

type cashierUI struct{ p *Plugin }

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
	}}
	jobs, err := h.p.store.ListByStatuses(ctx, []string{"received", "in_progress", "ready"})
	if err != nil {
		return err
	}
	for _, j := range jobs {
		label := j.TicketNo + " — " + j.DeviceModel + " (" + j.Status + ")"
		nodes = append(nodes, menuNode{
			Kind: "leaf", Name: label, Emoji: "🔧", Action: "detail",
			DetailURL: "/cashier/repairs/" + strconv.FormatInt(j.ID, 10),
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"nodes": nodes})
}

func (h *cashierUI) ApplyForm(c echo.Context) error {
	ctx := c.Request().Context()
	types, _ := h.p.store.DistinctTypes(ctx)
	models, _ := h.p.store.DistinctModels(ctx)
	repairers, _ := h.p.store.DistinctRepairers(ctx)
	return response.RenderFragment(c, RepairApplyFragment(FormData{
		Types: types, Models: models, Repairers: repairers, WarrantyDays: h.p.defWarranty,
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
	ctx := c.Request().Context()
	d, err := h.p.store.GetJob(ctx, id)
	if err != nil {
		return apperr.NotFound("repair")
	}
	sym := h.symbol(c)
	total, dep, bal := JobTotals(d)
	return response.RenderFragment(c, RepairCashierJob(DetailData{
		Symbol:   sym,
		D:        d,
		Total:    money.Format(sym, total),
		Deposit:  money.Format(sym, dep),
		Balance:  money.Format(sym, bal),
		Editable: d.Job.Status != "collected" && d.Job.Status != "cancelled",
	}))
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
	if err := h.p.store.AddPart(ctx, id, PartInput{
		ProductID: pid, Qty: qty, UnitCharge: prod.SellingPrice,
		DiscountType: c.FormValue("discount_type"), DiscountValue: dval,
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
		return err
	}
	if err := h.p.store.AddPayment(ctx, id, amount, "deposit", uid); err != nil {
		return err
	}
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
	if err := h.p.collect(ctx, id, c.FormValue("pay_method"), c.FormValue("pay_now"), middleware.CurrentUserID(c)); err != nil {
		return err
	}
	d, err := h.p.store.GetJob(ctx, id)
	if err != nil {
		return apperr.NotFound("repair")
	}
	return response.RenderFragment(c, RepairCollected(d.Job))
}

// ---- filled in Task 9 (stubs keep routes live) ----

func (h *cashierUI) Receipts(c echo.Context) error      { return c.NoContent(http.StatusOK) }
func (h *cashierUI) RepairReceipt(c echo.Context) error { return c.NoContent(http.StatusOK) }
