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
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/purchases"
	"karots-pos/internal/features/supplierpay"
	"karots-pos/internal/features/suppliers"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"
	adminfragments "karots-pos/templates/fragments/admin"
	cashierpages "karots-pos/templates/pages/cashier"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

// ============================ Suppliers at the counter ============================
//
// A cashier is often the only person in the shop when a supplier walks in
// wanting money, delivering goods, or asking what to send next. Every supplier
// route used to be admin-only, so either the owner was called away or the visit
// went unrecorded — and cash handed over off-system closes the till short.
//
// These routes are gated by middleware.RequireSupplierAccess: admins and
// managers always pass, a cashier only with the per-user flag.

// Suppliers lists suppliers with what the shop owes each of them.
func (h *cashierUI) Suppliers(c echo.Context) error {
	d, err := h.supplierData(c, c.QueryParam("q"))
	if err != nil {
		return err
	}
	return response.RenderPage(c, cashierpages.Suppliers(d))
}

// SuppliersTable is the HTMX fragment behind the search box and the reload event.
func (h *cashierUI) SuppliersTable(c echo.Context) error {
	d, err := h.supplierData(c, c.QueryParam("q"))
	if err != nil {
		return err
	}
	return response.RenderFragment(c, cashierpages.SuppliersTable(d))
}

func (h *cashierUI) supplierData(c echo.Context, q string) (cashierpages.SuppliersData, error) {
	ctx := c.Request().Context()
	rows, err := h.s.suppliers.List(ctx, strings.TrimSpace(q))
	if err != nil {
		return cashierpages.SuppliersData{}, err
	}
	return cashierpages.SuppliersData{
		CashierName:   middleware.CurrentUserName(c),
		Role:          middleware.CurrentRole(c),
		ShowChangePin: h.showChangePin(c),
		Symbol:        h.cashierSymbol(ctx),
		Query:         q,
		Suppliers:     rows,
	}, nil
}

// cashierCashSources lists where a cashier may take cash from: their own open
// drawer first, then the lockers the owner has marked usable by cashiers.
//
// The till entry is offered whether or not a session is open — cashflow refuses
// with a clear "that till has no open session" rather than us hiding the option
// and leaving the cashier guessing why they can't pay.
func (h *cashierUI) cashierCashSources(ctx context.Context, userID int64, userName string) ([]adminfragments.LocationChoice, error) {
	sym := h.cashierSymbol(ctx)
	out := []adminfragments.LocationChoice{{
		Value: "till:" + strconv.FormatInt(userID, 10),
		Label: "My drawer — " + userName,
		Group: "Till",
	}}
	rows, err := h.s.lockers.ListForCashier(ctx)
	if err != nil {
		return nil, err
	}
	for _, l := range rows {
		out = append(out, adminfragments.LocationChoice{
			Value: "locker:" + strconv.FormatInt(l.ID, 10),
			Label: l.Name + " (" + money.Format(sym, l.Balance) + ")",
			Group: "Lockers",
		})
	}
	return out, nil
}

// counterReceiveConfig seeds the counter's line editor. It reuses the admin
// grn() Alpine component, which takes the endpoint and the payment block from
// its config — one line editor and one product search, two screens.
type counterReceiveConfig struct {
	SupplierID   int64
	SupplierName string
	PostURL      string
	SavedMsg     string
	WithPayment  bool
	ProductURL   string // set to allow creating a product from a line
	// ProductNoPrices marks the order screen, where a product is created from a
	// name alone — there is no invoice yet, so no price is asked for.
	ProductNoPrices bool
	// Lines prefills the editor when receiving against an order already placed.
	Lines   []counterLine
	Sources []adminfragments.LocationChoice
}

// counterLine is one prefilled row of an order being received.
type counterLine struct {
	ProductID   int64  `json:"product_id"`
	Name        string `json:"name"`
	Ordered     string `json:"ordered"`
	Quantity    string `json:"quantity"`
	CostPrice   string `json:"cost_price"`
	SellingPric string `json:"selling_price"`
}

func counterReceiveConfigJSON(cfg counterReceiveConfig) string {
	type src struct {
		Value string `json:"value"`
		Label string `json:"label"`
	}
	srcs := make([]src, 0, len(cfg.Sources))
	for _, s := range cfg.Sources {
		srcs = append(srcs, src{Value: s.Value, Label: s.Label})
	}
	msg := cfg.SavedMsg
	if msg == "" {
		msg = "Goods received"
	}
	b, _ := json.Marshal(map[string]any{
		"supplierId":         strconv.FormatInt(cfg.SupplierID, 10),
		"supplierName":       cfg.SupplierName,
		"postUrl":            cfg.PostURL,
		"redirect":           "/cashier/suppliers",
		"savedMsg":           msg,
		"withPayment":        cfg.WithPayment,
		"productUrl":         cfg.ProductURL,
		"productNeedsPrices": !cfg.ProductNoPrices,
		"lines":              cfg.Lines,
		"sources":            srcs,
	})
	return string(b)
}

// allowedSource reports whether a submitted cash location is one the counter
// actually offered.
//
// Filtering the picker is not enough on its own: the form posts a plain
// "locker:7" string, so without this a cashier could hand-craft a request
// against the owner's safe — verified during development, it emptied 500 out of
// a locker marked off-limits and returned 200. The counter is restricted for
// everyone who uses it, admins included; the full picker still lives in admin.
func allowedSource(choices []adminfragments.LocationChoice, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, ch := range choices {
		if ch.Value == value {
			return true
		}
	}
	return false
}

// counterSource validates and parses the cash location a counter form submitted.
func (h *cashierUI) counterSource(c echo.Context, value string) (cashflow.Location, error) {
	choices, err := h.cashierCashSources(c.Request().Context(),
		middleware.CurrentUserID(c), middleware.CurrentUserName(c))
	if err != nil {
		return cashflow.Location{}, err
	}
	if !allowedSource(choices, value) {
		return cashflow.Location{}, apperr.Forbidden("you can't take cash from there")
	}
	return parseLocation(value)
}

// SupplierPayForm renders the counter pay dialog: open invoices to allocate
// against, and the cash sources this cashier is allowed to use.
func (h *cashierUI) SupplierPayForm(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	sup, err := h.s.suppliers.Get(ctx, id)
	if err != nil {
		return err
	}
	invoices, err := h.s.supplierPay.OpenInvoices(ctx, id)
	if err != nil {
		return err
	}
	sources, err := h.cashierCashSources(ctx, middleware.CurrentUserID(c), middleware.CurrentUserName(c))
	if err != nil {
		return err
	}
	return response.RenderFragment(c, cashierpages.SupplierPayForm(cashierpages.SupplierPayData{
		Supplier: *sup,
		Invoices: invoices,
		Symbol:   h.cashierSymbol(ctx),
		Sources:  sources,
	}))
}

// ReceiveForm renders the counter delivery screen for one supplier.
func (h *cashierUI) ReceiveForm(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	sup, err := h.s.suppliers.Get(ctx, id)
	if err != nil {
		return err
	}
	sources, err := h.cashierCashSources(ctx, middleware.CurrentUserID(c), middleware.CurrentUserName(c))
	if err != nil {
		return err
	}
	// Point out any order already open with this supplier, so a delivery that
	// was ordered gets received against it rather than typed in twice.
	drafts, err := h.s.purchases.ListDrafts(ctx)
	if err != nil {
		return err
	}
	mine := make([]purchases.Purchase, 0, 2)
	for _, d := range drafts {
		if d.SupplierID == id {
			mine = append(mine, d)
		}
	}
	return response.RenderPage(c, cashierpages.SupplierReceive(cashierpages.SupplierReceiveData{
		CashierName:   middleware.CurrentUserName(c),
		Role:          middleware.CurrentRole(c),
		ShowChangePin: h.showChangePin(c),
		Symbol:        h.cashierSymbol(ctx),
		Supplier:      *sup,
		Drafts:        mine,
		ConfigJSON: counterReceiveConfigJSON(counterReceiveConfig{
			SupplierID:   id,
			SupplierName: sup.Name,
			PostURL:      "/cashier/suppliers/" + strconv.FormatInt(id, 10) + "/receive",
			SavedMsg:     "Goods received",
			WithPayment:  true,
			ProductURL:   "/cashier/suppliers/products",
			Sources:      sources,
		}),
	}))
}

// ReceiveWalkIn takes in a delivery that was never ordered — the supplier who
// simply turns up with goods.
//
// Goods and any payment are one transaction, so a failed payment never leaves
// stock on the shelf without its payable.
func (h *cashierUI) ReceiveWalkIn(c echo.Context) error {
	ctx := c.Request().Context()
	supplierID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var req createRequest
	if err := c.Bind(&req); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	in := req.CreateInput
	in.SupplierID = supplierID // never trust the body for who this is owed to
	if err := c.Validate(&in); err != nil {
		return err
	}
	pay, err := parsePayNow(req.PayFields)
	if err != nil {
		return err
	}
	if pay.amount.IsPositive() && pay.method == "cash" {
		if pay.source, err = h.counterSource(c, req.PaySource); err != nil {
			return err
		}
	}
	sup, err := h.s.suppliers.Get(ctx, supplierID)
	if err != nil {
		return err
	}
	userID := middleware.CurrentUserID(c)

	var d *purchases.Detail
	var rec *cashflow.Receipt
	err = appdb.WithTx(ctx, h.s.db, func(tx *sqlx.Tx) error {
		got, txErr := purchases.CreateTx(ctx, tx, in, userID)
		if txErr != nil {
			return txErr
		}
		d = got
		// Spend anything the supplier already holds for us first, then cap what is
		// paid now at what genuinely remains.
		remaining, txErr := owedAfterSettlement(ctx, tx, d.Purchase.ID)
		if txErr != nil {
			return txErr
		}
		pay.amount = clampToBalance(pay.amount, remaining)
		if !pay.amount.IsPositive() {
			return nil
		}
		_, k, txErr := h.s.paySupplierTx(ctx, tx, payRequest{
			SupplierID:   supplierID,
			SupplierName: sup.Name,
			In: supplierpay.PayInput{
				Method:      pay.method,
				Allocations: []supplierpay.Alloc{{PurchaseID: d.Purchase.ID, Amount: pay.amount}},
			},
			Source: pay.source,
		}, userID)
		rec = k
		return txErr
	})
	if err != nil {
		return err
	}
	h.s.logAudit(c, audit.ActionCreate, "purchase", strconv.FormatInt(d.Purchase.ID, 10),
		"received a delivery from "+sup.Name+" at the counter")
	if rec != nil {
		h.s.printMoneyReceipt(ctx, rec)
	}
	return response.Created(c, d)
}

// OrderForm renders the counter order screen — what the supplier should send
// next time.
func (h *cashierUI) OrderForm(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	sup, err := h.s.suppliers.Get(ctx, id)
	if err != nil {
		return err
	}
	return response.RenderPage(c, cashierpages.SupplierOrder(cashierpages.SupplierOrderData{
		CashierName:   middleware.CurrentUserName(c),
		Role:          middleware.CurrentRole(c),
		ShowChangePin: h.showChangePin(c),
		Symbol:        h.cashierSymbol(ctx),
		Supplier:      *sup,
		ConfigJSON: counterReceiveConfigJSON(counterReceiveConfig{
			SupplierID:      id,
			SupplierName:    sup.Name,
			PostURL:         "/cashier/suppliers/" + strconv.FormatInt(id, 10) + "/order",
			SavedMsg:        "Order saved",
			WithPayment:     false,
			ProductURL:      "/cashier/suppliers/products/wanted",
			ProductNoPrices: true,
		}),
	}))
}

// OrderCreate records what the supplier should send next time as a normal draft
// purchase order, stamped with the cashier who took it.
//
// The phone call to the owner is the approval, so there is no second
// confirmation step — the draft simply appears in the owner's Purchases list.
func (h *cashierUI) OrderCreate(c echo.Context) error {
	ctx := c.Request().Context()
	supplierID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in purchases.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	in.SupplierID = supplierID
	if err := c.Validate(&in); err != nil {
		return err
	}
	d, err := h.s.purchases.CreateDraft(ctx, in, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	h.s.logAudit(c, audit.ActionCreate, "purchase", strconv.FormatInt(d.Purchase.ID, 10),
		"took a supplier order at the counter")
	return response.Created(c, map[string]any{
		"id":        d.Purchase.ID,
		"print_url": "/cashier/suppliers/orders/print?ids=" + strconv.FormatInt(d.Purchase.ID, 10),
	})
}

// SupplierPayAtCounter records a payment handed over at the till.
//
// Mirrors the admin handler through the same paySupplierTx helper; the only
// differences are the cash sources offered above and the print URL below, since
// /admin/money-receipts is unreachable for a cashier.
func (h *cashierUI) SupplierPayAtCounter(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	invoices, err := h.s.supplierPay.OpenInvoices(ctx, id)
	if err != nil {
		return err
	}
	in, err := parseAllocations(c, invoices)
	if err != nil {
		return err
	}
	sup, err := h.s.suppliers.Get(ctx, id)
	if err != nil {
		return err
	}
	userID := middleware.CurrentUserID(c)

	method, ok := normSupplierMethod(in.Method)
	if !ok {
		return apperr.Validation("invalid payment method")
	}
	var src cashflow.Location
	if method == "cash" {
		src, err = h.counterSource(c, c.FormValue("source"))
		if err != nil {
			return err
		}
	}

	var res *supplierpay.Result
	var rec *cashflow.Receipt
	err = appdb.WithTx(ctx, h.s.db, func(tx *sqlx.Tx) error {
		r, k, txErr := h.s.paySupplierTx(ctx, tx, payRequest{
			SupplierID: id, SupplierName: sup.Name, In: in, Source: src,
		}, userID)
		res, rec = r, k
		return txErr
	})
	if err != nil {
		return err
	}
	h.s.logAudit(c, audit.ActionPayment, "supplier", strconv.FormatInt(id, 10),
		"paid "+money.Display(res.Total)+" ("+res.Method+") at the counter")

	msg := "Paid " + money.Display(res.Total) + " to " + sup.Name
	cfg, _ := h.s.settings.Get(ctx)
	if rec != nil && cfg != nil && cfg.AskToPrint {
		printURL := "/cashier/money-receipts/" + strconv.FormatInt(rec.ID, 10) + "/print"
		c.Response().Header().Set("HX-Trigger",
			response.PrintPrompt(msg+" · "+rec.ReceiptNo, printURL, false, "reload-suppliers", "close-modal"))
		return c.NoContent(200)
	}
	if rec != nil {
		h.s.printMoneyReceipt(ctx, rec)
	}
	return htmxDone(c, msg, "reload-suppliers")
}

// SupplierRefundAtCounter records money handed back BY a supplier at the till.
//
// Mirrors the admin handler, with the counter's one difference: the destination
// is validated through counterSource, so a cashier can only put the cash into
// their own drawer or a locker the owner has opened to them.
func (h *cashierUI) SupplierRefundAtCounter(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	sup, err := h.s.suppliers.Get(ctx, id)
	if err != nil {
		return err
	}
	in, err := parseRefund(c)
	if err != nil {
		return err
	}
	userID := middleware.CurrentUserID(c)

	var dest cashflow.Location
	if in.Method == "cash" {
		dest, err = h.counterSource(c, c.FormValue("dest"))
		if err != nil {
			return err
		}
	}

	var res *supplierpay.RefundResult
	var rec *cashflow.Receipt
	err = appdb.WithTx(ctx, h.s.db, func(tx *sqlx.Tx) error {
		r, txErr := h.s.supplierPay.RefundTx(ctx, tx, id, in, userID)
		if txErr != nil {
			return txErr
		}
		res = r
		if r.Method != "cash" {
			return nil
		}
		k, txErr := h.s.cashflow.MoveTx(ctx, tx, cashflow.MoveInput{
			From:        cashflow.External(),
			To:          dest,
			Amount:      r.Amount,
			Reason:      "refund from " + sup.Name,
			ReceiptKind: "supplier_refund",
			Party:       sup.Name,
			Ref:         &cashflow.Ref{Kind: "supplier_refund", ID: r.RefundID},
			ActorID:     userID,
		})
		rec = k
		return txErr
	})
	if err != nil {
		return err
	}
	h.s.logAudit(c, audit.ActionPayment, "supplier", strconv.FormatInt(id, 10),
		"refund received "+money.Display(res.Amount)+" ("+res.Method+") at the counter")

	msg := "Received " + money.Display(res.Amount) + " back from " + sup.Name
	cfg, _ := h.s.settings.Get(ctx)
	if rec != nil && cfg != nil && cfg.AskToPrint {
		printURL := "/cashier/money-receipts/" + strconv.FormatInt(rec.ID, 10) + "/print"
		c.Response().Header().Set("HX-Trigger",
			response.PrintPrompt(msg+" · "+rec.ReceiptNo, printURL, false, "reload-suppliers", "close-modal"))
		return c.NoContent(200)
	}
	if rec != nil {
		h.s.printMoneyReceipt(ctx, rec)
	}
	return htmxDone(c, msg, "reload-suppliers")
}

// OrderPrint renders the printable order slip for the supplier to take away.
// The renderer lives on Server because /admin/purchases/po/print is out of
// reach for a cashier.
func (h *cashierUI) OrderPrint(c echo.Context) error {
	return h.s.renderPOPrint(c)
}

// SupplierQuickCreate adds a supplier nobody has dealt with before, from the
// counter.
//
// Name and phone only: a supplier is standing there with boxes, and the address
// and credit terms can wait. Without this the cashier's only options were to
// record the delivery against the wrong supplier or not record it at all — the
// very thing the counter exists to prevent.
func (h *cashierUI) SupplierQuickCreate(c echo.Context) error {
	ctx := c.Request().Context()
	name := strings.TrimSpace(c.FormValue("name"))
	if name == "" {
		return apperr.Validation("supplier name is required")
	}
	in := suppliers.CreateInput{Name: name, CreditDays: 0, OpeningBalance: "0"}
	if phone := strings.TrimSpace(c.FormValue("phone")); phone != "" {
		in.Phone = &phone
	}
	sup, err := h.s.suppliers.Create(ctx, in)
	if err != nil {
		return err
	}
	h.s.logAudit(c, audit.ActionCreate, "supplier", strconv.FormatInt(sup.ID, 10),
		"added supplier at the counter: "+sup.Name)
	return htmxDone(c, sup.Name+" added", "reload-suppliers")
}

// ProductQuickCreate adds a product that has just arrived on a delivery, so the
// receive line can carry it.
//
// Without this the line simply could not be filled, so the item was left off the
// invoice — which makes the stock, the invoice total and therefore the payment
// all wrong. See products.CreateForIntake for why this is not the till's
// quick-add.
//
// Both prices are required here, and checked server-side rather than only in the
// form: the cashier is holding the invoice, and a zero cost would book the
// eventual sale as pure profit.
func (h *cashierUI) ProductQuickCreate(c echo.Context) error {
	var in products.IntakeInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request")
	}
	if err := requirePositive(in.Cost, "cost price"); err != nil {
		return err
	}
	if err := requirePositive(in.Selling, "selling price"); err != nil {
		return err
	}
	return h.createIntakeProduct(c, in, "added on a delivery at the counter: ")
}

// ProductWantedCreate adds a product the supplier's rep has just described while
// taking an order — something the shop has never stocked.
//
// Name only, on purpose. There is no invoice yet: the rep quotes from memory and
// that number would become the catalogue price of an item nobody has seen. It is
// safe to leave priceless because with no stock it cannot be sold, and receiving
// the delivery overwrites cost and price from the real invoice.
func (h *cashierUI) ProductWantedCreate(c echo.Context) error {
	var in products.IntakeInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request")
	}
	in.Cost, in.Selling = "", "" // never take a quoted price as fact
	return h.createIntakeProduct(c, in, "added while taking an order at the counter: ")
}

func (h *cashierUI) createIntakeProduct(c echo.Context, in products.IntakeInput, note string) error {
	p, err := h.s.products.CreateForIntake(c.Request().Context(), in, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	h.s.logAudit(c, audit.ActionCreate, "product", strconv.FormatInt(p.ID, 10), note+p.Name)
	return response.Created(c, p)
}

// requirePositive rejects a blank or non-positive money field by name.
func requirePositive(raw, label string) error {
	v, err := money.Parse(strings.TrimSpace(raw))
	if err != nil || !v.IsPositive() {
		return apperr.Validation(label + " is required")
	}
	return nil
}

// SupplierNewForm renders the counter's add-a-supplier dialog.
func (h *cashierUI) SupplierNewForm(c echo.Context) error {
	return response.RenderFragment(c, cashierpages.SupplierNewForm())
}

// ReceiveAgainstOrderForm renders the counter receive screen prefilled from an
// order already placed, so a delivery that was ordered closes that order off
// instead of being typed in fresh as a second purchase.
func (h *cashierUI) ReceiveAgainstOrderForm(c echo.Context) error {
	ctx := c.Request().Context()
	poID, err := strconv.ParseInt(c.Param("poID"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	d, err := h.s.purchases.Get(ctx, poID)
	if err != nil {
		return err
	}
	if d.Purchase.Status != "draft" {
		return c.Redirect(303, "/cashier/suppliers")
	}
	sup, err := h.s.suppliers.Get(ctx, d.Purchase.SupplierID)
	if err != nil {
		return err
	}
	sources, err := h.cashierCashSources(ctx, middleware.CurrentUserID(c), middleware.CurrentUserName(c))
	if err != nil {
		return err
	}
	lines := make([]counterLine, 0, len(d.Items))
	for _, it := range d.Items {
		ordered := it.Quantity.String()
		if it.OrderedQty != nil {
			ordered = it.OrderedQty.String()
		}
		lines = append(lines, counterLine{
			ProductID:   it.ProductID,
			Name:        it.ProductName,
			Ordered:     ordered,
			Quantity:    ordered,
			CostPrice:   it.CostPrice.String(),
			SellingPric: it.SellingPrice.String(),
		})
	}
	return response.RenderPage(c, cashierpages.SupplierReceive(cashierpages.SupplierReceiveData{
		CashierName:   middleware.CurrentUserName(c),
		Role:          middleware.CurrentRole(c),
		ShowChangePin: h.showChangePin(c),
		Symbol:        h.cashierSymbol(ctx),
		Supplier:      *sup,
		OrderID:       poID,
		ConfigJSON: counterReceiveConfigJSON(counterReceiveConfig{
			SupplierID:   d.Purchase.SupplierID,
			SupplierName: sup.Name,
			PostURL:      "/cashier/suppliers/orders/" + strconv.FormatInt(poID, 10) + "/receive",
			SavedMsg:     "Goods received",
			WithPayment:  true,
			ProductURL:   "/cashier/suppliers/products",
			Lines:        lines,
			Sources:      sources,
		}),
	}))
}

// ReceiveAgainstOrder takes in a delivery against an order already placed,
// optionally paying in the same transaction.
func (h *cashierUI) ReceiveAgainstOrder(c echo.Context) error {
	ctx := c.Request().Context()
	poID, err := strconv.ParseInt(c.Param("poID"), 10, 64)
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
	if pay.amount.IsPositive() && pay.method == "cash" {
		if pay.source, err = h.counterSource(c, req.PaySource); err != nil {
			return err
		}
	}
	userID := middleware.CurrentUserID(c)

	var d *purchases.Detail
	var rec *cashflow.Receipt
	err = appdb.WithTx(ctx, h.s.db, func(tx *sqlx.Tx) error {
		got, txErr := purchases.ReceiveTx(ctx, tx, poID, in, userID)
		if txErr != nil {
			return txErr
		}
		d = got
		remaining, txErr := owedAfterSettlement(ctx, tx, poID)
		if txErr != nil {
			return txErr
		}
		pay.amount = clampToBalance(pay.amount, remaining)
		if !pay.amount.IsPositive() {
			return nil
		}
		name := ""
		if sup, gerr := h.s.suppliers.Get(ctx, d.Purchase.SupplierID); gerr == nil {
			name = sup.Name
		}
		_, k, txErr := h.s.paySupplierTx(ctx, tx, payRequest{
			SupplierID:   d.Purchase.SupplierID,
			SupplierName: name,
			In: supplierpay.PayInput{
				Method:      pay.method,
				Allocations: []supplierpay.Alloc{{PurchaseID: poID, Amount: pay.amount}},
			},
			Source: pay.source,
		}, userID)
		rec = k
		return txErr
	})
	if err != nil {
		return err
	}
	h.s.logAudit(c, audit.ActionUpdate, "purchase", strconv.FormatInt(poID, 10),
		"received an order at the counter")
	if rec != nil {
		h.s.printMoneyReceipt(ctx, rec)
	}
	return response.OK(c, d)
}
