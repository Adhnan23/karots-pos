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
	Sources      []adminfragments.LocationChoice
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
	b, _ := json.Marshal(map[string]any{
		"supplierId":   strconv.FormatInt(cfg.SupplierID, 10),
		"supplierName": cfg.SupplierName,
		"postUrl":      cfg.PostURL,
		"redirect":     "/cashier/suppliers",
		"savedMsg":     "Goods received",
		"withPayment":  true,
		"sources":      srcs,
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
