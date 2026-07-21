package documents

import (
	"net/http"
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/recipes"
	"karots-pos/internal/response"

	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

type cashierUI struct{ p *Plugin }

// menuNode is one entry in the cashier menu-node protocol: a folder drills
// further in via ChildrenURL, an "amount" leaf opens an inline amount step
// that POSTs AddURL, and a "detail" leaf opens an inline HTML fragment at
// DetailURL. Mirrors plugins/recharge/cashier.go's menuNode — packages are
// separate so each plugin defines its own copy. See internal/plugin/hooks.go
// (CashierMenuRoot) for the root hook this subtree hangs off.
type menuNode struct {
	Kind        string         `json:"kind"`                   // "folder" | "leaf"
	Name        string         `json:"name"`
	Emoji       string         `json:"emoji,omitempty"`
	ChildrenURL string         `json:"children_url,omitempty"` // folder
	Action      string         `json:"action,omitempty"`       // leaf: "amount" | "detail"
	AddURL      string         `json:"add_url,omitempty"`      // amount leaf
	DetailURL   string         `json:"detail_url,omitempty"`   // detail leaf
	Meta        map[string]any `json:"meta,omitempty"`
}

// MenuRoot returns one detail leaf per active service for the cashier menu's
// "🖨 Documents" root card. Tapping a leaf opens JobFragment inline.
func (h *cashierUI) MenuRoot(c echo.Context) error {
	svcs, err := h.p.store.Services(c.Request().Context(), true)
	if err != nil {
		return err
	}
	nodes := make([]menuNode, 0, len(svcs))
	for _, s := range svcs {
		nodes = append(nodes, menuNode{
			Kind: "leaf", Name: s.Name, Emoji: "🖨", Action: "detail",
			DetailURL: "/cashier/documents/job?service=" + strconv.FormatInt(s.ID, 10),
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"nodes": nodes})
}

// JobFragment renders one service's job form (the old JobPanel's metered/custom
// builder, minus the service-list/pick step) as an inline cashier-menu detail
// fragment for GET /cashier/documents/job?service=ID.
func (h *cashierUI) JobFragment(c echo.Context) error {
	ctx := c.Request().Context()
	sid, err := strconv.ParseInt(c.QueryParam("service"), 10, 64)
	if err != nil || sid <= 0 {
		return apperr.BadRequest("invalid service id")
	}
	sv, err := h.p.store.ServiceByID(ctx, sid)
	if err != nil {
		return err
	}
	if sv == nil || !sv.IsActive {
		return apperr.NotFound("service")
	}
	return response.RenderFragment(c, JobFragment(*sv))
}

// Services lists active services for the quick-action panel.
func (h *cashierUI) Services(c echo.Context) error {
	rows, err := h.p.store.Services(c.Request().Context(), true)
	if err != nil {
		return err
	}
	return response.OK(c, rows)
}

// PriceRows returns a service's price matrix so the panel can offer the right
// size / colour / side buttons.
func (h *cashierUI) PriceRows(c echo.Context) error {
	sid, _ := strconv.ParseInt(c.QueryParam("service"), 10, 64)
	rows, err := h.p.store.Prices(c.Request().Context(), sid)
	if err != nil {
		return err
	}
	return response.OK(c, rows)
}

// Quote resolves the tiered unit price, line total, descriptive label and the
// consumables a metered job will draw down.
func (h *cashierUI) Quote(c echo.Context) error {
	ctx := c.Request().Context()
	sid, _ := strconv.ParseInt(c.QueryParam("service"), 10, 64)
	sv, err := h.p.store.ServiceByID(ctx, sid)
	if err != nil {
		return err
	}
	if sv == nil {
		return apperr.NotFound("service")
	}
	size := c.QueryParam("size")
	color := truthy(c.QueryParam("color"))
	double := truthy(c.QueryParam("double"))
	qty, _ := strconv.Atoi(c.QueryParam("qty"))
	if qty <= 0 {
		return apperr.Validation("quantity must be greater than zero")
	}
	unit, found, err := h.p.store.ResolveUnitPrice(ctx, sid, size, color, double, qty)
	if err != nil {
		return err
	}
	if !found {
		return apperr.Validation("no price is set for that size/option")
	}
	qd := decimal.NewFromInt(int64(qty))
	lineTotal := unit.Mul(qd).Round(2)

	// Paper sheets: a double-sided copy puts 2 impressions on 1 sheet.
	baseUnits := qty
	if double {
		baseUnits = (qty + 1) / 2
	}
	cs, _ := h.p.store.ConsumablesFor(ctx, sid, size)
	comps := make([]map[string]any, 0, len(cs))
	consCost := decimal.Zero
	base := decimal.NewFromInt(int64(baseUnits))
	// Anything the plugin already resolved for this size wins over a core recipe
	// naming the same product. The two lists are independent, so without this an
	// item listed in both would be deducted and charged twice on every job —
	// silently, because both numbers look plausible on their own.
	fromPlugin := make(map[int64]bool, len(cs))
	for _, cm := range cs {
		fromPlugin[cm.ProductID] = true
	}
	for _, cm := range cs {
		// Paper is a whole unit — a single copy uses a whole sheet. This used to
		// Ceil() every component, so a yield-based one (a toner rated for 5000
		// copies) consumed an entire cartridge on a one-copy job.
		comp := recipes.Component{
			ComponentProductID: cm.ProductID,
			QtyPerUnit:         decimal.NullDecimal{Decimal: cm.QtyPerUnit, Valid: true},
			WholeUnits:         true,
		}
		consumed := comp.Consumed(base)
		comps = append(comps, map[string]any{"product_id": cm.ProductID, "quantity": consumed.String()})
		consCost = consCost.Add(consumed.Mul(h.p.store.ConsumableCost(ctx, cm.ProductID)))
	}

	// Size-agnostic ingredients now live in core recipes (toner, ink) and are
	// fractional, so they are expanded without rounding up.
	core, _ := recipes.NewRepository(h.p.core.DB).For(ctx, sv.ProductID)
	for _, cons := range recipes.Expand(core, base) {
		if fromPlugin[cons.ProductID] {
			continue
		}
		comps = append(comps, map[string]any{"product_id": cons.ProductID, "quantity": cons.Qty.String()})
		consCost = consCost.Add(cons.Qty.Mul(h.p.store.ConsumableCost(ctx, cons.ProductID)))
	}

	return response.OK(c, map[string]any{
		"product_id":      sv.ProductID,
		"unit_price":      unit.String(),
		"line_total":      lineTotal.String(),
		"description":     buildDesc(sv.Name, size, color, double, qty),
		"components":      comps,
		"consumable_cost": consCost.Round(2).String(),
	})
}

// RecordInput is the post-checkout payload: the sale id + the document jobs on it.
type RecordInput struct {
	SaleID int64 `json:"sale_id"`
	Jobs   []struct {
		ServiceID      *int64 `json:"service_id"`
		Description    string `json:"description"`
		Qty            string `json:"qty"`
		UnitPrice      string `json:"unit_price"`
		LineTotal      string `json:"line_total"`
		ConsumableCost string `json:"consumable_cost"`
	} `json:"jobs"`
}

// Record writes the doc_job ledger rows for a completed sale (analytics + labour).
// Stock depletion already happened in the sale tx via the core consume-on-sale seam.
func (h *cashierUI) Record(c echo.Context) error {
	ctx := c.Request().Context()
	var in RecordInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if in.SaleID <= 0 {
		return apperr.BadRequest("missing sale id")
	}
	sid := in.SaleID
	for _, j := range in.Jobs {
		if err := h.p.store.InsertJob(ctx, Job{
			SaleID:         &sid,
			ServiceID:      j.ServiceID,
			Description:    j.Description,
			Qty:            dec(j.Qty),
			UnitPrice:      dec(j.UnitPrice),
			LineTotal:      dec(j.LineTotal),
			ConsumableCost: dec(j.ConsumableCost),
		}); err != nil {
			return err
		}
	}
	return response.OK(c, map[string]any{"ok": true})
}

// --- helpers ---

func truthy(s string) bool { return s == "1" || s == "true" || s == "on" }

func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(strings.TrimSpace(s))
	if err != nil {
		return decimal.Zero
	}
	return d
}

// buildDesc renders a job's receipt label, e.g. "Photocopy A4 colour 2-side ×20".
func buildDesc(name, size string, color, double bool, qty int) string {
	parts := []string{name}
	if size != "" {
		parts = append(parts, size)
	}
	if color {
		parts = append(parts, "colour")
	} else {
		parts = append(parts, "B&W")
	}
	if double {
		parts = append(parts, "2-side")
	} else {
		parts = append(parts, "1-side")
	}
	return strings.Join(parts, " ") + " ×" + strconv.Itoa(qty)
}
