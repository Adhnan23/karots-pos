package web

import (
	"context"
	"strconv"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/customers"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/sales"
	"karots-pos/internal/features/settings"
	"karots-pos/internal/features/stock"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"
	adminfragments "karots-pos/templates/fragments/admin"
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

func (a *adminUI) Dashboard(c echo.Context) error {
	ctx := c.Request().Context()
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	todays, err := a.s.sales.List(ctx, sales.ListFilter{From: &start, Limit: 500})
	if err != nil {
		return err
	}
	total := decimal.Zero
	for _, s := range todays {
		total = total.Add(s.Total)
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

	return response.RenderPage(c, adminpages.Dashboard(adminpages.DashboardData{
		UserName:       middleware.CurrentUserName(c),
		Symbol:         a.symbol(ctx),
		TodayCount:     len(todays),
		TodayTotal:     total,
		LowStockCount:  lowStock,
		ExpiringCount:  len(expiring),
		OutstandingDue: due,
		Recent:         recent,
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
	cats, err := a.s.categories.List(ctx)
	if err != nil {
		return err
	}
	us, err := a.s.units.List(ctx)
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
	return response.RenderFragment(c, adminfragments.ProductForm(p, cats, us))
}

func (a *adminUI) ProductCreate(c echo.Context) error {
	var in products.CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if _, err := a.s.products.Create(c.Request().Context(), in); err != nil {
		return err
	}
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
	if _, err := a.s.products.Update(c.Request().Context(), id, in); err != nil {
		return err
	}
	return htmxDone(c, "Product updated", "reload-products")
}

func (a *adminUI) ProductDelete(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.products.Delete(c.Request().Context(), id); err != nil {
		return err
	}
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
	rows, err := a.s.customers.List(ctx, "")
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.CustomersPage(adminpages.CustomersData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Rows:     rows,
	}))
}

func (a *adminUI) CustomersTable(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.customers.List(ctx, "")
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
	}))
}

func (a *adminUI) SettingsUpdate(c echo.Context) error {
	var in settings.UpdateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid form")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if _, err := a.s.settings.Update(c.Request().Context(), in); err != nil {
		return err
	}
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
