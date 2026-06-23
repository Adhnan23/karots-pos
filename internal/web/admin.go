package web

import (
	"context"
	"io"
	"strconv"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/escpos"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/customers"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/sales"
	"karots-pos/internal/features/settings"
	"karots-pos/internal/features/stock"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/printing"
	"karots-pos/internal/receiptimg"
	"karots-pos/internal/response"
	adminfragments "karots-pos/templates/fragments/admin"
	"karots-pos/templates/layouts"
	adminpages "karots-pos/templates/pages/admin"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

type adminUI struct {
	s  *Server
	db *sqlx.DB
}

func (a *adminUI) symbol(ctx context.Context) string {
	if cfg, err := a.s.settings.Get(ctx); err == nil {
		return cfg.CurrencySymbol
	}
	return "Rs."
}

// sectionHub renders the generic landing page for an admin section (Sell,
// Inventory, Purchasing, Money, Setup). Reports keeps its own richer ReportsHub.
func (a *adminUI) sectionHub(key string) echo.HandlerFunc {
	return func(c echo.Context) error {
		sec, ok := layouts.SectionByKey(key)
		if !ok {
			return apperr.NotFound("section")
		}
		return response.RenderPage(c, adminpages.SectionHub(adminpages.SectionHubData{
			UserName: middleware.CurrentUserName(c),
			Section:  sec,
		}))
	}
}

func (a *adminUI) Dashboard(c echo.Context) error {
	ctx := c.Request().Context()
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	tomorrow := start.AddDate(0, 0, 1)

	todays, err := a.s.sales.List(ctx, sales.ListFilter{From: &start, Limit: 500})
	if err != nil {
		return err
	}
	// Today's headline figure mirrors the P&L: net revenue (gross − returns),
	// excluding voids — so the dashboard and the finance report never disagree.
	pl, err := a.s.reports.Compute(ctx, start, tomorrow)
	if err != nil {
		return err
	}
	recent := todays
	if len(recent) > 8 {
		recent = recent[:8]
	}

	_, lowStock, err := a.s.products.List(ctx, products.ListQuery{LowStock: true, Limit: 100})
	if err != nil {
		return err
	}

	var due decimal.Decimal
	_ = a.db.GetContext(ctx, &due, `SELECT COALESCE(SUM(outstanding_balance),0) FROM customers`)

	expiring, err := a.s.stock.Expiring(ctx, 30)
	if err != nil {
		return err
	}

	reviewCount, err := a.s.products.CountNeedsReview(ctx)
	if err != nil {
		return err
	}

	return response.RenderPage(c, adminpages.Dashboard(adminpages.DashboardData{
		UserName:       middleware.CurrentUserName(c),
		Symbol:         a.symbol(ctx),
		TodayCount:     pl.SalesCount,
		TodayTotal:     pl.Revenue,
		LowStockCount:  lowStock,
		ExpiringCount:  len(expiring),
		OutstandingDue: due,
		ReviewCount:    reviewCount,
		Recent:         recent,
		Setup:          a.setupStatus(ctx),
	}))
}

// --- products ---

const productPageSize = 20

// productsData builds the products view model for a given request, handling the
// search/category filters and page-based pagination (fetches one extra row to
// know whether a next page exists).
func (a *adminUI) productsData(c echo.Context) (adminpages.ProductsData, error) {
	ctx := c.Request().Context()
	page := 1
	if v, err := strconv.Atoi(c.QueryParam("page")); err == nil && v > 1 {
		page = v
	}
	search := c.QueryParam("search")
	catParam := c.QueryParam("category_id")

	q := products.ListQuery{Search: search, Page: page, Limit: productPageSize + 1}
	if catParam != "" {
		if id, err := strconv.ParseInt(catParam, 10, 64); err == nil {
			q.CategoryID = &id
		}
	}
	rows, _, err := a.s.products.List(ctx, q)
	if err != nil {
		return adminpages.ProductsData{}, err
	}
	hasNext := len(rows) > productPageSize
	if hasNext {
		rows = rows[:productPageSize]
	}
	cats, err := a.s.categories.Tree(ctx)
	if err != nil {
		return adminpages.ProductsData{}, err
	}
	return adminpages.ProductsData{
		UserName:   middleware.CurrentUserName(c),
		Symbol:     a.symbol(ctx),
		Rows:       rows,
		Categories: cats,
		Page:       page,
		HasNext:    hasNext,
		Search:     search,
		CategoryID: catParam,
	}, nil
}

func (a *adminUI) Products(c echo.Context) error {
	d, err := a.productsData(c)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.ProductsPage(d))
}

func (a *adminUI) ProductsTable(c echo.Context) error {
	d, err := a.productsData(c)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.ProductList(d))
}

func (a *adminUI) ProductForm(c echo.Context) error {
	ctx := c.Request().Context()
	cats, err := a.s.categories.Tree(ctx)
	if err != nil {
		return err
	}
	us, err := a.s.units.List(ctx)
	if err != nil {
		return err
	}
	sups, err := a.s.suppliers.List(ctx, "")
	if err != nil {
		return err
	}
	var p *products.Product
	if idStr := c.Param("id"); idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return apperr.BadRequest("invalid id")
		}
		if p, err = a.s.products.Get(ctx, id); err != nil {
			return err
		}
	}
	return response.RenderFragment(c, adminfragments.ProductForm(p, cats, us, sups))
}

func (a *adminUI) ProductCreate(c echo.Context) error {
	var in products.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	p, err := a.s.products.Create(c.Request().Context(), in)
	if err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionCreate, "product", strconv.FormatInt(p.ID, 10), "created "+in.Name)
	return htmxDone(c, "Product created", "reload-products")
}

func (a *adminUI) ProductUpdate(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in products.UpdateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	ctx := c.Request().Context()
	p, err := a.s.products.Update(ctx, id, in)
	if err != nil {
		return err
	}
	msg := "Product updated"
	// Finishing a quick-added item: clear the review flag and correct the
	// placeholder cost 0 on its past sale lines so historical COGS/profit are right.
	if p.NeedsReview {
		_ = a.s.products.MarkReviewed(ctx, id)
		msg = "Item finalized & removed from review"
		if n, berr := a.s.products.BackfillCost(ctx, id, in.CostPrice); berr == nil && n > 0 {
			msg = "Item finalized — corrected cost on " + strconv.FormatInt(n, 10) + " past sale line(s)"
		}
		a.s.logAudit(c, audit.ActionUpdate, "product", strconv.FormatInt(id, 10), "finalized quick-add "+in.Name)
		return htmxDone(c, msg, "reload-products")
	}
	a.s.logAudit(c, audit.ActionUpdate, "product", strconv.FormatInt(id, 10), "updated "+in.Name)
	return htmxDone(c, msg, "reload-products")
}

// ProductReview lists items quick-added at the till that still need an admin to
// finish them (real category/unit/cost + a true stock count). It's reachable from
// the dashboard banner and the Inventory nav.
func (a *adminUI) ProductReview(c echo.Context) error {
	rows, err := a.s.products.NeedsReview(c.Request().Context())
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.ReviewPage(adminpages.ReviewData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(c.Request().Context()),
		Rows:     rows,
	}))
}

// ProductReviewTable is the HTMX-refreshed body of the review list.
func (a *adminUI) ProductReviewTable(c echo.Context) error {
	rows, err := a.s.products.NeedsReview(c.Request().Context())
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.ReviewRows(adminpages.ReviewData{
		Symbol: a.symbol(c.Request().Context()),
		Rows:   rows,
	}))
}

// ProductReviewCount renders the small count badge loaded over HTMX into the
// sidebar "Items to Review" link (empty when nothing needs review).
func (a *adminUI) ProductReviewCount(c echo.Context) error {
	n, err := a.s.products.CountNeedsReview(c.Request().Context())
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.ReviewCountBadge(n))
}

// ProductReviewDone clears the review flag without editing — for items the admin
// has confirmed are fine as-is (kept simple; cost backfill happens when they
// actually edit the cost via the product form).
func (a *adminUI) ProductReviewDone(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.products.MarkReviewed(c.Request().Context(), id); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionUpdate, "product", strconv.FormatInt(id, 10), "marked reviewed")
	return htmxReload(c, "Marked reviewed", "reload-products")
}

func (a *adminUI) ProductDelete(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.products.Delete(c.Request().Context(), id); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionDelete, "product", strconv.FormatInt(id, 10), "")
	return htmxReload(c, "Product deleted", "reload-products")
}

// --- stock ---

func (a *adminUI) Stock(c echo.Context) error {
	ctx := c.Request().Context()
	prods, _, err := a.s.products.List(ctx, products.ListQuery{Limit: 100})
	if err != nil {
		return err
	}
	moves, err := a.s.stock.Movements(ctx, nil, c.QueryParam("type"), 50)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.StockPage(adminpages.StockData{
		UserName:   middleware.CurrentUserName(c),
		Products:   prods,
		Movements:  moves,
		MoveType:   c.QueryParam("type"),
	}))
}

func (a *adminUI) StockTable(c echo.Context) error {
	moves, err := a.s.stock.Movements(c.Request().Context(), nil, c.QueryParam("type"), 50)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.MovementRows(moves))
}

// StockLevels re-renders just the on-hand rows so the Stock Levels table can
// refresh in place after an adjustment/damage (reload-stock trigger), instead of
// going stale until a full page reload.
func (a *adminUI) StockLevels(c echo.Context) error {
	prods, _, err := a.s.products.List(c.Request().Context(), products.ListQuery{Limit: 100})
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.StockLevelRows(prods))
}

// StockMovements is the full stock-movement history: every in/out with who did
// it and why, filterable by product and type. Unlike the stock-overview page
// (capped at 50, no filter), this is the audit trail.
func (a *adminUI) StockMovements(c echo.Context) error {
	ctx := c.Request().Context()
	var pid *int64
	pidStr := c.QueryParam("product_id")
	if pidStr != "" {
		if id, perr := strconv.ParseInt(pidStr, 10, 64); perr == nil {
			pid = &id
		}
	}
	mtype := c.QueryParam("type")
	moves, err := a.s.stock.Movements(ctx, pid, mtype, 300)
	if err != nil {
		return err
	}
	prods, _, err := a.s.products.List(ctx, products.ListQuery{Limit: 1000})
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.StockMovementsPage(adminpages.StockMovementsData{
		UserName:  middleware.CurrentUserName(c),
		Products:  prods,
		Movements: moves,
		MoveType:  mtype,
		ProductID: pidStr,
	}))
}

func (a *adminUI) StockForm(c echo.Context) error {
	prods, _, err := a.s.products.List(c.Request().Context(), products.ListQuery{Limit: 100})
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.StockForm(prods))
}

func (a *adminUI) StockAdjust(c echo.Context) error {
	var in stock.AdjustInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := a.s.stock.Adjust(c.Request().Context(), in, middleware.CurrentUserID(c)); err != nil {
		return err
	}
	return htmxDone(c, "Stock adjusted", "reload-stock")
}

// StockTake is the bulk opening-stock / stock-take screen: a (search-filterable)
// list of products, each with a counted-quantity box and an optional cost box.
// It's how a shop already running enters the stock it owned before this system —
// no fake supplier/purchase needed.
func (a *adminUI) StockTake(c echo.Context) error {
	d, err := a.stockTakeData(c, -1)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.StockTakePage(d))
}

func (a *adminUI) stockTakeData(c echo.Context, saved int) (adminpages.StockTakeData, error) {
	ctx := c.Request().Context()
	search := strings.TrimSpace(c.FormValue("search"))
	page := 1
	if v, err := strconv.Atoi(c.FormValue("page")); err == nil && v > 1 {
		page = v
	}
	prods, _, err := a.s.products.List(ctx, products.ListQuery{Search: search, Page: page, Limit: stockTakePageSize + 1})
	if err != nil {
		return adminpages.StockTakeData{}, err
	}
	hasNext := len(prods) > stockTakePageSize
	if hasNext {
		prods = prods[:stockTakePageSize]
	}
	return adminpages.StockTakeData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Rows:     prods,
		Search:   search,
		Page:     page,
		HasNext:  hasNext,
		Saved:    saved,
	}, nil
}

const stockTakePageSize = 50

// StockTakeApply reads the per-row qty_<id> (and optional cost_<id>) fields and
// applies each as an absolute count via the audited stock.Adjust path. Setting
// the cost first means the opening batch is valued correctly. Rows left blank are
// skipped, so the admin can save a section at a time.
func (a *adminUI) StockTakeApply(c echo.Context) error {
	ctx := c.Request().Context()
	uid := middleware.CurrentUserID(c)
	form, err := c.FormParams()
	if err != nil {
		return apperr.BadRequest("invalid form")
	}
	saved := 0
	for key, vals := range form {
		if !strings.HasPrefix(key, "qty_") || len(vals) == 0 {
			continue
		}
		idStr := strings.TrimPrefix(key, "qty_")
		id, perr := strconv.ParseInt(idStr, 10, 64)
		if perr != nil {
			continue
		}
		qty := strings.TrimSpace(vals[0])
		if qty == "" {
			continue
		}
		target, perr := money.Parse(qty)
		if perr != nil {
			continue
		}
		// Skip rows whose count is unchanged so "Saved N" reflects real edits.
		if cur, cerr := a.s.stock.Quantity(ctx, id); cerr == nil && cur.Equal(target) {
			continue
		}
		// Set cost first (if entered) so the opening batch is valued correctly.
		if costStr := strings.TrimSpace(c.FormValue("cost_" + idStr)); costStr != "" {
			if cost, cerr := money.Parse(costStr); cerr == nil {
				_ = a.s.products.SetCost(ctx, id, cost)
			}
		}
		if err := a.s.stock.Adjust(ctx, stock.AdjustInput{ProductID: id, NewQuantity: qty, Note: "stock-take"}, uid); err == nil {
			saved++
		}
	}
	d, err := a.stockTakeData(c, saved)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.StockTakePage(d))
}

// --- sales ---

const salesPageSize = 25

func (a *adminUI) salesData(c echo.Context) (adminpages.SalesData, error) {
	ctx := c.Request().Context()
	page := 1
	if v, err := strconv.Atoi(c.QueryParam("page")); err == nil && v > 1 {
		page = v
	}
	f := salesFilterFromQuery(c)
	f.Limit = salesPageSize + 1
	f.Offset = (page - 1) * salesPageSize
	rows, err := a.s.sales.List(ctx, f)
	if err != nil {
		return adminpages.SalesData{}, err
	}
	hasNext := len(rows) > salesPageSize
	if hasNext {
		rows = rows[:salesPageSize]
	}
	return adminpages.SalesData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Rows:     rows,
		From:     c.QueryParam("from"),
		To:       c.QueryParam("to"),
		Status:   c.QueryParam("status"),
		Page:     page,
		HasNext:  hasNext,
	}, nil
}

func (a *adminUI) Sales(c echo.Context) error {
	d, err := a.salesData(c)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.SalesPage(d))
}

// --- customers ---

func (a *adminUI) Customers(c echo.Context) error {
	ctx := c.Request().Context()
	search := c.QueryParam("search")
	rows, err := a.s.customers.ListAll(ctx, search)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.CustomersPage(adminpages.CustomersData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Search:   search,
		Rows:     rows,
	}))
}

func (a *adminUI) CustomersTable(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.customers.ListAll(ctx, c.QueryParam("search"))
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.CustomerRows(rows, a.symbol(ctx)))
}

func (a *adminUI) CustomerForm(c echo.Context) error {
	return response.RenderFragment(c, adminpages.CustomerForm())
}

func (a *adminUI) CustomerCreate(c echo.Context) error {
	var in customers.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if _, err := a.s.customers.Create(c.Request().Context(), in); err != nil {
		return err
	}
	return htmxDone(c, "Customer created", "reload-customers")
}

func (a *adminUI) CustomerDelete(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.customers.Delete(c.Request().Context(), id); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionDelete, "customer", strconv.FormatInt(id, 10), "")
	return htmxReload(c, "Customer deactivated", "reload-customers")
}

func (a *adminUI) CustomerReactivate(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.customers.Reactivate(c.Request().Context(), id); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionUpdate, "customer", strconv.FormatInt(id, 10), "reactivated")
	return htmxReload(c, "Customer reactivated", "reload-customers")
}

// --- settings ---

func (a *adminUI) Settings(c echo.Context) error {
	ctx := c.Request().Context()
	cfg, err := a.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.SettingsPage(adminpages.SettingsData{
		UserName: middleware.CurrentUserName(c),
		S:        *cfg,
		Queues:   printing.Queues(ctx),
	}))
}

// LogoUpload accepts an image file, downscales it, and stores it in the DB as a
// self-contained data URI so the logo prints and previews fully offline.
func (a *adminUI) LogoUpload(c echo.Context) error {
	fh, err := c.FormFile("logo")
	if err != nil {
		return apperr.BadRequest("choose an image file")
	}
	f, err := fh.Open()
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 8<<20))
	if err != nil {
		return err
	}
	uri, err := receiptimg.ToDataURI(data, 400)
	if err != nil {
		return apperr.BadRequest("that file is not a valid image")
	}
	if err := a.s.settings.SetLogo(c.Request().Context(), uri); err != nil {
		return err
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Logo uploaded", "success"))
	return response.OK(c, map[string]bool{"ok": true})
}

// LogoClear removes the uploaded logo.
func (a *adminUI) LogoClear(c echo.Context) error {
	if err := a.s.settings.SetLogo(c.Request().Context(), ""); err != nil {
		return err
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("Logo removed", "success"))
	return response.OK(c, map[string]bool{"ok": true})
}

// PrinterTest sends a short test slip to the configured receipt printer so the
// owner can verify printer wiring during setup, without ringing up a real sale.
// It reports the outcome as a toast either way. It uses the saved printer, so the
// owner must save a new printer choice before testing it.
func (a *adminUI) PrinterTest(c echo.Context) error {
	ctx := c.Request().Context()
	cfg, err := a.s.settings.Get(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.ReceiptPrinter) == "" {
		c.Response().Header().Set("HX-Trigger", response.Toast("Set and save a receipt printer first", "error"))
		return c.NoContent(200)
	}
	if err := escpos.Send(ctx, cfg.ReceiptPrinter, escpos.TestDocument(*cfg)); err != nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("Test print failed: "+err.Error(), "error"))
		return c.NoContent(200)
	}
	a.s.logAudit(c, audit.ActionSettings, "settings", "", "sent printer test")
	c.Response().Header().Set("HX-Trigger", response.Toast("Test slip sent to "+cfg.ReceiptPrinter, "success"))
	return c.NoContent(200)
}

func (a *adminUI) SettingsUpdate(c echo.Context) error {
	var in settings.UpdateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	// A network-printer address (tcp://host:9100), if given, overrides the
	// dropdown selection for that job.
	if net := strings.TrimSpace(in.ReceiptPrinterNet); net != "" {
		in.ReceiptPrinter = net
	}
	if net := strings.TrimSpace(in.LabelPrinterNet); net != "" {
		in.LabelPrinter = net
	}
	if _, err := a.s.settings.Update(c.Request().Context(), in); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionSettings, "settings", "", "updated shop settings")
	c.Response().Header().Set("HX-Trigger", response.Toast("Settings saved", "success"))
	return c.NoContent(200)
}

// htmxDone closes the open modal, refreshes the relevant list, and toasts.
func htmxDone(c echo.Context, msg, reloadEvent string) error {
	c.Response().Header().Set("HX-Trigger", response.ToastAnd(msg, "success", reloadEvent, "close-modal"))
	return c.NoContent(200)
}

// htmxReload refreshes a list and toasts without closing a modal (e.g. delete).
func htmxReload(c echo.Context, msg, reloadEvent string) error {
	c.Response().Header().Set("HX-Trigger", response.ToastAnd(msg, "success", reloadEvent))
	return c.NoContent(200)
}
