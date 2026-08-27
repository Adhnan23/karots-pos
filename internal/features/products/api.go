package products

import (
	"strconv"

	"karots-pos/internal/apperr"
	"karots-pos/internal/config"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

type APIHandler struct{ svc *Service }

func NewAPIHandler(svc *Service) *APIHandler { return &APIHandler{svc: svc} }

func (h *APIHandler) List(c echo.Context) error {
	var q ListQuery
	if err := c.Bind(&q); err != nil {
		return apperr.BadRequest("invalid query parameters")
	}
	q.Normalize()
	ctx := c.Request().Context()
	rows, total, err := h.svc.List(ctx, q)
	if err != nil {
		return err
	}
	if BadgeProvider != nil && len(rows) > 0 {
		ids := make([]int64, len(rows))
		for i := range rows {
			ids[i] = rows[i].ID
		}
		if m := BadgeProvider(ctx, ids); m != nil {
			for i := range rows {
				rows[i].Badges = m[rows[i].ID]
			}
		}
	}
	return response.Paged(c, rows, response.NewPageMeta(q.Page, q.Limit, total))
}

// SyncCatalog serves the read-only LAN catalog snapshot for the stock_capture
// app: all active products with full category path, current qty and price. Any
// signed-in user (same posture as List); the app can only read, never write.
func (h *APIHandler) SyncCatalog(c echo.Context) error {
	rows, err := h.svc.SyncCatalog(c.Request().Context())
	if err != nil {
		return err
	}
	return response.OK(c, rows)
}

func (h *APIHandler) Get(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	p, err := h.svc.Get(c.Request().Context(), id)
	if err != nil {
		return err
	}
	if BadgeProvider != nil && p != nil {
		if m := BadgeProvider(c.Request().Context(), []int64{p.ID}); m != nil {
			p.Badges = m[p.ID]
		}
	}
	return response.OK(c, p)
}

// ProductDetail is the till info-popup payload: core facts plus plugin rows.
// Cost and margin are present only when the request may see cost (server-side
// gate) so a forbidden cashier never receives them.
type ProductDetail struct {
	ID           int64       `json:"id"`
	Name         string      `json:"name"`
	Category     string      `json:"category"`
	Barcode      string      `json:"barcode,omitempty"`
	Unit         string      `json:"unit"`
	SellingPrice string      `json:"selling_price"`
	StockQty     string      `json:"stock_qty"`
	Warranty     string      `json:"warranty,omitempty"`
	ShowCost     bool        `json:"show_cost"`
	CostPrice    string      `json:"cost_price,omitempty"`
	MarginPct    string      `json:"margin_pct,omitempty"`
	Rows         []DetailRow `json:"rows,omitempty"`
}

// Detail serves the till product-info popup. Any signed-in user may read the
// facts; cost/margin are added only when MaySeeCost passes for this user.
func (h *APIHandler) Detail(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	ctx := c.Request().Context()
	p, err := h.svc.Get(ctx, id)
	if err != nil {
		return err
	}
	d := ProductDetail{
		ID:           p.ID,
		Name:         p.Name,
		Category:     p.CategoryName,
		Unit:         p.UnitAbbr,
		SellingPrice: p.SellingPrice.StringFixed(2),
		StockQty:     p.StockQty.String(),
	}
	if p.Barcode != nil {
		d.Barcode = *p.Barcode
	}
	if p.WarrantyMonths > 0 {
		d.Warranty = strconv.Itoa(p.WarrantyMonths) + " month warranty"
	}
	if middleware.MaySeeCost(middleware.CurrentRole(c), middleware.CanSeeCost(c)) {
		d.ShowCost = true
		d.CostPrice = p.CostPrice.StringFixed(2)
		// Margin as a % of the selling price; skip when either side is zero.
		if p.SellingPrice.IsPositive() && p.CostPrice.IsPositive() {
			m := p.SellingPrice.Sub(p.CostPrice).Div(p.SellingPrice).Mul(decimal.NewFromInt(100))
			d.MarginPct = m.StringFixed(1)
		}
	}
	if DetailContributor != nil {
		d.Rows = DetailContributor(ctx, p.ID)
	}
	return response.OK(c, d)
}

func (h *APIHandler) GetByBarcode(c echo.Context) error {
	p, err := h.svc.GetByBarcode(c.Request().Context(), c.Param("code"))
	if err != nil {
		return err
	}
	return response.OK(c, p)
}

// PriceOptions serves the till's batch price map (see Service.PriceOptions).
// Available to any signed-in user because cashiers are exactly who needs it.
func (h *APIHandler) PriceOptions(c echo.Context) error {
	opts, err := h.svc.PriceOptions(c.Request().Context())
	if err != nil {
		return err
	}
	return response.OK(c, opts)
}

// QuickPicks serves the till's dynamic top-menu shortcut rows (frequent + recent
// sellers). Any signed-in user, because cashiers are exactly who taps them.
func (h *APIHandler) QuickPicks(c echo.Context) error {
	picks, err := h.svc.QuickPicks(c.Request().Context())
	if err != nil {
		return err
	}
	return response.OK(c, picks)
}

// Lots serves one product's live lots for the lot pickers on stock-removal
// screens. Any signed-in user: cashiers write off damage too, and the payload
// carries no cost price.
func (h *APIHandler) Lots(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	rows, err := h.svc.LotsFor(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return response.OK(c, rows)
}

// GenerateBarcode returns a fresh, unused EAN-13 for the "Generate" button next
// to barcode inputs (product form + label pages).
func (h *APIHandler) GenerateBarcode(c echo.Context) error {
	code, err := h.svc.GenerateBarcode(c.Request().Context())
	if err != nil {
		return err
	}
	return response.OK(c, map[string]string{"barcode": code})
}

// AssignBarcode saves a barcode onto a product that currently has none, powering
// the label pages' "Generate barcode" action. It is available to any signed-in
// user (no manage role) because AssignBarcode only fills an *empty* barcode with
// a generated, unused code — it can never overwrite an existing one.
func (h *APIHandler) AssignBarcode(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := h.svc.AssignBarcode(c.Request().Context(), id, c.FormValue("barcode")); err != nil {
		return err
	}
	return response.OK(c, map[string]string{"barcode": c.FormValue("barcode")})
}

func (h *APIHandler) Create(c echo.Context) error {
	var in CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	p, err := h.svc.Create(c.Request().Context(), in)
	if err != nil {
		return err
	}
	return response.Created(c, p)
}

func (h *APIHandler) Update(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in UpdateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	p, err := h.svc.Update(c.Request().Context(), id, in)
	if err != nil {
		return err
	}
	return response.OK(c, p)
}

func (h *APIHandler) Delete(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := h.svc.Delete(c.Request().Context(), id); err != nil {
		return err
	}
	return response.NoContent(c)
}

func RegisterAPI(e *echo.Echo, db *sqlx.DB, cfg *config.Config) {
	api := NewAPIHandler(NewService(db))
	jwt := middleware.JWTAuth(cfg.JWTSecret)
	manage := middleware.RequireRole("admin", "manager")

	g := e.Group("/api/products", jwt)
	g.GET("", api.List)
	g.GET("/:id", api.Get)
	g.GET("/:id/detail", api.Detail)
	g.GET("/price-options", api.PriceOptions)
	g.GET("/quick-picks", api.QuickPicks)
	g.GET("/:id/lots", api.Lots)
	g.GET("/barcode/generate", api.GenerateBarcode)
	g.GET("/barcode/:code", api.GetByBarcode)
	g.POST("/:id/barcode", api.AssignBarcode)
	g.POST("", api.Create, manage)
	g.PUT("/:id", api.Update, manage)
	g.DELETE("/:id", api.Delete, manage)

	// LAN catalog sync for the stock_capture app: a single read-only snapshot
	// endpoint on its own path. Reuses the same JWT; the app can only read.
	e.GET("/api/sync/catalog", api.SyncCatalog, jwt)
}
