package web

import (
	"context"
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/stock"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"
	adminfragments "karots-pos/templates/fragments/admin"
	adminpages "karots-pos/templates/pages/admin"

	"github.com/labstack/echo/v4"
)

// IntakePage renders the Stock Intake workflow: add or restock an item, set its
// quantity, and print barcode labels — all on one page. Gated by the same
// stock-take toggle as the Stock-take page.
func (a *adminUI) IntakePage(c echo.Context) error {
	if err := a.requireStockTake(c); err != nil {
		return err
	}
	ctx := c.Request().Context()
	cats, err := a.s.categories.Tree(ctx)
	if err != nil {
		return err
	}
	us, err := a.s.units.List(ctx)
	if err != nil {
		return err
	}
	// Build the unit options + default to "pcs" here (the picker helpers in the
	// fragments package are unexported), matching the product form's behaviour.
	unitOpts := make([]adminfragments.PickerOption, 0, len(us))
	var unitSel int64
	for _, u := range us {
		unitOpts = append(unitOpts, adminfragments.PickerOption{ID: u.ID, Label: u.Name + " (" + u.Abbreviation + ")"})
		if unitSel == 0 && strings.EqualFold(u.Abbreviation, "pcs") {
			unitSel = u.ID
		}
	}
	if unitSel == 0 && len(us) > 0 {
		unitSel = us[0].ID
	}
	return response.RenderPage(c, adminpages.IntakePage(adminpages.IntakeData{
		UserName:     middleware.CurrentUserName(c),
		Symbol:       a.symbol(ctx),
		Categories:   cats,
		UnitOptions:  unitOpts,
		UnitSelected: unitSel,
	}))
}

// IntakeCreate creates a new product from the minimal intake fields and seeds its
// opening stock. Returns the saved item as JSON so the page can print a label and
// add it to the "added this session" list.
func (a *adminUI) IntakeCreate(c echo.Context) error {
	if err := a.requireStockTake(c); err != nil {
		return err
	}
	var in products.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	ctx := c.Request().Context()
	p, err := a.s.products.Create(ctx, in)
	if err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionCreate, "product", strconv.FormatInt(p.ID, 10), "intake created "+in.Name)

	qty := strings.TrimSpace(c.FormValue("quantity"))
	if qty != "" {
		if n, perr := money.Parse(qty); perr == nil && n.IsPositive() {
			if aerr := a.s.stock.Adjust(ctx, stock.AdjustInput{
				ProductID:   p.ID,
				NewQuantity: n.String(),
				Note:        "stock intake",
			}, middleware.CurrentUserID(c)); aerr != nil {
				return aerr
			}
		}
	}
	return response.OK(c, a.intakeItem(ctx, p.ID, qty))
}

// IntakeRestock adds the entered quantity to an existing product's on-hand stock.
// Any barcode generation for a barcode-less product happens client-side first via
// /api/products/:id/barcode, so here we only move stock.
func (a *adminUI) IntakeRestock(c echo.Context) error {
	if err := a.requireStockTake(c); err != nil {
		return err
	}
	id, err := strconv.ParseInt(c.FormValue("product_id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("select a product")
	}
	add, err := money.Parse(strings.TrimSpace(c.FormValue("quantity")))
	if err != nil || !add.IsPositive() {
		return apperr.Validation("enter a quantity to add")
	}
	ctx := c.Request().Context()
	p, err := a.s.products.Get(ctx, id)
	if err != nil {
		return err
	}

	// Optional price edits (stock-take style): apply cost first so the
	// adjustment batch below is valued at the new cost, then selling/wholesale.
	// A blank field leaves that price unchanged.
	if costStr := strings.TrimSpace(c.FormValue("cost_price")); costStr != "" {
		if cost, cerr := money.Parse(costStr); cerr == nil && !p.CostPrice.Equal(cost) {
			if serr := a.s.products.SetCost(ctx, id, cost); serr != nil {
				return serr
			}
		}
	}
	sellStr := strings.TrimSpace(c.FormValue("selling_price"))
	wholeStr := strings.TrimSpace(c.FormValue("wholesale_price"))
	if sellStr != "" || wholeStr != "" {
		sell, whole := p.SellingPrice, p.WholesalePrice
		if v, verr := money.Parse(sellStr); sellStr != "" && verr == nil {
			sell = v
		}
		if v, verr := money.Parse(wholeStr); wholeStr != "" && verr == nil {
			whole = v
		}
		if !sell.Equal(p.SellingPrice) || !whole.Equal(p.WholesalePrice) {
			if serr := a.s.products.SetPrices(ctx, id, sell, whole); serr != nil {
				return serr
			}
		}
	}

	// The entered selling price does two jobs: above it became the product's new
	// shelf price, and here it prices the lot this restock opens — so stock
	// already on the shelf keeps ringing up at the price it was stickered with.
	newQty := p.StockQty.Add(add)
	if aerr := a.s.stock.Adjust(ctx, stock.AdjustInput{
		ProductID:    id,
		NewQuantity:  newQty.String(),
		Note:         "stock intake restock",
		SellingPrice: sellStr,
	}, middleware.CurrentUserID(c)); aerr != nil {
		return aerr
	}
	a.s.logAudit(c, audit.ActionUpdate, "product", strconv.FormatInt(id, 10), "intake restock +"+add.String())
	return response.OK(c, a.intakeItem(ctx, id, add.String()))
}

// intakeItem builds the small JSON payload the intake page needs to render a
// session-list row and print a label (re-reading the product so the barcode
// reflects any just-saved value).
func (a *adminUI) intakeItem(ctx context.Context, id int64, qty string) map[string]any {
	item := map[string]any{"id": id, "qty": qty, "name": "", "barcode": "", "price": ""}
	if p, err := a.s.products.Get(ctx, id); err == nil {
		item["name"] = p.Name
		if p.Barcode != nil {
			item["barcode"] = *p.Barcode
		}
		item["price"] = money.Format(a.symbol(ctx), p.SellingPrice)
	}
	return item
}
