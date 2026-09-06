package aicatalog

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"karots-pos/internal/features/products"
	"karots-pos/internal/features/stock"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"

	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

type adminUI struct{ p *Plugin }

// wizard is the accumulating quick-add state. The persistent fields ride hidden
// form inputs from step to step; the rest are transient per-step display values.
type wizard struct {
	Query       string
	Name        string
	Category    string
	Specs       string
	Explanation string
	Source      string
	Barcode     string
	CostPrice   string
	Selling     string
	Qty         string
	// transient display
	Markup    string
	Warn      string
	AIError   string
	Confident bool
	Options   []Candidate
}

// readWizard pulls the persistent fields every step re-posts.
func readWizard(c echo.Context) wizard {
	w := wizard{
		Query:       strings.TrimSpace(c.FormValue("query")),
		Name:        strings.TrimSpace(c.FormValue("name")),
		Category:    strings.TrimSpace(c.FormValue("category")),
		Specs:       strings.TrimSpace(c.FormValue("specs")),
		Explanation: strings.TrimSpace(c.FormValue("explanation")),
		Source:      strings.TrimSpace(c.FormValue("source")),
		Barcode:     strings.TrimSpace(c.FormValue("barcode")),
		CostPrice:   strings.TrimSpace(c.FormValue("cost_price")),
		Selling:     strings.TrimSpace(c.FormValue("selling_price")),
		Qty:         strings.TrimSpace(c.FormValue("qty")),
	}
	if w.Source == "" {
		w.Source = "user_picked"
	}
	return w
}

func (a *adminUI) Page(c echo.Context) error {
	ctx := c.Request().Context()
	cfg, err := a.p.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	d := PageData{
		UserName:      middleware.CurrentUserName(c),
		Provider:      cfg.Provider,
		BaseURL:       cfg.BaseURL,
		Model:         cfg.Model,
		HasKey:        cfg.APIKey != "",
		DefaultMarkup: cfg.DefaultMarkup.String(),
	}
	if run, _ := a.p.store.LatestRun(ctx); run != nil {
		d.HasLastRun = true
		d.LastRunSummary = run.Summary
	}
	return response.RenderPage(c, Page(d))
}

// OptimizePreview asks the AI for a tidy-up plan and renders it for approval.
func (a *adminUI) OptimizePreview(c echo.Context) error {
	ctx := c.Request().Context()
	cfg, err := a.p.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	cats, err := a.p.store.OptimizeCategories(ctx)
	if err != nil {
		return err
	}
	prods, err := a.p.store.OptimizeProducts(ctx, 300)
	if err != nil {
		return err
	}
	plan, aerr := NewClient(cfg).Optimize(ctx, cats, prods)
	if aerr != nil {
		return response.RenderFragment(c, OptimizeError(aerr.Error()))
	}
	d := OptimizeData{Plan: plan, HasChanges: !plan.empty(), CatName: map[int64]string{}, ProdName: map[int64]string{}}
	for _, x := range cats {
		d.CatName[x.ID] = x.Name
	}
	for _, x := range prods {
		d.ProdName[x.ID] = x.Name
	}
	pj, _ := json.Marshal(plan)
	d.PlanJSON = string(pj)
	return response.RenderFragment(c, OptimizePreview(d))
}

// OptimizeApply applies the plan the preview posted back, recording undo.
func (a *adminUI) OptimizeApply(c echo.Context) error {
	plan, err := parseOptimizePlan([]byte(c.FormValue("plan")))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid plan")
	}
	summary, err := a.p.store.ApplyPlan(c.Request().Context(), plan, middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	return response.RenderFragment(c, OptimizeResult(summary, true),
		response.Toast("Optimised: "+summary, "success"))
}

// OptimizeRevert undoes the most recent Optimize run.
func (a *adminUI) OptimizeRevert(c echo.Context) error {
	summary, err := a.p.store.Revert(c.Request().Context())
	if err != nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("Revert failed: "+err.Error(), "error"))
		return response.NoContent(c)
	}
	return response.RenderFragment(c, OptimizeResult("Reverted ("+summary+")", false),
		response.Toast("Reverted: "+summary, "success"))
}

func (a *adminUI) SaveSettings(c echo.Context) error {
	ctx := c.Request().Context()
	cur, err := a.p.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	cur.Provider = c.FormValue("provider")
	cur.BaseURL = c.FormValue("base_url")
	cur.Model = c.FormValue("model")
	// Only overwrite the key when a new one is typed (the form shows a
	// placeholder, never the stored key), so saving other fields doesn't wipe it.
	if k := c.FormValue("api_key"); k != "" {
		cur.APIKey = k
	}
	if m, e := decimal.NewFromString(c.FormValue("default_markup")); e == nil && m.GreaterThan(decimal.NewFromInt(1)) {
		cur.DefaultMarkup = m
	}
	if err := a.p.store.SaveSettings(ctx, cur); err != nil {
		return err
	}
	return c.Redirect(http.StatusSeeOther, "/admin/ai-catalog")
}

func (a *adminUI) TestKey(c echo.Context) error {
	cfg, err := a.p.store.GetSettings(c.Request().Context())
	if err != nil {
		return err
	}
	if err := NewClient(cfg).Ping(c.Request().Context()); err != nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("AI test failed: "+err.Error(), "error"))
		return response.NoContent(c)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("AI key works ✓", "success"))
	return response.NoContent(c)
}

// Identify: step 1 -> options.
func (a *adminUI) Identify(c echo.Context) error {
	ctx := c.Request().Context()
	w := readWizard(c)
	if w.Query == "" {
		return response.RenderFragment(c, StepIdentifyError("Type a name or part number."))
	}
	cfg, err := a.p.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	res, aerr := NewClient(cfg).Identify(ctx, w.Query, c.FormValue("hint"))
	if aerr != nil {
		// AI down / no key: let the owner type it manually.
		w.AIError = aerr.Error()
		return response.RenderFragment(c, StepOptions(w))
	}
	if res.Confident {
		w.Confident = true
		w.Options = []Candidate{res.Best}
	} else {
		w.Options = res.Options
	}
	return response.RenderFragment(c, StepOptions(w))
}

// Pick: a chosen option (or the "explain" box) -> category step.
func (a *adminUI) Pick(c echo.Context) error {
	w := readWizard(c)
	if ex := strings.TrimSpace(c.FormValue("explain")); ex != "" && w.Name == "" {
		w.Name = ex
		w.Explanation = ex
		w.Source = "user_explained"
	}
	if c.FormValue("confident") == "1" {
		w.Source = "ai_confident"
	}
	if w.Name == "" {
		w.Name = w.Query
	}
	return response.RenderFragment(c, StepCategory(w))
}

// Category: accept/edit the suggested category -> barcode step.
func (a *adminUI) Category(c echo.Context) error {
	w := readWizard(c)
	if v := strings.TrimSpace(c.FormValue("name_in")); v != "" {
		w.Name = v
	}
	if v := strings.TrimSpace(c.FormValue("category_in")); v != "" {
		w.Category = v
	}
	return response.RenderFragment(c, StepBarcode(w))
}

// Barcode: scan (dup-checked) or generate -> price step.
func (a *adminUI) Barcode(c echo.Context) error {
	ctx := c.Request().Context()
	w := readWizard(c)
	code := strings.TrimSpace(c.FormValue("barcode_in"))
	switch {
	case c.FormValue("action") == "generate" || code == "":
		gen, err := a.p.core.Products.GenerateBarcode(ctx)
		if err != nil {
			return err
		}
		w.Barcode = gen
	default:
		if _, err := a.p.core.Products.GetByBarcode(ctx, code); err == nil {
			// already used by another product
			w.Warn = "That barcode is already used — scan another or Generate one."
			w.Barcode = ""
		} else {
			w.Barcode = code
		}
	}
	w.Markup = a.markup(ctx)
	return response.RenderFragment(c, StepPrice(w))
}

// Price: cost direct, or selling + markup -> qty step.
func (a *adminUI) Price(c echo.Context) error {
	w := readWizard(c)
	cost := strings.TrimSpace(c.FormValue("cost_in"))
	selling := strings.TrimSpace(c.FormValue("selling_in"))
	if cost == "" && selling != "" {
		if sell, e1 := decimal.NewFromString(selling); e1 == nil {
			if mk, e2 := decimal.NewFromString(strings.TrimSpace(c.FormValue("markup"))); e2 == nil {
				if v, err := costFromMarkup(sell, mk); err == nil {
					cost = v.String()
				}
			}
		}
	}
	w.CostPrice = cost
	w.Selling = selling
	return response.RenderFragment(c, StepQty(w))
}

// Qty -> labels step.
func (a *adminUI) Qty(c echo.Context) error {
	w := readWizard(c)
	w.Qty = strings.TrimSpace(c.FormValue("qty_in"))
	return response.RenderFragment(c, StepLabels(w))
}

// Save: the one place that writes to the catalog.
func (a *adminUI) Save(c echo.Context) error {
	ctx := c.Request().Context()
	w := readWizard(c)
	if w.Name == "" {
		return response.RenderFragment(c, StepIdentifyError("Nothing to save — start again."))
	}
	// FindOrCreateByPath nests on ">"; accept "/" too and normalise so the AI's
	// (or the user's) "Bearings/Deep Groove" becomes a real parent>child tree
	// instead of one flat top-level category.
	catPath := strings.ReplaceAll(w.Category, "/", ">")
	catID, err := a.p.cats.FindOrCreateByPath(ctx, catPath)
	if err != nil || catID <= 0 {
		if catID, err = a.p.cats.FindOrCreateByPath(ctx, "Uncategorized"); err != nil {
			return err
		}
	}
	unitID, err := a.defaultUnitID(ctx)
	if err != nil {
		return err
	}
	// Final barcode safety: never assign a taken code; generate if blank/taken.
	barcode := strings.TrimSpace(c.FormValue("barcode"))
	if barcode != "" {
		if _, e := a.p.core.Products.GetByBarcode(ctx, barcode); e == nil {
			barcode = ""
		}
	}
	if barcode == "" {
		if barcode, err = a.p.core.Products.GenerateBarcode(ctx); err != nil {
			return err
		}
	}
	p, err := a.p.core.Products.Create(ctx, products.CreateInput{
		Name:         w.Name,
		Barcode:      strPtr(barcode),
		CategoryID:   catID,
		UnitID:       unitID,
		CostPrice:    c.FormValue("cost_price"),
		SellingPrice: c.FormValue("selling_price"),
	})
	if err != nil {
		return err
	}
	// Set qty on hand via a stock adjustment (mirrors internal/web/intake.go).
	if qty := strings.TrimSpace(c.FormValue("qty")); qty != "" {
		if n, e := decimal.NewFromString(qty); e == nil && n.IsPositive() {
			if e := a.p.core.Stock.Adjust(ctx, stock.AdjustInput{
				ProductID:    p.ID,
				NewQuantity:  n.String(),
				Note:         "AI catalog intake",
				SellingPrice: c.FormValue("selling_price"),
			}, middleware.CurrentUserID(c)); e != nil {
				return e
			}
		}
	}
	// Learn once, so a later Optimize pass never re-asks what this is.
	_ = a.p.store.LearnItem(ctx, LearnedItem{
		ProductID: p.ID, ResolvedName: w.Name, SuggestedCategory: w.Category,
		Specs: w.Specs, UserExplanation: w.Explanation, Source: w.Source, RawQuery: w.Query,
	})
	// Reset to step 1; fire a label print for N labels via the core endpoint.
	trigger := response.Toast("Saved: "+p.Name, "success")
	if labels := strings.TrimSpace(c.FormValue("labels")); labels != "" && labels != "0" {
		trigger = response.ToastAnd("Saved: "+p.Name, "success",
			`{"ai-print-labels":{"product_id":`+strconv.FormatInt(p.ID, 10)+`,"qty":"`+labels+`"}}`)
	}
	return response.RenderFragment(c, StepIdentify(), trigger)
}

func (a *adminUI) markup(ctx context.Context) string {
	if cfg, err := a.p.store.GetSettings(ctx); err == nil {
		return cfg.DefaultMarkup.String()
	}
	return "1.4"
}

func (a *adminUI) defaultUnitID(ctx context.Context) (int64, error) {
	var id int64
	err := a.p.core.DB.GetContext(ctx, &id,
		`SELECT id FROM units ORDER BY (abbreviation = 'pcs') DESC, id ASC LIMIT 1`)
	return id, err
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func itoa(i int) string { return strconv.Itoa(i) }
