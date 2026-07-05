package web

import (
	"fmt"
	"strings"

	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/stock"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"
	adminfragments "karots-pos/templates/fragments/admin"

	"github.com/labstack/echo/v4"
)

// stockTakeColumns is the CSV layout for the count-sheet download and upload.
var stockTakeColumns = []string{"barcode", "name", "current_qty", "counted_qty", "cost", "selling_price", "wholesale_price"}

// stockTakeSynonyms maps alternative headers to canonical stock-take columns.
var stockTakeSynonyms = map[string]string{
	"code": "barcode", "sku": "barcode",
	"product": "name", "item": "name",
	"on hand": "current_qty", "current": "current_qty", "system qty": "current_qty",
	"count": "counted_qty", "counted": "counted_qty", "qty": "counted_qty",
	"quantity": "counted_qty", "physical": "counted_qty", "actual": "counted_qty",
	"cost price": "cost", "cost_price": "cost", "buying price": "cost",
	"selling": "selling_price", "selling price": "selling_price", "sell price": "selling_price",
	"retail": "selling_price", "retail price": "selling_price", "price": "selling_price",
	"wholesale": "wholesale_price", "wholesale price": "wholesale_price", "trade price": "wholesale_price",
}

func stockTakeImportConfig() adminfragments.ImportConfig {
	return adminfragments.ImportConfig{
		Title:       "Import Stock Count",
		Columns:     strings.Join(stockTakeColumns, ", "),
		PostURL:     "/admin/stock/take/import",
		TemplateURL: "/admin/stock/take/sheet",
		Help: []string{
			"Download the count sheet, fill the <b>counted_qty</b> column, then upload it here.",
			"Rows are matched by <b>barcode</b>; a blank counted_qty is left unchanged.",
			"Each change is an audited stock adjustment (not a purchase). Optional <b>cost</b>, <b>selling_price</b> and <b>wholesale_price</b> value the stock and set its prices.",
		},
	}
}

// StockTakeImportModal returns the CSV upload dialog for stock-take.
func (a *adminUI) StockTakeImportModal(c echo.Context) error {
	return response.RenderFragment(c, adminfragments.ImportModal(stockTakeImportConfig()))
}

// StockTakeSheet streams a count sheet: every active product with its current
// on-hand quantity and cost, and a blank counted_qty column to fill in.
func (a *adminUI) StockTakeSheet(c echo.Context) error {
	ctx := c.Request().Context()
	rows, _, err := a.s.products.List(ctx, products.ListQuery{Limit: 100000, Page: 1})
	if err != nil {
		return err
	}
	out := make([][]string, 0, len(rows))
	for _, p := range rows {
		barcode := ""
		if p.Barcode != nil {
			barcode = *p.Barcode
		}
		out = append(out, []string{
			barcode, p.Name, csvMoney(p.StockQty), "", csvMoney(p.CostPrice),
			csvMoney(p.SellingPrice), csvMoney(p.WholesalePrice),
		})
	}
	return writeSheet(c, "stock-count-sheet", stockTakeColumns, out)
}

// StockTakeImport applies a filled count sheet: for each row matched by barcode it
// optionally updates the cost, then sets the on-hand quantity to counted_qty via
// the audited stock.Adjust path. Blank counts and unchanged rows are left alone.
func (a *adminUI) StockTakeImport(c echo.Context) error {
	ctx := c.Request().Context()
	uid := middleware.CurrentUserID(c)
	col, recs, err := readImportCSV(c, stockTakeSynonyms)
	if err != nil {
		return err
	}

	var sum adminfragments.ImportSummary
	unchanged := 0
	line := 1
	for _, rec := range recs {
		line++
		if line-1 > maxImportRows {
			sum.Notes = append(sum.Notes, fmt.Sprintf("stopped at %d rows (limit)", maxImportRows))
			break
		}
		get := cellGetter(col, rec)
		barcode := get("barcode")
		counted := get("counted_qty")
		if barcode == "" && get("name") == "" && counted == "" {
			continue // blank trailing row
		}
		if counted == "" {
			continue // not counted — leave unchanged
		}
		if barcode == "" {
			sum.Errors = append(sum.Errors, adminfragments.ImportRowError{Line: line, Message: "barcode required to match a product"})
			sum.Skipped++
			continue
		}
		target, perr := money.Parse(counted)
		if perr != nil || target.IsNegative() {
			sum.Errors = append(sum.Errors, adminfragments.ImportRowError{Line: line, Message: "counted_qty must be a non-negative number"})
			sum.Skipped++
			continue
		}
		p, ferr := a.s.products.GetByBarcode(ctx, barcode)
		if ferr != nil {
			sum.Errors = append(sum.Errors, adminfragments.ImportRowError{Line: line, Message: "no product with barcode " + barcode})
			sum.Skipped++
			continue
		}
		// Set cost first (if given) so the opening/adjustment batch is valued right.
		// Done before the unchanged-qty check so a cost-only correction still applies.
		costChanged := false
		if costStr := get("cost"); costStr != "" {
			if cost, cerr := money.Parse(costStr); cerr == nil {
				if cp, gerr := a.s.products.Get(ctx, p.ID); gerr == nil && !cp.CostPrice.Equal(cost) {
					if a.s.products.SetCost(ctx, p.ID, cost) == nil {
						costChanged = true
					}
				}
			}
		}
		// Selling / wholesale prices (optional): a blank column keeps the current
		// value. Counts as an update so a price-only correction isn't "unchanged".
		if sp, wp := get("selling_price"), get("wholesale_price"); sp != "" || wp != "" {
			if cp, gerr := a.s.products.Get(ctx, p.ID); gerr == nil {
				sell, whole := cp.SellingPrice, cp.WholesalePrice
				if sp != "" {
					if v, verr := money.Parse(sp); verr == nil {
						sell = v
					}
				}
				if wp != "" {
					if v, verr := money.Parse(wp); verr == nil {
						whole = v
					}
				}
				if !sell.Equal(cp.SellingPrice) || !whole.Equal(cp.WholesalePrice) {
					if a.s.products.SetPrices(ctx, p.ID, sell, whole) == nil {
						costChanged = true
					}
				}
			}
		}
		if cur, cerr := a.s.stock.Quantity(ctx, p.ID); cerr == nil && cur.Equal(target) {
			if costChanged {
				sum.Updated++
			} else {
				unchanged++
			}
			continue
		}
		if aerr := a.s.stock.Adjust(ctx, stock.AdjustInput{ProductID: p.ID, NewQuantity: counted, Note: "stock-take CSV"}, uid); aerr != nil {
			sum.Errors = append(sum.Errors, adminfragments.ImportRowError{Line: line, Message: importErrMsg(aerr)})
			sum.Skipped++
			continue
		}
		sum.Updated++
	}
	if unchanged > 0 {
		sum.Notes = append(sum.Notes, fmt.Sprintf("%d rows already matched the count (unchanged)", unchanged))
	}

	a.s.logAudit(c, audit.ActionUpdate, "stock", "",
		fmt.Sprintf("stock-take CSV: %d adjusted, %d unchanged, %d skipped", sum.Updated, unchanged, sum.Skipped))
	c.Response().Header().Set("HX-Trigger", "reload-products")
	return response.RenderFragment(c, adminfragments.ImportResultView(sum))
}
