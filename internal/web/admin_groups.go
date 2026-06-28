package web

import (
	"context"
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/productgroups"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"
	adminfragments "karots-pos/templates/fragments/admin"
	adminpages "karots-pos/templates/pages/admin"

	"github.com/labstack/echo/v4"
)

// Groups renders the admin "Cashier Menu" page: the group tree + a detail panel.
func (a *adminUI) Groups(c echo.Context) error {
	ctx := c.Request().Context()
	tree, err := a.s.groups.Tree(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, adminpages.GroupsPage(adminpages.GroupsData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Tree:     tree,
	}))
}

// GroupsTree is the HTMX-refreshed tree fragment.
func (a *adminUI) GroupsTree(c echo.Context) error {
	tree, err := a.s.groups.Tree(c.Request().Context())
	if err != nil {
		return err
	}
	return response.RenderFragment(c, adminpages.GroupTree(tree))
}

// GroupForm returns the create/edit modal. ?parent=<id> pre-selects a parent for
// a new sub-group; /form/:id loads an existing group for editing.
func (a *adminUI) GroupForm(c echo.Context) error {
	ctx := c.Request().Context()
	tree, err := a.s.groups.Tree(ctx)
	if err != nil {
		return err
	}
	opts := groupParentOptions(tree)
	var g *productgroups.Group
	if idStr := c.Param("id"); idStr != "" {
		id, perr := strconv.ParseInt(idStr, 10, 64)
		if perr != nil {
			return apperr.BadRequest("invalid id")
		}
		if g, err = a.s.groups.Get(ctx, id); err != nil {
			return err
		}
	}
	return response.RenderFragment(c, adminpages.GroupForm(g, c.QueryParam("parent"), opts))
}

func (a *adminUI) GroupCreate(c echo.Context) error {
	in := productgroups.CreateInput{
		Name:     strings.TrimSpace(c.FormValue("name")),
		Emoji:    emojiPtr(c, "emoji"),
		ParentID: parentIDPtr(c.FormValue("parent_id")),
	}
	id, err := a.s.groups.Create(c.Request().Context(), in)
	if err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionCreate, "product_group", strconv.FormatInt(id, 10), "created group "+in.Name)
	return htmxDone(c, "Group created", "reload-groups")
}

func (a *adminUI) GroupUpdate(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	in := productgroups.UpdateInput{
		Name:  strings.TrimSpace(c.FormValue("name")),
		Emoji: emojiPtr(c, "emoji"),
	}
	if err := a.s.groups.Update(c.Request().Context(), id, in); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionUpdate, "product_group", strconv.FormatInt(id, 10), "updated group "+in.Name)
	return htmxDone(c, "Group updated", "reload-groups")
}

func (a *adminUI) GroupDelete(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.groups.Delete(c.Request().Context(), id); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionDelete, "product_group", strconv.FormatInt(id, 10), "")
	return htmxReload(c, "Group deleted", "reload-groups")
}

func (a *adminUI) GroupMove(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.s.groups.Move(c.Request().Context(), id, c.QueryParam("dir")); err != nil {
		return err
	}
	return htmxReload(c, "Reordered", "reload-groups")
}

// GroupItems renders the right-hand panel for one group.
func (a *adminUI) GroupItems(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	return a.renderGroupItems(c, ctx, id, false)
}

func (a *adminUI) GroupItemAdd(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	productID, perr := strconv.ParseInt(c.FormValue("product_id"), 10, 64)
	if perr != nil || productID <= 0 {
		return apperr.Validation("choose a product to add")
	}
	if err := a.s.groups.LinkProduct(ctx, id, productID, emojiPtr(c, "emoji")); err != nil {
		return err
	}
	a.s.logAudit(c, audit.ActionUpdate, "product_group", strconv.FormatInt(id, 10),
		"linked product "+strconv.FormatInt(productID, 10))
	return a.renderGroupItems(c, ctx, id, true)
}

func (a *adminUI) GroupItemRemove(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	productID, perr := strconv.ParseInt(c.Param("productId"), 10, 64)
	if perr != nil {
		return apperr.BadRequest("invalid product id")
	}
	if err := a.s.groups.UnlinkProduct(ctx, id, productID); err != nil {
		return err
	}
	return a.renderGroupItems(c, ctx, id, true)
}

func (a *adminUI) GroupItemEmoji(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	productID, perr := strconv.ParseInt(c.Param("productId"), 10, 64)
	if perr != nil {
		return apperr.BadRequest("invalid product id")
	}
	if err := a.s.groups.SetItemEmoji(ctx, id, productID, emojiPtr(c, "emoji")); err != nil {
		return err
	}
	return c.NoContent(200)
}

// renderGroupItems renders the items panel for a group; when refreshTree is set it
// also triggers a tree reload so item counts stay current.
func (a *adminUI) renderGroupItems(c echo.Context, ctx context.Context, id int64, refreshTree bool) error {
	g, err := a.s.groups.Get(ctx, id)
	if err != nil {
		return err
	}
	items, err := a.s.groups.Products(ctx, id)
	if err != nil {
		return err
	}
	if refreshTree {
		c.Response().Header().Set("HX-Trigger", "reload-groups")
	}
	return response.RenderFragment(c, adminpages.GroupItems(adminpages.GroupItemsData{
		Symbol: a.symbol(ctx),
		Group:  *g,
		Items:  items,
	}))
}

// groupParentOptions builds the indented parent choices for the group form.
func groupParentOptions(tree []productgroups.Group) []adminfragments.PickerOption {
	children := map[int64][]productgroups.Group{}
	var roots []productgroups.Group
	for _, g := range tree {
		if g.ParentID == nil {
			roots = append(roots, g)
		} else {
			children[*g.ParentID] = append(children[*g.ParentID], g)
		}
	}
	var out []adminfragments.PickerOption
	var walk func(g productgroups.Group, depth int)
	walk = func(g productgroups.Group, depth int) {
		out = append(out, adminfragments.PickerOption{ID: g.ID, Label: strings.Repeat("— ", depth) + g.Name})
		for _, c := range children[g.ID] {
			walk(c, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	return out
}

// emojiPtr returns nil for a blank emoji field, else a trimmed pointer.
func emojiPtr(c echo.Context, field string) *string {
	v := strings.TrimSpace(c.FormValue(field))
	if v == "" {
		return nil
	}
	return &v
}

// parentIDPtr parses a parent_id form value to *int64 (nil when blank/zero).
func parentIDPtr(v string) *int64 {
	id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	return &id
}
