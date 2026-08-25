package alternatives

import (
	"encoding/csv"
	"strconv"

	"karots-pos/internal/features/products"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"

	"github.com/labstack/echo/v4"
)

// Reorder is the reorder-by-alternatives report: per group, each tier's summed
// on-hand qty vs its reorder level, plus low-stock items that belong to no group.
func (a *adminUI) Reorder(c echo.Context) error {
	rollups, ungrouped, err := a.reorderData(c)
	if err != nil {
		return err
	}
	return response.RenderPage(c, ReorderPage(middleware.CurrentUserName(c), rollups, ungrouped))
}

func (a *adminUI) reorderData(c echo.Context) ([]GroupRollup, []products.Product, error) {
	ctx := c.Request().Context()
	rollups, err := a.p.store.Reorder(ctx)
	if err != nil {
		return nil, nil, err
	}
	low, _, err := a.p.core.Products.List(ctx, products.ListQuery{LowStock: true, Page: 1, Limit: 500})
	if err != nil {
		return nil, nil, err
	}
	grouped, err := a.p.store.AllMemberIDs(ctx)
	if err != nil {
		return nil, nil, err
	}
	inGroup := make(map[int64]bool, len(grouped))
	for _, id := range grouped {
		inGroup[id] = true
	}
	var ungrouped []products.Product
	for _, p := range low {
		if !inGroup[p.ID] {
			ungrouped = append(ungrouped, p)
		}
	}
	return rollups, ungrouped, nil
}

// ReorderCSV streams the tier rollups as CSV.
func (a *adminUI) ReorderCSV(c echo.Context) error {
	rollups, _, err := a.reorderData(c)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Content-Type", "text/csv")
	c.Response().Header().Set("Content-Disposition", `attachment; filename="reorder-alternatives.csv"`)
	w := csv.NewWriter(c.Response())
	_ = w.Write([]string{"group", "tier", "total_qty", "reorder_level", "low"})
	for _, g := range rollups {
		for _, t := range g.Tiers {
			low := ""
			if t.Low {
				low = "LOW"
			}
			_ = w.Write([]string{
				g.Group.Name, t.Tier.Name,
				strconv.Itoa(t.TotalQty), strconv.Itoa(t.Tier.ReorderLevel), low,
			})
		}
	}
	w.Flush()
	return w.Error()
}
