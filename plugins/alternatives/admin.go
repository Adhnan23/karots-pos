package alternatives

import (
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/products"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"

	"github.com/labstack/echo/v4"
)

// adminUI hosts the plugin's admin pages: the groups list, one group's detail
// (tiers + members), and (in report.go) the reorder report.
type adminUI struct{ p *Plugin }

func (a *adminUI) audit(c echo.Context, action, entity, entityID, detail string) {
	if a.p.core.Audit != nil {
		a.p.core.Audit.Record(c.Request().Context(), middleware.CurrentUserID(c), action, entity, entityID, detail)
	}
}

func atoiID(s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, apperr.BadRequest("invalid id")
	}
	return id, nil
}

// --- groups ---

func (a *adminUI) Page(c echo.Context) error {
	showDisabled := c.QueryParam("show_disabled") != ""
	rows, err := a.p.store.GroupSummaries(c.Request().Context(), showDisabled)
	if err != nil {
		return err
	}
	return response.RenderPage(c, GroupsPage(middleware.CurrentUserName(c), rows, showDisabled))
}

func (a *adminUI) GroupForm(c echo.Context) error {
	return response.RenderFragment(c, GroupForm(nil))
}

func parseGroup(c echo.Context) (Group, error) {
	g := Group{Name: strings.TrimSpace(c.FormValue("name"))}
	g.SortOrder, _ = strconv.Atoi(strings.TrimSpace(c.FormValue("sort_order")))
	if g.Name == "" {
		return g, apperr.Validation("group name is required")
	}
	return g, nil
}

func (a *adminUI) CreateGroup(c echo.Context) error {
	g, err := parseGroup(c)
	if err != nil {
		return err
	}
	id, err := a.p.store.CreateGroup(c.Request().Context(), g)
	if err != nil {
		return err
	}
	a.audit(c, audit.ActionCreate, "alt_group", strconv.FormatInt(id, 10), "created group "+g.Name)
	c.Response().Header().Set("HX-Trigger", response.ToastAnd(g.Name+" added", "success", "reload-altgroups", "close-modal"))
	return response.NoContent(c)
}

func (a *adminUI) UpdateGroup(c echo.Context) error {
	id, err := atoiID(c.Param("id"))
	if err != nil {
		return err
	}
	g, err := parseGroup(c)
	if err != nil {
		return err
	}
	g.ID = id
	if err := a.p.store.UpdateGroup(c.Request().Context(), g); err != nil {
		return err
	}
	a.audit(c, audit.ActionUpdate, "alt_group", strconv.FormatInt(id, 10), "renamed group to "+g.Name)
	c.Response().Header().Set("HX-Trigger", response.ToastAnd("Group saved", "success", "reload-altgroup"))
	return response.NoContent(c)
}

func (a *adminUI) SetGroupActive(c echo.Context) error {
	id, err := atoiID(c.Param("id"))
	if err != nil {
		return err
	}
	active := c.FormValue("active") == "1"
	if err := a.p.store.SetGroupActive(c.Request().Context(), id, active); err != nil {
		return err
	}
	msg := "Group disabled"
	if active {
		msg = "Group enabled"
	}
	a.audit(c, audit.ActionUpdate, "alt_group", strconv.FormatInt(id, 10), msg)
	c.Response().Header().Set("HX-Trigger", response.ToastAnd(msg, "success", "reload-altgroups"))
	return response.NoContent(c)
}

// --- group detail (tiers + members) ---

func (a *adminUI) GroupDetail(c echo.Context) error {
	id, err := atoiID(c.Param("id"))
	if err != nil {
		return err
	}
	g, err := a.p.store.Group(c.Request().Context(), id)
	if err != nil {
		return err
	}
	views, err := a.tierViews(c, id)
	if err != nil {
		return err
	}
	return response.RenderPage(c, GroupDetailPage(middleware.CurrentUserName(c), *g, views))
}

// TiersSection re-renders just the tiers block (the reload target after any change).
func (a *adminUI) TiersSection(c echo.Context) error {
	id, err := atoiID(c.Param("id"))
	if err != nil {
		return err
	}
	views, err := a.tierViews(c, id)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, TiersSection(id, views))
}

// TierView is a tier plus its member rows, for the detail page.
type TierView struct {
	Tier    Tier
	Members []MemberRow
}

func (a *adminUI) tierViews(c echo.Context, groupID int64) ([]TierView, error) {
	ctx := c.Request().Context()
	tiers, err := a.p.store.Tiers(ctx, groupID)
	if err != nil {
		return nil, err
	}
	views := make([]TierView, 0, len(tiers))
	for _, t := range tiers {
		members, err := a.p.store.TierMembers(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		views = append(views, TierView{Tier: t, Members: members})
	}
	return views, nil
}

// --- tiers ---

func parseTier(c echo.Context, groupID int64) (Tier, error) {
	t := Tier{GroupID: groupID, Name: strings.TrimSpace(c.FormValue("name"))}
	t.ReorderLevel, _ = strconv.Atoi(strings.TrimSpace(c.FormValue("reorder_level")))
	t.SortOrder, _ = strconv.Atoi(strings.TrimSpace(c.FormValue("sort_order")))
	if t.Name == "" {
		return t, apperr.Validation("tier name is required")
	}
	if t.ReorderLevel < 0 {
		return t, apperr.Validation("reorder level cannot be negative")
	}
	return t, nil
}

func (a *adminUI) CreateTier(c echo.Context) error {
	gid, err := atoiID(c.Param("id"))
	if err != nil {
		return err
	}
	t, err := parseTier(c, gid)
	if err != nil {
		return err
	}
	id, err := a.p.store.CreateTier(c.Request().Context(), t)
	if err != nil {
		return err
	}
	a.audit(c, audit.ActionCreate, "alt_tier", strconv.FormatInt(id, 10), "added tier "+t.Name)
	c.Response().Header().Set("HX-Trigger", response.ToastAnd(t.Name+" added", "success", "reload-altgroup"))
	return response.NoContent(c)
}

func (a *adminUI) UpdateTier(c echo.Context) error {
	tid, err := atoiID(c.Param("tid"))
	if err != nil {
		return err
	}
	cur, err := a.p.store.Tier(c.Request().Context(), tid)
	if err != nil {
		return err
	}
	t, err := parseTier(c, cur.GroupID)
	if err != nil {
		return err
	}
	t.ID = tid
	if err := a.p.store.UpdateTier(c.Request().Context(), t); err != nil {
		return err
	}
	a.audit(c, audit.ActionUpdate, "alt_tier", strconv.FormatInt(tid, 10), "updated tier "+t.Name)
	c.Response().Header().Set("HX-Trigger", response.ToastAnd("Tier saved", "success", "reload-altgroup"))
	return response.NoContent(c)
}

func (a *adminUI) SetTierActive(c echo.Context) error {
	tid, err := atoiID(c.Param("tid"))
	if err != nil {
		return err
	}
	active := c.FormValue("active") == "1"
	if err := a.p.store.SetTierActive(c.Request().Context(), tid, active); err != nil {
		return err
	}
	msg := "Tier disabled"
	if active {
		msg = "Tier enabled"
	}
	a.audit(c, audit.ActionUpdate, "alt_tier", strconv.FormatInt(tid, 10), msg)
	c.Response().Header().Set("HX-Trigger", response.ToastAnd(msg, "success", "reload-altgroup"))
	return response.NoContent(c)
}

// --- members ---

// ProductSearch renders matching products as an "Add" list for a tier. Products
// already in THIS tier are skipped; those in another group get a "move" hint.
func (a *adminUI) ProductSearch(c echo.Context) error {
	tid, err := atoiID(c.Param("tid"))
	if err != nil {
		return err
	}
	q := strings.TrimSpace(c.QueryParam("q"))
	if q == "" {
		return response.RenderFragment(c, ProductResults(tid, nil))
	}
	rows, _, err := a.p.core.Products.List(c.Request().Context(), products.ListQuery{Search: q, Page: 1, Limit: 20})
	if err != nil {
		return err
	}
	return response.RenderFragment(c, ProductResults(tid, rows))
}

func (a *adminUI) AddMember(c echo.Context) error {
	ctx := c.Request().Context()
	tid, err := atoiID(c.Param("tid"))
	if err != nil {
		return err
	}
	pid, err := atoiID(c.FormValue("product_id"))
	if err != nil {
		return err
	}
	confirm := c.FormValue("confirm") == "1"

	cur, isMember, err := a.p.store.MemberTier(ctx, pid)
	if err != nil {
		return err
	}
	if isMember && cur == tid {
		// Already here: nothing to do, just refresh.
		c.Response().Header().Set("HX-Trigger", response.ToastAnd("Already in this tier", "info", "reload-altgroup"))
		return response.NoContent(c)
	}
	if isMember && cur != tid && !confirm {
		// Belongs to another group — ask to confirm the move (swaps the Add button).
		return response.RenderFragment(c, MoveConfirmButton(tid, pid))
	}

	if err := a.p.store.AddMember(ctx, pid, tid); err != nil {
		return err
	}
	msg := "Product added"
	if isMember {
		msg = "Product moved here"
	}
	a.audit(c, audit.ActionUpdate, "alt_member", strconv.FormatInt(pid, 10), msg+" (tier "+strconv.FormatInt(tid, 10)+")")
	c.Response().Header().Set("HX-Trigger", response.ToastAnd(msg, "success", "reload-altgroup"))
	return response.NoContent(c)
}

func (a *adminUI) RemoveMember(c echo.Context) error {
	pid, err := atoiID(c.Param("pid"))
	if err != nil {
		return err
	}
	if err := a.p.store.RemoveMember(c.Request().Context(), pid); err != nil {
		return err
	}
	a.audit(c, audit.ActionUpdate, "alt_member", strconv.FormatInt(pid, 10), "removed from group")
	c.Response().Header().Set("HX-Trigger", response.ToastAnd("Removed", "success", "reload-altgroup"))
	return response.NoContent(c)
}
