package web

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"karots-pos/internal/features/activity"
	"karots-pos/internal/middleware"
	"karots-pos/internal/plugin"
	"karots-pos/internal/response"
	adminpages "karots-pos/templates/pages/admin"

	"github.com/labstack/echo/v4"
)

const activityPageSize = 100

// activityRows gathers the core union rows plus every plugin contributor's rows
// for the filter, then sorts newest-first. A plugin that errors is skipped, never
// failing the whole page (same defensive stance as PLIncome).
func (a *adminUI) activityRows(ctx context.Context, f activity.Filter) ([]activity.Row, error) {
	rows, err := a.s.activity.List(ctx, f)
	if err != nil {
		return nil, err
	}
	for _, cb := range plugin.ActivityContributors() {
		// When a specific source is selected, only ask the matching contributor.
		if f.Source != "" && f.Source != "all" && f.Source != cb.Source {
			continue
		}
		pr, perr := cb.List(ctx, f)
		if perr != nil {
			continue
		}
		rows = append(rows, pr...)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].When.After(rows[j].When) })
	return rows, nil
}

// activitySources is the Source dropdown: the four core trails plus whatever
// plugins register a contributor for (inert when no plugins are loaded).
func activitySources() []adminpages.ActivitySource {
	out := []adminpages.ActivitySource{
		{ID: activity.SourceAudit, Label: "Audit"},
		{ID: activity.SourceStock, Label: "Stock"},
		{ID: activity.SourceCash, Label: "Cash drawer"},
		{ID: activity.SourceLocker, Label: "Lockers"},
	}
	for _, cb := range plugin.ActivityContributors() {
		out = append(out, adminpages.ActivitySource{ID: cb.Source, Label: titleCase(cb.Source)})
	}
	return out
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// activityFilterFromQuery reads the filter (date preset/range, who, source, text)
// off the request. The date defaults to the current month via rangeStrings, which
// keeps the in-memory merge bounded.
func (a *adminUI) activityFilterFromQuery(c echo.Context) (activity.Filter, string, string, string, error) {
	from, to, fromStr, toStr, preset, err := rangeStrings(c)
	if err != nil {
		return activity.Filter{}, "", "", "", err
	}
	f := activity.Filter{
		From:   &from,
		To:     &to,
		Source: strings.TrimSpace(c.QueryParam("source")),
		Query:  strings.TrimSpace(c.QueryParam("q")),
	}
	if v, e := strconv.ParseInt(c.QueryParam("user"), 10, 64); e == nil && v > 0 {
		f.UserID = &v
	}
	return f, preset, fromStr, toStr, nil
}

func (a *adminUI) activityData(c echo.Context) (adminpages.ActivityData, error) {
	ctx := c.Request().Context()
	f, preset, fromStr, toStr, err := a.activityFilterFromQuery(c)
	if err != nil {
		return adminpages.ActivityData{}, err
	}
	all, err := a.activityRows(ctx, f)
	if err != nil {
		return adminpages.ActivityData{}, err
	}
	page := pageParam(c)
	start := (page - 1) * activityPageSize
	hasNext := len(all) > start+activityPageSize
	var rows []activity.Row
	if start < len(all) {
		end := start + activityPageSize
		if end > len(all) {
			end = len(all)
		}
		rows = all[start:end]
	}
	users, _ := a.s.activity.Users(ctx)
	return adminpages.ActivityData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Rows:     rows,
		Users:    users,
		Sources:  activitySources(),
		Preset:   preset,
		From:     fromStr,
		To:       toStr,
		UserID:   c.QueryParam("user"),
		Source:   f.Source,
		Query:    f.Query,
		Page:     page,
		HasNext:  hasNext,
	}, nil
}

// Activity renders the unified who-did-what page.
func (a *adminUI) Activity(c echo.Context) error {
	d, err := a.activityData(c)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.ActivityPage(d))
}

// ActivityTable renders just the list region (table + pager) for HTMX swaps.
func (a *adminUI) ActivityTable(c echo.Context) error {
	d, err := a.activityData(c)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.ActivityList(d))
}

// ActivityExport streams the filtered trail (no pagination) as csv/xlsx/ods.
func (a *adminUI) ActivityExport(c echo.Context) error {
	ctx := c.Request().Context()
	f, _, _, _, err := a.activityFilterFromQuery(c)
	if err != nil {
		return err
	}
	rows, err := a.activityRows(ctx, f)
	if err != nil {
		return err
	}
	header := []string{"When", "Who", "Source", "Action", "Detail", "Amount"}
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		amt := ""
		if !r.Amount.IsZero() {
			amt = csvMoney(r.Amount)
		}
		out = append(out, []string{
			r.When.Format("2006-01-02 15:04"), r.UserName, r.Source, r.Action, r.Detail, amt,
		})
	}
	return writeSheet(c, "activity", header, out)
}
