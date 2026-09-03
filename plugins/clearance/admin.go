package clearance

import (
	"net/http"
	"strconv"

	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"

	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

type adminUI struct{ p *Plugin }

func (a *adminUI) Page(c echo.Context) error {
	ctx := c.Request().Context()
	cfg, err := a.p.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	items, err := a.p.store.StaleItems(ctx)
	if err != nil {
		return err
	}
	symbol := "Rs."
	if sc, serr := a.p.core.Settings.Get(ctx); serr == nil && sc != nil {
		symbol = sc.CurrencySymbol
	}
	rows := make([]Row, 0, len(items))
	for _, it := range items {
		pct := suggestPercent(it.Price, it.Cost, cfg.DefaultPercent, cfg.MinMarginPercent)
		days := "never sold"
		if it.DaysSinceSale != nil {
			days = strconv.Itoa(*it.DaysSinceSale) + " days"
		}
		approved := it.Status != nil && *it.Status == "approved"
		apct := ""
		if approved && it.MarkdownValue != nil {
			apct = it.MarkdownValue.String()
			pct = *it.MarkdownValue // show the approved value in the box
		}
		rows = append(rows, Row{
			ProductID:   it.ProductID,
			Name:        it.Name,
			Unit:        it.Unit,
			OnHand:      it.OnHand.String(),
			Cost:        money.Format(symbol, it.Cost),
			Price:       money.Format(symbol, it.Price),
			DaysLabel:   days,
			SuggestPct:  pct.String(),
			NewPrice:    money.Format(symbol, newPrice(it.Price, pct)),
			Approved:    approved,
			ApprovedPct: apct,
		})
	}
	return response.RenderPage(c, Page(PageData{
		UserName:         middleware.CurrentUserName(c),
		Symbol:           symbol,
		Rows:             rows,
		StaleDays:        cfg.StaleDays,
		DefaultPercent:   cfg.DefaultPercent.String(),
		MinMarginPercent: cfg.MinMarginPercent.String(),
	}))
}

func (a *adminUI) Approve(c echo.Context) error {
	pid, err := strconv.ParseInt(c.Param("pid"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	pct, err := decimal.NewFromString(c.FormValue("percent"))
	if err != nil || pct.IsNegative() {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid percent")
	}
	if err := a.p.store.Approve(c.Request().Context(), pid, "percent", pct, middleware.CurrentUserID(c)); err != nil {
		return err
	}
	return c.Redirect(http.StatusSeeOther, "/admin/clearance")
}

func (a *adminUI) Dismiss(c echo.Context) error {
	pid, err := strconv.ParseInt(c.Param("pid"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	if err := a.p.store.Dismiss(c.Request().Context(), pid, middleware.CurrentUserID(c)); err != nil {
		return err
	}
	return c.Redirect(http.StatusSeeOther, "/admin/clearance")
}

func (a *adminUI) SaveSettings(c echo.Context) error {
	days, _ := strconv.Atoi(c.FormValue("stale_days"))
	if days < 1 {
		days = 60
	}
	dp, err := decimal.NewFromString(c.FormValue("default_percent"))
	if err != nil {
		dp = decimal.NewFromInt(20)
	}
	mm, err := decimal.NewFromString(c.FormValue("min_margin_percent"))
	if err != nil {
		mm = decimal.NewFromInt(5)
	}
	if err := a.p.store.SaveSettings(c.Request().Context(), Settings{StaleDays: days, DefaultPercent: dp, MinMarginPercent: mm}); err != nil {
		return err
	}
	return c.Redirect(http.StatusSeeOther, "/admin/clearance")
}
