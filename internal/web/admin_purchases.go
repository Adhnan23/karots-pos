package web

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/purchases"
	"karots-pos/internal/features/supplierpay"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"
	adminfragments "karots-pos/templates/fragments/admin"
	adminpages "karots-pos/templates/pages/admin"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

// ============================ Purchase Orders (draft → receive) ============================

// PurchaseEntryCreate saves the New-Purchase entry form as a draft Purchase Order
// (no inventory effect). Receiving happens later on the receive screen.
func (a *adminUI) PurchaseEntryCreate(c echo.Context) error {
	var in purchases.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	d, err := a.s.purchases.CreateDraft(c.Request().Context(), in, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionCreate, "purchase", strconv.FormatInt(d.Purchase.ID, 10), "created purchase order (draft)")
	return response.Created(c, d)
}

// PurchaseDraftCreate builds one draft Purchase Order per supplier from the
// low-stock reorder picker. Returns the new draft IDs for the print step.
func (a *adminUI) PurchaseDraftCreate(c echo.Context) error {
	var in purchases.ReorderPOInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	ids, err := a.s.purchases.CreateDraftsFromReorder(c.Request().Context(), in, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionCreate, "purchase", "", "built purchase orders from reorder")
	return response.Created(c, map[string]any{"ids": ids})
}

// PurchaseDraftEditForm renders the entry form prefilled from a draft for editing.
func (a *adminUI) PurchaseDraftEditForm(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	d, err := a.s.purchases.Get(ctx, id)
	if err != nil {
		return err
	}
	if d.Purchase.Status != "draft" {
		return c.Redirect(303, "/admin/purchases/"+strconv.FormatInt(id, 10))
	}
	sups, err := a.s.suppliers.List(ctx, "")
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.PurchaseEntryPage(adminpages.PurchaseEntryData{
		UserName:   middleware.CurrentUserName(c),
		Symbol:     a.symbol(ctx),
		Suppliers:  sups,
		EditID:     id,
		ConfigJSON: entryConfigJSON(*d),
	}))
}

// PurchaseDraftUpdate replaces a draft's lines/header (no inventory effect).
func (a *adminUI) PurchaseDraftUpdate(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in purchases.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	d, err := a.s.purchases.UpdateDraft(ctx, id, in, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionUpdate, "purchase", strconv.FormatInt(id, 10), "updated draft purchase order")
	return response.OK(c, d)
}

// PurchaseReceiveForm renders the receive screen for a draft (ordered vs received).
func (a *adminUI) PurchaseReceiveForm(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	d, err := a.s.purchases.Get(ctx, id)
	if err != nil {
		return err
	}
	if d.Purchase.Status != "draft" {
		return c.Redirect(303, "/admin/purchases/"+strconv.FormatInt(id, 10))
	}
	// Current product cost/sell drives the receive screen's margin guard.
	cur := make(map[int64][2]string, len(d.Items))
	for _, it := range d.Items {
		if p, err := a.s.products.Get(ctx, it.ProductID); err == nil {
			cur[it.ProductID] = [2]string{p.CostPrice.String(), p.SellingPrice.String()}
		}
	}
	// Where the cash would come from if the supplier is paid on the spot.
	sources, err := a.cashLocationChoices(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.PurchaseReceivePage(adminpages.PurchaseReceiveData{
		UserName:   middleware.CurrentUserName(c),
		Symbol:     a.symbol(ctx),
		Detail:     *d,
		ConfigJSON: receiveConfigJSON(*d, cur, sources),
	}))
}

// PurchaseReceive applies a draft: records actual received qty (overstock allowed),
// invoice/paid/due, and posts the inventory + payable effects.
func (a *adminUI) PurchaseReceive(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var req receiveRequest
	if err := c.Bind(&req); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	in := req.ReceiveInput
	if err := c.Validate(&in); err != nil {
		return err
	}
	pay, err := parsePayNow(req.PayFields)
	if err != nil {
		return err
	}
	userID := middleware.CurrentUserID(c)

	var d *purchases.Detail
	var rec *cashflow.Receipt
	err = appdb.WithTx(ctx, a.db, func(tx *sqlx.Tx) error {
		got, txErr := purchases.ReceiveTx(ctx, tx, id, in, userID)
		if txErr != nil {
			return txErr
		}
		d = got
		if !pay.amount.IsPositive() {
			return nil
		}
		name := ""
		if sup, gerr := a.s.suppliers.Get(ctx, d.Purchase.SupplierID); gerr == nil {
			name = sup.Name
		}
		_, k, txErr := a.s.paySupplierTx(ctx, tx, payRequest{
			SupplierID:   d.Purchase.SupplierID,
			SupplierName: name,
			In: supplierpay.PayInput{
				Method:      pay.method,
				Allocations: []supplierpay.Alloc{{PurchaseID: id, Amount: pay.amount}},
			},
			Source: pay.source,
		}, userID)
		rec = k
		return txErr
	})
	if err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionUpdate, "purchase", strconv.FormatInt(id, 10), "received purchase order")
	if rec != nil {
		a.s.printMoneyReceipt(ctx, rec)
	}
	return response.OK(c, d)
}

// PurchaseDraftDelete removes a draft (only while still a draft).
func (a *adminUI) PurchaseDraftDelete(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.purchases.DeleteDraft(c.Request().Context(), id); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionDelete, "purchase", strconv.FormatInt(id, 10), "deleted draft purchase order")
	return response.OK(c, map[string]any{"ok": true})
}

// DraftPOPrint renders printable Purchase Order document(s) for the given draft
// IDs — combined on one page or one-per-supplier (page-broken).
func (a *adminUI) DraftPOPrint(c echo.Context) error {
	ctx := c.Request().Context()
	ids := parseIDList(c.QueryParam("ids"))
	if len(ids) == 0 {
		return apperr.BadRequest("no purchase orders selected")
	}
	mode := c.QueryParam("mode")
	if mode != "separate" {
		mode = "combined"
	}
	details, err := a.s.purchases.GetMany(ctx, ids)
	if err != nil {
		return err
	}
	orders := make([]adminpages.POOrder, 0, len(details))
	for _, det := range details {
		o := adminpages.POOrder{Detail: det}
		if sup, err := a.s.suppliers.Get(ctx, det.Purchase.SupplierID); err == nil && sup != nil {
			o.Supplier = *sup
		}
		orders = append(orders, o)
	}
	d := adminpages.POPrintData{
		ShopName: a.shopName(ctx),
		Symbol:   a.symbol(ctx),
		Mode:     mode,
		IDs:      c.QueryParam("ids"),
		Orders:   orders,
	}
	d.Address, d.Phone = a.shopContact(ctx)
	return response.RenderPage(c, adminpages.POPrintPage(d))
}

// GRNPrint renders the goods-received slip for a received purchase.
func (a *adminUI) GRNPrint(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	det, err := a.s.purchases.Get(ctx, id)
	if err != nil {
		return err
	}
	o := adminpages.POOrder{Detail: *det}
	if sup, err := a.s.suppliers.Get(ctx, det.Purchase.SupplierID); err == nil && sup != nil {
		o.Supplier = *sup
	}
	d := adminpages.GRNPrintData{
		ShopName: a.shopName(ctx),
		Symbol:   a.symbol(ctx),
		Order:    o,
	}
	d.Address, d.Phone = a.shopContact(ctx)
	return response.RenderPage(c, adminpages.GRNPrintPage(d))
}

// shopContact returns the shop address + phone from settings (blank when unset).
func (a *adminUI) shopContact(ctx context.Context) (address, phone string) {
	if cfg, err := a.s.settings.Get(ctx); err == nil && cfg != nil {
		if cfg.Address != nil {
			address = *cfg.Address
		}
		if cfg.Phone != nil {
			phone = *cfg.Phone
		}
	}
	return address, phone
}

// --- helpers ---

func parseIDList(s string) []int64 {
	out := make([]int64, 0)
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if n, err := strconv.ParseInt(p, 10, 64); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	return out
}

// entryConfigJSON serialises a draft for the edit form's Alpine state.
func entryConfigJSON(d purchases.Detail) string {
	type line struct {
		ProductID    int64  `json:"product_id"`
		Name         string `json:"name"`
		Quantity     string `json:"quantity"`
		CostPrice    string `json:"cost_price"`
		SellingPrice string `json:"selling_price"`
		ExpiryDate   string `json:"expiry_date"`
	}
	lines := make([]line, 0, len(d.Items))
	for _, it := range d.Items {
		exp := ""
		if it.ExpiryDate != nil {
			exp = it.ExpiryDate.Format("2006-01-02")
		}
		lines = append(lines, line{
			ProductID:    it.ProductID,
			Name:         it.ProductName,
			Quantity:     it.Quantity.String(),
			CostPrice:    it.CostPrice.String(),
			SellingPrice: it.SellingPrice.String(),
			ExpiryDate:   exp,
		})
	}
	notes := ""
	if d.Purchase.Notes != nil {
		notes = *d.Purchase.Notes
	}
	expected := ""
	if d.Purchase.ExpectedDate != nil {
		expected = d.Purchase.ExpectedDate.Format("2006-01-02")
	}
	b, _ := json.Marshal(map[string]any{
		"editId":       d.Purchase.ID,
		"supplierId":   strconv.FormatInt(d.Purchase.SupplierID, 10),
		"supplierName": d.Purchase.SupplierName,
		"expectedDate": expected,
		"notes":        notes,
		"lines":        lines,
	})
	return string(b)
}

// receiveConfigJSON serialises a draft's lines for the receive screen (ordered +
// editable received qty defaulting to ordered). cur holds each product's current
// {cost, sell} so the screen can flag a squeezed margin and suggest a new price.
func receiveConfigJSON(d purchases.Detail, cur map[int64][2]string, sources []adminfragments.LocationChoice) string {
	type line struct {
		ProductID    int64  `json:"product_id"`
		ProductName  string `json:"product_name"`
		Ordered      string `json:"ordered"`
		Quantity     string `json:"quantity"`
		CostPrice    string `json:"cost_price"`
		SellingPrice string `json:"selling_price"`
		ExpiryDate   string `json:"expiry_date"`
		CurCost      string `json:"cur_cost"`
		CurSell      string `json:"cur_sell"`
	}
	lines := make([]line, 0, len(d.Items))
	for _, it := range d.Items {
		ord := it.Quantity.String()
		if it.OrderedQty != nil {
			ord = it.OrderedQty.String()
		}
		exp := ""
		if it.ExpiryDate != nil {
			exp = it.ExpiryDate.Format("2006-01-02")
		}
		curCost, curSell := "0", "0"
		if v, ok := cur[it.ProductID]; ok {
			curCost, curSell = v[0], v[1]
		}
		lines = append(lines, line{
			ProductID:    it.ProductID,
			ProductName:  it.ProductName,
			Ordered:      ord,
			Quantity:     ord,
			CostPrice:    it.CostPrice.String(),
			SellingPrice: it.SellingPrice.String(),
			ExpiryDate:   exp,
			CurCost:      curCost,
			CurSell:      curSell,
		})
	}
	type src struct {
		Value string `json:"value"`
		Label string `json:"label"`
	}
	srcs := make([]src, 0, len(sources))
	for _, s := range sources {
		srcs = append(srcs, src{Value: s.Value, Label: s.Label})
	}
	b, _ := json.Marshal(map[string]any{
		"id":      d.Purchase.ID,
		"lines":   lines,
		"sources": srcs,
	})
	return string(b)
}
