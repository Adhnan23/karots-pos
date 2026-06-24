package documents

import (
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/internal/response"

	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

type cashierUI struct{ p *Plugin }

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
	for _, cm := range cs {
		consumed := cm.QtyPerUnit.Mul(decimal.NewFromInt(int64(baseUnits))).Ceil()
		comps = append(comps, map[string]any{"product_id": cm.ProductID, "quantity": consumed.String()})
		consCost = consCost.Add(consumed.Mul(h.p.store.ConsumableCost(ctx, cm.ProductID)))
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
