package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/auth"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/categories"
	"karots-pos/internal/features/conversions"
	"karots-pos/internal/features/customers"
	"karots-pos/internal/features/expenses"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/reports"
	"karots-pos/internal/features/sales"
	"karots-pos/internal/features/stock"
	"karots-pos/internal/features/supplierpay"
	"karots-pos/internal/features/suppliers"
	"karots-pos/internal/features/units"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/printing"
	"karots-pos/internal/response"
	"karots-pos/internal/tspl"
	adminpages "karots-pos/templates/pages/admin"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

// ============================ Suppliers ============================

func (a *adminUI) Suppliers(c echo.Context) error {
	ctx := c.Request().Context()
	search := c.QueryParam("search")
	owing := c.QueryParam("owing") == "1"
	rows, err := a.s.suppliers.List(ctx, search)
	if err != nil {
		return err
	}
	if owing {
		rows = suppliersOwing(rows)
	}
	return response.RenderPage(c, adminpages.SuppliersPage(adminpages.SuppliersData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Search:   search,
		Owing:    owing,
		Rows:     rows,
	}))
}

func (a *adminUI) SuppliersTable(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.suppliers.List(ctx, c.QueryParam("search"))
	if err != nil {
		return err
	}
	if c.QueryParam("owing") == "1" {
		rows = suppliersOwing(rows)
	}
	return response.RenderFragment(c, adminpages.SupplierRows(rows, a.symbol(ctx)))
}

// suppliersOwing keeps only suppliers with an outstanding payable balance.
func suppliersOwing(rows []suppliers.Supplier) []suppliers.Supplier {
	out := rows[:0]
	for _, s := range rows {
		if s.OutstandingBalance.IsPositive() {
			out = append(out, s)
		}
	}
	return out
}

func (a *adminUI) SupplierForm(c echo.Context) error {
	ctx := c.Request().Context()
	var s *suppliers.Supplier
	if idStr := c.Param("id"); idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return apperr.BadRequest("invalid id")
		}
		if s, err = a.s.suppliers.Get(ctx, id); err != nil {
			return err
		}
	}
	return response.RenderFragment(c, adminpages.SupplierForm(s))
}

func (a *adminUI) SupplierPayForm(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	s, err := a.s.suppliers.Get(ctx, id)
	if err != nil {
		return err
	}
	invoices, err := a.s.supplierPay.OpenInvoices(ctx, id)
	if err != nil {
		return err
	}
	history, err := a.s.supplierPay.History(ctx, id)
	if err != nil {
		return err
	}
	sources, err := a.cashLocationChoices(ctx)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.SupplierPaymentForm(adminpages.SupplierPayData{
		Supplier: *s,
		Invoices: invoices,
		History:  history,
		Symbol:   a.symbol(ctx),
		Sources:  sources,
	}))
}

func (a *adminUI) SupplierCreate(c echo.Context) error {
	var in suppliers.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if _, err := a.s.suppliers.Create(c.Request().Context(), in); err != nil {
		return err
	}
	return htmxDone(c, "Supplier created", "reload-suppliers")
}

func (a *adminUI) SupplierUpdate(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in suppliers.UpdateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := a.s.suppliers.Update(c.Request().Context(), id, in); err != nil {
		return err
	}
	return htmxDone(c, "Supplier updated", "reload-suppliers")
}

func (a *adminUI) SupplierDelete(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.suppliers.Delete(c.Request().Context(), id); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionDelete, "supplier", strconv.FormatInt(id, 10), "")
	return htmxReload(c, "Supplier removed", "reload-suppliers")
}

func (a *adminUI) SupplierPay(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()

	invoices, err := a.s.supplierPay.OpenInvoices(ctx, id)
	if err != nil {
		return err
	}

	in, err := parseAllocations(c, invoices)
	if err != nil {
		return err
	}

	userID := middleware.CurrentUserID(c)
	name := ""
	if sup, gerr := a.s.suppliers.Get(ctx, id); gerr == nil {
		name = sup.Name
	}

	// A cash payment leaves a chosen cash location (locker or till) and produces a
	// receipt — booked atomically with the payment. Non-cash (card/online) just
	// records the payment with no drawer impact.
	method, _ := normSupplierMethod(in.Method)
	var src cashflow.Location
	if method == "cash" {
		src, err = parseLocation(c.FormValue("source"))
		if err != nil {
			return err
		}
	}

	var res *supplierpay.Result
	var rec *cashflow.Receipt
	err = appdb.WithTx(ctx, a.db, func(tx *sqlx.Tx) error {
		r, k, txErr := a.s.paySupplierTx(ctx, tx, payRequest{
			SupplierID: id, SupplierName: name, In: in, Source: src,
		}, userID)
		res, rec = r, k
		return txErr
	})
	if err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionPayment, "supplier", strconv.FormatInt(id, 10),
		"paid "+money.Display(res.Total)+" ("+res.Method+")")
	if rec != nil {
		return a.s.afterMoneyMove(c, rec)
	}
	return htmxDone(c, "Payment recorded", "reload-suppliers")
}

// SupplierRefund records money received back from a supplier — settling the
// credit left after goods went back, or an advance being returned.
//
// It is the mirror of SupplierPay: cash arrives from External into a chosen till
// or locker through cashflow, so the drawer, the ledger and the receipt all move
// together. supplierpay caps the amount at the credit actually owed, so a refund
// can never invent money.
func (a *adminUI) SupplierRefund(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	sup, err := a.s.suppliers.Get(ctx, id)
	if err != nil {
		return err
	}
	in, err := parseRefund(c)
	if err != nil {
		return err
	}
	userID := middleware.CurrentUserID(c)

	// Cash has to land somewhere; card/online just records the refund.
	var dest cashflow.Location
	if in.Method == "cash" {
		dest, err = parseLocation(c.FormValue("dest"))
		if err != nil {
			return err
		}
	}

	var res *supplierpay.RefundResult
	var rec *cashflow.Receipt
	err = appdb.WithTx(ctx, a.db, func(tx *sqlx.Tx) error {
		r, txErr := a.s.supplierPay.RefundTx(ctx, tx, id, in, userID)
		if txErr != nil {
			return txErr
		}
		res = r
		if r.Method != "cash" {
			return nil
		}
		k, txErr := a.s.cashflow.MoveTx(ctx, tx, cashflow.MoveInput{
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
	a.s.logAudit(c, audit.ActionPayment, "supplier", strconv.FormatInt(id, 10),
		"refund received "+money.Display(res.Amount)+" ("+res.Method+")")
	if rec != nil {
		return a.s.afterMoneyMove(c, rec)
	}
	return htmxDone(c, "Refund recorded", "reload-suppliers")
}

// parseRefund reads the refund form shared by the admin and counter dialogs.
func parseRefund(c echo.Context) (supplierpay.RefundInput, error) {
	method, ok := normSupplierMethod(c.FormValue("method"))
	if !ok {
		return supplierpay.RefundInput{}, apperr.Validation("refund method must be cash, card or online")
	}
	amt, err := money.Parse(strings.TrimSpace(c.FormValue("amount")))
	if err != nil || !amt.IsPositive() {
		return supplierpay.RefundInput{}, apperr.Validation("enter the amount received")
	}
	return supplierpay.RefundInput{
		Amount:    amt,
		Method:    method,
		Reference: strings.TrimSpace(c.FormValue("reference")),
		Note:      strings.TrimSpace(c.FormValue("note")),
	}, nil
}

// normSupplierMethod mirrors supplierpay.normMethod for the web layer's cash
// branch decision (blank → cash).
func normSupplierMethod(m string) (string, bool) {
	switch m {
	case "cash", "card", "online":
		return m, true
	case "":
		return "cash", true
	}
	return "", false
}

// ============================ Purchases (GRN) ============================

func (a *adminUI) Purchases(c echo.Context) error {
	ctx := c.Request().Context()
	drafts, err := a.s.purchases.ListDrafts(ctx)
	if err != nil {
		return err
	}
	received, err := a.s.purchases.ListReceived(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.PurchasesPage(adminpages.PurchasesData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Drafts:   drafts,
		Received: received,
	}))
}

func (a *adminUI) PurchaseDetail(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	detail, err := a.s.purchases.Get(ctx, id)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.PurchaseDetailPage(adminpages.PurchaseDetailData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Detail:   *detail,
	}))
}

func (a *adminUI) PurchaseEntry(c echo.Context) error {
	ctx := c.Request().Context()
	sups, err := a.s.suppliers.List(ctx, "")
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.PurchaseEntryPage(adminpages.PurchaseEntryData{
		UserName:   middleware.CurrentUserName(c),
		Symbol:     a.symbol(ctx),
		Suppliers:  sups,
		ConfigJSON: "null",
	}))
}

// ============================ Purchase returns ============================

func (a *adminUI) PurchaseReturns(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.purchaseReturns.List(ctx)
	if err != nil {
		return err
	}
	page := pageParam(c)
	return response.RenderPage(c, adminpages.PurchaseReturnsPage(adminpages.PurchaseReturnsData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Rows:     paginate(rows, page, reportPageSize),
		Page:     page,
		PageSize: reportPageSize,
		Total:    len(rows),
	}))
}

func (a *adminUI) PurchaseReturnDetail(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	d, err := a.s.purchaseReturns.Get(ctx, id)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.PurchaseReturnDetailPage(adminpages.PurchaseReturnDetailData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Detail:   *d,
	}))
}

func (a *adminUI) PurchaseReturnEntry(c echo.Context) error {
	ctx := c.Request().Context()
	sups, err := a.s.suppliers.List(ctx, "")
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.PurchaseReturnEntryPage(adminpages.PurchaseReturnEntryData{
		UserName:  middleware.CurrentUserName(c),
		Symbol:    a.symbol(ctx),
		Suppliers: sups,
	}))
}

// ============================ Expenses ============================

func (a *adminUI) Expenses(c echo.Context) error {
	ctx := c.Request().Context()
	from, to := c.QueryParam("from"), c.QueryParam("to")
	f := expenses.Filter{}
	if t, ok := parseDate(from); ok {
		f.From = &t
	}
	if t, ok := parseDate(to); ok {
		f.To = &t
	}
	rows, err := a.s.expenses.List(ctx, f)
	if err != nil {
		return err
	}
	total := decimal.Zero
	for _, e := range rows {
		total = total.Add(e.Amount)
	}
	return response.RenderPage(c, adminpages.ExpensesPage(adminpages.ExpensesData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Rows:     rows,
		From:     from,
		To:       to,
		Total:    total,
	}))
}

// expenseCategories returns the built-in ∪ already-used category list for the
// expense combo box.
func (a *adminUI) expenseCategories(ctx context.Context) ([]string, error) {
	distinct, err := expenses.NewRepository(a.db).DistinctCategories(ctx)
	if err != nil {
		return nil, err
	}
	return expenses.MergedCategories(distinct), nil
}

func (a *adminUI) ExpenseForm(c echo.Context) error {
	sources, err := a.cashLocationChoices(c.Request().Context())
	if err != nil {
		return err
	}
	svcs, err := a.s.products.ListServices(c.Request().Context())
	if err != nil {
		return err
	}
	cats, err := a.expenseCategories(c.Request().Context())
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.ExpenseForm(adminpages.ExpenseFormData{Sources: sources, Services: svcs, Categories: cats}))
}

func (a *adminUI) ExpenseEditForm(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	e, err := a.s.expenses.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	svcs, err := a.s.products.ListServices(c.Request().Context())
	if err != nil {
		return err
	}
	cats, err := a.expenseCategories(c.Request().Context())
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.ExpenseForm(adminpages.ExpenseFormData{Expense: e, Services: svcs, Categories: cats}))
}

func (a *adminUI) ExpenseUpdate(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in expenses.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := a.s.expenses.Update(c.Request().Context(), id, in); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionUpdate, "expense", strconv.FormatInt(id, 10), "updated expense")
	c.Response().Header().Set("HX-Trigger", response.ToastAnd("Expense updated", "success", "close-modal"))
	c.Response().Header().Set("HX-Refresh", "true")
	return c.NoContent(200)
}

func (a *adminUI) ExpenseDelete(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.expenses.Delete(c.Request().Context(), id); err != nil {
		return err
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Expense deleted", "success"))
	c.Response().Header().Set("HX-Refresh", "true")
	return c.NoContent(200)
}

func (a *adminUI) ExpenseCreate(c echo.Context) error {
	ctx := c.Request().Context()
	var in expenses.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	src, err := parseLocation(c.FormValue("source"))
	if err != nil {
		return err
	}
	userID := middleware.CurrentUserID(c)

	// Insert the expense and pay it from the chosen cash location in ONE tx, so
	// the expense, the source debit and the receipt always commit together.
	reason := strings.TrimSpace(in.Category)
	if in.Description != nil && strings.TrimSpace(*in.Description) != "" {
		reason += " - " + strings.TrimSpace(*in.Description)
	}
	var rec *cashflow.Receipt
	var expenseID int64
	err = appdb.WithTx(ctx, a.db, func(tx *sqlx.Tx) error {
		e, err := a.s.expenses.CreateInTx(ctx, tx, in, userID)
		if err != nil {
			return err
		}
		expenseID = e.ID
		r, err := a.s.cashflow.MoveTx(ctx, tx, cashflow.MoveInput{
			From:        src,
			To:          cashflow.External(),
			Amount:      e.Amount,
			Reason:      reason,
			ReceiptKind: "expense",
			Ref:         &cashflow.Ref{Kind: "expense", ID: e.ID},
			ActorID:     userID,
		})
		rec = r
		return err
	})
	if err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionCreate, "expense", strconv.FormatInt(expenseID, 10), "recorded expense paid from "+rec.FromLabel)
	return a.s.afterMoneyMove(c, rec)
}

// ============================ Finance / Profit ============================

// financeData resolves the shared range + P&L for the Finance hub sub-pages.
func (a *adminUI) financeData(c echo.Context, tab string) (adminpages.FinanceData, error) {
	ctx := c.Request().Context()
	preset := c.QueryParam("preset")
	from, to, fromStr, toStr, err := reports.ResolveRange(preset, c.QueryParam("from"), c.QueryParam("to"))
	if err != nil {
		return adminpages.FinanceData{}, err
	}
	pl, err := a.s.reports.Compute(ctx, from, to)
	if err != nil {
		return adminpages.FinanceData{}, err
	}
	return adminpages.FinanceData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		From:     fromStr,
		To:       toStr,
		Preset:   preset,
		Tab:      tab,
		PL:       *pl,
	}, nil
}

// Finance is the hub Overview: headline KPIs + a revenue/profit trend line and a
// payment-mix donut.
func (a *adminUI) Finance(c echo.Context) error {
	ctx := c.Request().Context()
	d, err := a.financeData(c, "overview")
	if err != nil {
		return err
	}
	trend, err := a.s.reports.SalesByPeriod(ctx, d.PL.From, d.PL.To, "day")
	if err != nil {
		return err
	}
	d.Trend = trend
	return response.RenderPage(c, adminpages.FinanceOverview(d))
}

// FinanceProfit shows margins, profit-by-category bars and top products.
func (a *adminUI) FinanceProfit(c echo.Context) error {
	ctx := c.Request().Context()
	d, err := a.financeData(c, "profit")
	if err != nil {
		return err
	}
	cats, err := a.s.reports.ProfitByCategory(ctx, d.PL.From, d.PL.To)
	if err != nil {
		return err
	}
	d.Cats = cats
	return response.RenderPage(c, adminpages.FinanceProfit(d))
}

// FinanceCashflow shows cash received/withdrawn, register over/short and dues.
func (a *adminUI) FinanceCashflow(c echo.Context) error {
	d, err := a.financeData(c, "cashflow")
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.FinanceCashflow(d))
}

// ============================ Categories ============================

func (a *adminUI) Categories(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.categories.Tree(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.CategoriesPage(adminpages.CategoriesData{
		UserName: middleware.CurrentUserName(c),
		Rows:     rows,
	}))
}

func (a *adminUI) CategoriesTable(c echo.Context) error {
	rows, err := a.s.categories.Tree(c.Request().Context())
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.CategoryRows(rows))
}

func (a *adminUI) CategoryForm(c echo.Context) error {
	ctx := c.Request().Context()
	all, err := a.s.categories.List(ctx)
	if err != nil {
		return err
	}
	var cur *categories.Category
	if idStr := c.Param("id"); idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return apperr.BadRequest("invalid id")
		}
		if cur, err = a.s.categories.Get(ctx, id); err != nil {
			return err
		}
	}
	return response.RenderFragment(c, adminpages.CategoryForm(all, cur))
}

func (a *adminUI) CategoryCreate(c echo.Context) error {
	var in categories.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	in.ParentID = normalizeParent(in.ParentID)
	if err := c.Validate(&in); err != nil {
		return err
	}
	if _, err := a.s.categories.Create(c.Request().Context(), in); err != nil {
		return err
	}
	return htmxDone(c, "Category created", "reload-categories")
}

// CategoryQuickCreate creates one category from inside a picker and returns it
// as JSON so the picker can select it without a page reload.
//
// Creation goes through FindOrCreateByPath — the same call the CSV product
// import uses — so asking twice for the same child selects the existing
// category instead of duplicating it.
func (a *adminUI) CategoryQuickCreate(c echo.Context) error {
	name, err := categories.CleanName(c.FormValue("name"))
	if err != nil {
		return err
	}
	ctx := c.Request().Context()

	path := name
	depth := 0
	if raw := strings.TrimSpace(c.FormValue("parent_id")); raw != "" && raw != "0" {
		pid, perr := strconv.ParseInt(raw, 10, 64)
		if perr != nil {
			return apperr.BadRequest("invalid parent category")
		}
		crumbs, aerr := a.s.products.CategoryPath(ctx, pid)
		if aerr != nil {
			return aerr
		}
		if len(crumbs) == 0 {
			return apperr.NotFound("parent category")
		}
		parts := make([]string, 0, len(crumbs)+1)
		for _, cr := range crumbs {
			parts = append(parts, cr.Name)
		}
		parts = append(parts, name)
		path = strings.Join(parts, " > ")
		depth = len(crumbs)
	}

	id, err := a.s.categories.FindOrCreateByPath(ctx, path)
	if err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionCreate, "category", strconv.FormatInt(id, 10), "created from picker: "+path)
	return response.Created(c, map[string]any{"id": id, "name": name, "depth": depth})
}

func (a *adminUI) CategoryUpdate(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in categories.UpdateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	in.ParentID = normalizeParent(in.ParentID)
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := a.s.categories.Update(c.Request().Context(), id, in); err != nil {
		return err
	}
	return htmxDone(c, "Category updated", "reload-categories")
}

func (a *adminUI) CategoryDelete(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.categories.Delete(c.Request().Context(), id); err != nil {
		return err
	}
	return htmxReload(c, "Category deleted", "reload-categories")
}

// normalizeParent turns an empty/zero "parent_id" form value into nil (top-level).
func normalizeParent(p *int64) *int64 {
	if p == nil || *p == 0 {
		return nil
	}
	return p
}

// ============================ Barcode labels ============================

func (a *adminUI) Labels(c echo.Context) error {
	ctx := c.Request().Context()
	// The page picks products through the searchable ProductPicker, so no
	// product list is loaded here.
	return response.RenderPage(c, adminpages.LabelsPage(adminpages.LabelsData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
	}))
}

// labelReq is the resolved label content shared by the browser sheet
// (LabelsPrint) and the direct TSPL print (LabelsSend).
type labelReq struct {
	ShopName, Name, Code, Format, PriceText string
	ShowPrice                               bool
	Count                                   int
}

// parseLabelReq reads the label form (works for GET query params and POST form
// bodies) and resolves it to a product label or a custom barcode label.
func (s *Server) parseLabelReq(c echo.Context, shopName, symbol string) (labelReq, error) {
	count := 12
	if n, err := strconv.Atoi(c.FormValue("qty")); err == nil && n > 0 {
		count = min(n, 200)
	}
	showPrice := c.FormValue("show_price") == "1"

	// Custom barcode: an arbitrary value typed by the user (no product record).
	if c.FormValue("custom") == "1" {
		code := strings.TrimSpace(c.FormValue("code"))
		if code == "" {
			return labelReq{}, apperr.BadRequest("enter a barcode value")
		}
		format := strings.TrimSpace(c.FormValue("format"))
		if format == "" {
			format = "CODE128"
		}
		priceText := ""
		if p := strings.TrimSpace(c.FormValue("price")); p != "" {
			priceText = symbol + " " + p
		}
		return labelReq{
			ShopName:  shopName,
			Name:      strings.TrimSpace(c.FormValue("text")),
			Code:      code,
			Format:    format,
			PriceText: priceText,
			ShowPrice: showPrice && priceText != "",
			Count:     count,
		}, nil
	}

	// From a product.
	id, err := strconv.ParseInt(c.FormValue("product_id"), 10, 64)
	if err != nil {
		return labelReq{}, apperr.BadRequest("select a product")
	}
	p, err := s.products.Get(c.Request().Context(), id)
	if err != nil {
		return labelReq{}, err
	}
	code := "SKU" + strconv.FormatInt(p.ID, 10)
	if p.Barcode != nil && *p.Barcode != "" {
		code = *p.Barcode
	}
	return labelReq{
		ShopName:  shopName,
		Name:      p.Name,
		Code:      code,
		Format:    "CODE128",
		PriceText: money.Format(symbol, p.SellingPrice),
		ShowPrice: showPrice,
		Count:     count,
	}, nil
}

// resolveLabelSize picks the sticker dimensions for this print: a "WxH" preset,
// a "custom" size from the label_w/label_h/label_gap inputs, or the shop's saved
// default. All values are clamped to sane millimetre bounds.
func resolveLabelSize(c echo.Context, defW, defH, defGap int) (w, h, gap int) {
	w, h, gap = defW, defH, defGap
	switch size := strings.TrimSpace(strings.ToLower(c.FormValue("label_size"))); size {
	case "", "default":
		// keep the saved default
	case "custom":
		if v, err := strconv.Atoi(c.FormValue("label_w")); err == nil && v > 0 {
			w = v
		}
		if v, err := strconv.Atoi(c.FormValue("label_h")); err == nil && v > 0 {
			h = v
		}
		if v, err := strconv.Atoi(c.FormValue("label_gap")); err == nil && v >= 0 {
			gap = v
		}
	default: // "WxH" preset, e.g. "40x30"
		if parts := strings.SplitN(size, "x", 2); len(parts) == 2 {
			pw, e1 := strconv.Atoi(strings.TrimSpace(parts[0]))
			ph, e2 := strconv.Atoi(strings.TrimSpace(parts[1]))
			if e1 == nil && e2 == nil && pw > 0 && ph > 0 {
				w, h = pw, ph
			}
		}
	}
	return min(max(w, 10), 200), min(max(h, 10), 200), min(max(gap, 0), 20)
}

func (a *adminUI) LabelsPrint(c echo.Context) error {
	cfg, err := a.s.settings.Get(c.Request().Context())
	if err != nil {
		return err
	}
	req, err := a.s.parseLabelReq(c, cfg.ShopName, cfg.CurrencySymbol)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.LabelSheet(adminpages.LabelSheetData{
		ShopName:  req.ShopName,
		Name:      req.Name,
		Code:      req.Code,
		PriceText: req.PriceText,
		ShowPrice: req.ShowPrice,
		Count:     req.Count,
		Format:    req.Format,
	}))
}

// sendLabel renders the label as TSPL and sends it raw to the configured label
// printer — bypassing the browser PDF path that a raw thermal queue mis-prints.
// Shared by the admin and cashier label printers. Mirrors cashier.PrintReceipt.
func (s *Server) sendLabel(c echo.Context) error {
	ctx := c.Request().Context()
	cfg, err := s.settings.Get(ctx)
	if err != nil {
		return err
	}
	req, err := s.parseLabelReq(c, cfg.ShopName, cfg.CurrencySymbol)
	if err != nil {
		return err
	}
	w, h, gap := resolveLabelSize(c, cfg.LabelWidthMM, cfg.LabelHeightMM, cfg.LabelGapMM)

	// The print target chosen in Settings (a detected printer name, a
	// "tcp://host:9100" network address, or empty = the OS default printer).
	queue := cfg.LabelPrinter
	doc := tspl.Document(tspl.Input{
		Name:      req.Name,
		Code:      req.Code,
		Format:    req.Format,
		PriceText: req.PriceText,
		ShowPrice: req.ShowPrice,
		Count:     req.Count,
		WidthMM:   w,
		HeightMM:  h,
		GapMM:     gap,
	})
	if err := printing.Raw(ctx, queue, doc); err != nil {
		return apperr.Internal("could not print labels", err)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Labels sent to printer", "success"))
	return response.OK(c, map[string]bool{"ok": true})
}

// LabelsSend (admin) prints a label directly to the configured label printer.
func (a *adminUI) LabelsSend(c echo.Context) error { return a.s.sendLabel(c) }

// ============================ Conversions ============================

func (a *adminUI) Conversions(c echo.Context) error {
	ctx := c.Request().Context()
	search := c.QueryParam("search")
	rows, err := a.s.conversions.List(ctx, search)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.ConversionsPage(adminpages.ConversionsData{
		UserName: middleware.CurrentUserName(c),
		Rows:     rows,
		Search:   search,
	}))
}

func (a *adminUI) ConversionsTable(c echo.Context) error {
	rows, err := a.s.conversions.List(c.Request().Context(), c.QueryParam("search"))
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.ConversionRows(rows))
}

// ConversionEditForm opens the recipe editor. Only the ratio and note are
// editable: changing which products a conversion joins would silently rewrite
// the meaning of every past run that points at it.
func (a *adminUI) ConversionEditForm(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	cv, err := a.s.conversions.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.ConversionEditForm(*cv))
}

func (a *adminUI) ConversionUpdate(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in conversions.UpdateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if _, err := a.s.conversions.Update(c.Request().Context(), id, in); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionUpdate, "conversion", strconv.FormatInt(id, 10), "ratio "+in.Ratio)
	return htmxDone(c, "Conversion updated", "reload-conversions")
}

// ConversionRuns is the history of every conversion actually performed — the
// audit trail for a stock change that no other screen explains.
func (a *adminUI) ConversionRuns(c echo.Context) error {
	ctx := c.Request().Context()
	var f conversions.RunFilter
	if v := c.QueryParam("conversion_id"); v != "" {
		if id, perr := strconv.ParseInt(v, 10, 64); perr == nil && id > 0 {
			f.ConversionID = &id
		}
	}
	preset := c.QueryParam("preset")
	fromStr, toStr := c.QueryParam("from"), c.QueryParam("to")
	if preset != "" {
		var rerr error
		if _, _, fromStr, toStr, rerr = reports.ResolveRange(preset, "", ""); rerr != nil {
			return rerr
		}
	}
	if t, ok := parseDate(fromStr); ok {
		f.From = &t
	}
	if t, ok := parseDate(toStr); ok {
		end := t.AddDate(0, 0, 1)
		f.To = &end
	}

	if wantsCSV(c) {
		rows, _, err := a.s.conversions.ListRuns(ctx, f) // Limit 0 = every match
		if err != nil {
			return err
		}
		out := make([][]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, []string{
				r.CreatedAt.Format("2006-01-02 15:04"), r.FromName, r.FromQty.String(), r.FromUnitAbbr,
				r.ToName, r.ToQty.String(), r.ToUnitAbbr, r.UserName,
			})
		}
		return writeCSV(c, "conversion_runs_"+time.Now().Format("2006-01-02"),
			[]string{"When", "From", "Qty out", "Unit", "To", "Qty in", "Unit", "By"}, out)
	}

	page := pageParam(c)
	f.Limit, f.Offset = reportPageSize, (page-1)*reportPageSize
	rows, total, err := a.s.conversions.ListRuns(ctx, f)
	if err != nil {
		return err
	}
	defs, err := a.s.conversions.List(ctx, "")
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.ConversionRunsPage(adminpages.ConversionRunsData{
		UserName:     middleware.CurrentUserName(c),
		Rows:         rows,
		Definitions:  defs,
		ConversionID: c.QueryParam("conversion_id"),
		Preset:       preset,
		From:         fromStr,
		To:           toStr,
		Total:        total,
		Page:         page,
		PageSize:     reportPageSize,
	}))
}

func (a *adminUI) ConversionForm(c echo.Context) error {
	// No product list is loaded: the form uses the searchable ProductPicker,
	// which queries the server as you type.
	return response.RenderFragment(c, adminpages.ConversionForm())
}

func (a *adminUI) ConversionRunForm(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	cv, err := a.s.conversions.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.ConversionRunForm(*cv))
}

func (a *adminUI) ConversionCreate(c echo.Context) error {
	var in conversions.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if _, err := a.s.conversions.Create(c.Request().Context(), in); err != nil {
		return err
	}
	return htmxDone(c, "Conversion created", "reload-conversions")
}

func (a *adminUI) ConversionDelete(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.conversions.Delete(c.Request().Context(), id); err != nil {
		return err
	}
	return htmxReload(c, "Conversion removed", "reload-conversions")
}

func (a *adminUI) ConversionRun(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	qty, err := money.Parse(c.FormValue("quantity"))
	if err != nil {
		return apperr.Validation("quantity is invalid")
	}
	if err := a.s.conversions.Run(c.Request().Context(), id, qty, middleware.CurrentUserID(c)); err != nil {
		return err
	}
	// Stock changed on two products; refresh several lists.
	c.Response().Header().Set("HX-Trigger", response.ToastAnd("Conversion done", "success", "reload-conversions", "close-modal"))
	return c.NoContent(200)
}

// ============================ Units ============================

func (a *adminUI) Units(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.units.List(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.UnitsPage(adminpages.UnitsData{
		UserName: middleware.CurrentUserName(c),
		Rows:     rows,
	}))
}

func (a *adminUI) UnitsTable(c echo.Context) error {
	rows, err := a.s.units.List(c.Request().Context())
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.UnitRows(rows))
}

func (a *adminUI) UnitForm(c echo.Context) error {
	ctx := c.Request().Context()
	var cur *units.Unit
	if idStr := c.Param("id"); idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return apperr.BadRequest("invalid id")
		}
		if cur, err = a.s.units.Get(ctx, id); err != nil {
			return err
		}
	}
	return response.RenderFragment(c, adminpages.UnitForm(cur))
}

func (a *adminUI) UnitCreate(c echo.Context) error {
	var in units.Input
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if _, err := a.s.units.Create(c.Request().Context(), in); err != nil {
		return err
	}
	return htmxDone(c, "Unit created", "reload-units-mgmt")
}

func (a *adminUI) UnitUpdate(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in units.Input
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := a.s.units.Update(c.Request().Context(), id, in); err != nil {
		return err
	}
	return htmxDone(c, "Unit updated", "reload-units-mgmt")
}

func (a *adminUI) UnitDelete(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.units.Delete(c.Request().Context(), id); err != nil {
		return err
	}
	return htmxReload(c, "Unit deleted", "reload-units-mgmt")
}

// ============================ Batches (per product) ============================

func (a *adminUI) BatchesView(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	p, err := a.s.products.Get(ctx, id)
	if err != nil {
		return err
	}
	batches, err := a.s.stock.Batches(ctx, id)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.BatchesModal(*p, batches, a.symbol(ctx)))
}

// ProductLots lists a product's live lots as JSON, for the purchase-return
// screen's "which lot is going back" picker. Admin-only and separate from the
// till's /api/products/price-options because these rows carry cost price, which
// cashiers have no business seeing.
func (a *adminUI) ProductLots(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	rows, err := a.s.stock.Batches(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.OK(c, rows)
}

// BatchPriceSet re-prices one lot from the batch modal. A blank box means "follow
// the product's current price" and is stored as zero — the same sentinel every
// lot starts life with.
func (a *adminUI) BatchPriceSet(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	price := decimal.Zero
	if s := strings.TrimSpace(c.FormValue("selling_price")); s != "" {
		v, perr := money.Parse(s)
		if perr != nil || v.IsNegative() {
			return apperr.Validation("enter a valid price, or leave it blank to follow the product")
		}
		price = v
	}
	if err := a.s.stock.SetBatchPrice(ctx, id, price); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionUpdate, "stock_batch", strconv.FormatInt(id, 10),
		"batch price set to "+price.String())
	c.Response().Header().Set("HX-Trigger", response.Toast("Batch price updated", "success"))
	return c.NoContent(http.StatusOK)
}

// ============================ Reports: expiring & low-stock ============================

func (a *adminUI) ExpiringReport(c echo.Context) error {
	ctx := c.Request().Context()
	days := 30
	if v := c.QueryParam("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			days = n
		}
	}
	rows, err := a.s.stock.Expiring(ctx, days)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.ExpiringPage(adminpages.ExpiringData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Days:     days,
		Rows:     rows,
	}))
}

func (a *adminUI) LowStockReport(c echo.Context) error {
	ctx := c.Request().Context()
	// Limit 200 used to clamp to 50, quietly hiding low-stock items from the
	// reorder worklist. Page through at the real maximum instead, and keep the
	// total so the page can say how many there are.
	page := pageParam(c)
	rows, total, err := a.s.products.List(ctx, products.ListQuery{
		LowStock: true, Limit: reportPageSize, Page: page,
	})
	if err != nil {
		return err
	}
	sups, err := a.s.suppliers.List(ctx, "")
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.LowStockPage(adminpages.LowStockData{
		UserName:  middleware.CurrentUserName(c),
		Symbol:    a.symbol(ctx),
		Rows:      rows,
		Suppliers: sups,
		Demand:    a.reorderDemand(ctx, rows),
		Total:     total,
		Page:      page,
		PageSize:  reportPageSize,
	}))
}

// reorderDemand computes a demand-based reorder hint per low-stock product:
// suggested qty = ceil(avg daily sales over the last 90 days × 14-day lead −
// on-hand) when there's sales history, plus units sold over the last week and
// month (recent velocity) and the same 90-day window a year ago (seasonality).
// Products with no recent sales get an empty Suggested so the page falls back to
// the simple 2× reorder-level rule.
func (a *adminUI) reorderDemand(ctx context.Context, rows []products.Product) map[int64]adminpages.ReorderInfo {
	const trailingDays, leadDays = 90, 14
	ids := make([]int64, 0, len(rows))
	for _, p := range rows {
		ids = append(ids, p.ID)
	}
	now := time.Now()
	sold90, err := a.s.reports.ProductQtySold(ctx, ids, now.AddDate(0, 0, -trailingDays), now)
	if err != nil {
		return nil
	}
	soldWeek, _ := a.s.reports.ProductQtySold(ctx, ids, now.AddDate(0, 0, -7), now)
	soldMonth, _ := a.s.reports.ProductQtySold(ctx, ids, now.AddDate(0, 0, -30), now)
	lyTo := now.AddDate(-1, 0, 0)
	soldLY, _ := a.s.reports.ProductQtySold(ctx, ids, lyTo.AddDate(0, 0, -trailingDays), lyTo)
	out := make(map[int64]adminpages.ReorderInfo, len(rows))
	for _, p := range rows {
		var info adminpages.ReorderInfo
		if v, ok := soldWeek[p.ID]; ok {
			info.SoldLastWeek = money.Display(v)
		}
		if v, ok := soldMonth[p.ID]; ok {
			info.SoldLastMonth = money.Display(v)
		}
		if v, ok := soldLY[p.ID]; ok {
			info.SoldLastYear = money.Display(v)
		}
		if s90, ok := sold90[p.ID]; ok && s90.IsPositive() {
			avgDaily := s90.Div(decimal.NewFromInt(trailingDays))
			need := avgDaily.Mul(decimal.NewFromInt(leadDays)).Sub(p.StockQty).Ceil()
			if need.IsNegative() {
				need = decimal.Zero
			}
			info.Suggested = need.String()
		}
		out[p.ID] = info
	}
	return out
}

// ============================ Staff Users ============================

func (a *adminUI) Users(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.auth.ListUsers(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.UsersPage(adminpages.UsersData{
		UserName: middleware.CurrentUserName(c),
		Rows:     rows,
	}))
}

func (a *adminUI) UsersTable(c echo.Context) error {
	rows, err := a.s.auth.ListUsers(c.Request().Context())
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.UserRows(rows))
}

func (a *adminUI) UserForm(c echo.Context) error {
	return response.RenderFragment(c, adminpages.UserForm(nil))
}

func (a *adminUI) UserEditForm(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	u, err := a.s.auth.GetUser(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.UserForm(u))
}

func (a *adminUI) UserUpdate(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in auth.UpdateUserInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := a.s.auth.UpdateUser(c.Request().Context(), id, in); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionUpdate, "user", strconv.FormatInt(id, 10), "updated user "+in.Name+" ("+in.Role+")")
	return htmxDone(c, "User updated", "reload-users")
}

func (a *adminUI) UserCreate(c echo.Context) error {
	var in auth.CreateUserInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if _, err := a.s.auth.CreateUser(c.Request().Context(), in); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionCreate, "user", "", "created user "+in.Name+" ("+in.Role+")")
	return htmxDone(c, "User created", "reload-users")
}

func (a *adminUI) UserDeactivate(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.auth.DeactivateUser(c.Request().Context(), id); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionDelete, "user", strconv.FormatInt(id, 10), "deactivated user")
	return htmxReload(c, "User deactivated", "reload-users")
}

func (a *adminUI) UserReactivate(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.auth.ReactivateUser(c.Request().Context(), id); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionUpdate, "user", strconv.FormatInt(id, 10), "reactivated user")
	return htmxReload(c, "User reactivated", "reload-users")
}

// ============================ Damage write-off ============================

func (a *adminUI) DamageForm(c echo.Context) error {
	return response.RenderFragment(c, adminpages.DamageForm())
}

func (a *adminUI) DamageRecord(c echo.Context) error {
	var in stock.ConsumeInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := a.s.stock.Consume(c.Request().Context(), in, middleware.CurrentUserID(c)); err != nil {
		return err
	}
	return htmxDone(c, consumeMsg[in.Reason], "reload-stock")
}

// ============================ Customer edit / pay ============================

func (a *adminUI) CustomerEditForm(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	cust, err := a.s.customers.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.CustomerEditForm(*cust))
}

func (a *adminUI) CustomerPayForm(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	cust, err := a.s.customers.Get(ctx, id)
	if err != nil {
		return err
	}
	dests, err := a.cashLocationChoices(ctx)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.CustomerPaymentForm(adminpages.CustomerPayData{
		Customer: *cust,
		Symbol:   a.symbol(ctx),
		Dests:    dests,
	}))
}

func (a *adminUI) CustomerUpdate(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in customers.UpdateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := a.s.customers.Update(c.Request().Context(), id, in); err != nil {
		return err
	}
	return htmxDone(c, "Customer updated", "reload-customers")
}

func (a *adminUI) CustomerPay(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	var in customers.PaymentInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	cust, err := a.s.customers.Get(ctx, id)
	if err != nil {
		return err
	}
	userID := middleware.CurrentUserID(c)

	method := strings.TrimSpace(in.Method)
	if method == "" {
		method = "cash"
	}
	var dest cashflow.Location
	if method == "cash" {
		dest, err = parseLocation(c.FormValue("dest"))
		if err != nil {
			return err
		}
	}

	var res *customers.PaymentResult
	err = appdb.WithTx(ctx, a.db, func(tx *sqlx.Tx) error {
		var txErr error
		res, txErr = a.s.customers.RecordPaymentTx(ctx, tx, id, in, userID)
		if txErr != nil {
			return txErr
		}
		if res.Method == "cash" {
			_, txErr = a.s.cashflow.MoveTx(ctx, tx, cashflow.MoveInput{
				From:        cashflow.External(),
				To:          dest,
				Amount:      res.Amount,
				Reason:      "credit collected: " + cust.Name,
				ReceiptKind: "customer_payment",
				Party:       cust.Name,
				Ref:         &cashflow.Ref{Kind: "customer_payment", ID: res.PaymentID},
				ActorID:     userID,
			})
			return txErr
		}
		return nil
	})
	if err != nil {
		return err
	}
	cfg, _ := a.s.settings.Get(ctx)
	if cfg != nil {
		pay := customers.CustomerPayment{
			Amount: res.Amount, Method: res.Method, CreatedAt: time.Now(),
			ReceiptNo: &res.ReceiptNo, BalanceBefore: &res.BalanceBefore, BalanceAfter: &res.BalanceAfter,
		}
		_ = printing.Raw(ctx, cfg.ReceiptPrinter, a.s.buildDebtSlip(ctx, cfg, pay, cust, middleware.CurrentUserName(c)))
	}
	a.s.logAudit(c, audit.ActionPayment, "customer", strconv.FormatInt(id, 10), "credit payment "+in.Amount)
	return htmxDone(c, "Payment recorded · "+res.ReceiptNo, "reload-customers")
}

// CustomerStatement renders a printable credit ledger for one customer.
func (a *adminUI) CustomerStatement(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	st, err := a.s.customers.Statement(ctx, id)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.CustomerStatement(adminpages.CustomerStatementData{
		ShopName: a.shopName(ctx), Symbol: a.symbol(ctx), Stmt: *st,
	}))
}

// ============================ Sale return ============================

func (a *adminUI) SaleReturn(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if _, err := a.s.sales.Return(c.Request().Context(), id, middleware.CurrentUserID(c)); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionReturn, "sale", strconv.FormatInt(id, 10), "full return")
	return htmxReload(c, "Sale returned & restocked", "reload-sales")
}

// SaleReturnForm renders the per-line partial-return modal for a sale.
func (a *adminUI) SaleReturnForm(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	detail, err := a.s.sales.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.SaleReturnForm(*detail))
}

// SalesTable renders the list region (table + pager), honoring filters + page.
func (a *adminUI) SalesTable(c echo.Context) error {
	d, err := a.salesData(c)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.SalesList(d))
}

func salesFilterFromQuery(c echo.Context) sales.ListFilter {
	f := sales.ListFilter{Limit: 200, Status: c.QueryParam("status")}
	if t, ok := parseDate(c.QueryParam("from")); ok {
		f.From = &t
	}
	if t, ok := parseDate(c.QueryParam("to")); ok {
		// make `to` inclusive of the whole day
		end := t.AddDate(0, 0, 1)
		f.To = &end
	}
	return f
}

// parseDate parses a YYYY-MM-DD string; ok is false when empty/invalid.
func parseDate(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
