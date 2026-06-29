package web

import (
	"context"
	"os"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"
	"karots-pos/templates/layouts"
	adminpages "karots-pos/templates/pages/admin"

	"github.com/labstack/echo/v4"
)

// setupStatus computes the onboarding checklist from live data: which go-live
// steps are already done and where to go to finish each one. It is best-effort —
// a failed sub-query just leaves that step "not done" rather than erroring the page.
func (a *adminUI) setupStatus(ctx context.Context) []adminpages.SetupStep {
	shopNamed, printerSet := false, false
	if cfg, err := a.s.settings.Get(ctx); err == nil {
		// "My Shop" is the migration default, not a real name — keep the step
		// open until the owner actually renames the shop.
		name := strings.TrimSpace(cfg.ShopName)
		shopNamed = name != "" && !strings.EqualFold(name, "My Shop")
		printerSet = strings.TrimSpace(cfg.ReceiptPrinter) != ""
	}

	count := func(query string, args ...any) int {
		var n int
		_ = a.db.GetContext(ctx, &n, query, args...)
		return n
	}
	systemPhone := os.Getenv("POS_SYSTEM_PHONE")
	if systemPhone == "" {
		systemPhone = "0000000001"
	}
	staff := count(`SELECT COUNT(*) FROM users WHERE is_active = true AND phone <> $1`, systemPhone)
	cats := count(`SELECT COUNT(*) FROM categories`)
	prods := count(`SELECT COUNT(*) FROM products WHERE is_active = true`)
	salesDone := count(`SELECT COUNT(*) FROM sales`)

	return []adminpages.SetupStep{
		{Label: "Name your shop", Hint: "Shop name, address & currency on the receipt", Href: "/admin/settings", Done: shopNamed},
		{Label: "Add a staff login", Hint: "Create at least one cashier or manager account", Href: "/admin/users", Done: staff > 0},
		{Label: "Create categories", Hint: "Organise your products into categories", Href: "/admin/categories", Done: cats > 0},
		{Label: "Add products", Hint: "Type them in, or bulk-import from CSV", Href: "/admin/products", Done: prods > 0},
		{Label: "Set up the receipt printer", Hint: "Pick a printer and run a test print", Href: "/admin/settings", Done: printerSet},
		{Label: "Make your first sale", Hint: "Ring up a sale at the till", Href: "/cashier", Done: salesDone > 0},
	}
}

// Setup renders the Setup hub: the onboarding checklist above the section's links.
func (a *adminUI) Setup(c echo.Context) error {
	sec, ok := layouts.SectionByKey("setup")
	if !ok {
		return apperr.NotFound("section")
	}
	return response.RenderPage(c, adminpages.SetupPage(adminpages.SetupData{
		UserName: middleware.CurrentUserName(c),
		Section:  sec,
		Steps:    a.setupStatus(c.Request().Context()),
	}))
}
