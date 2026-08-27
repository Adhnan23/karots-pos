package productplus

import (
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"

	"github.com/labstack/echo/v4"
)

// adminUI hosts the plugin's own admin field-manager page.
type adminUI struct{ p *Plugin }

// audit records a field-manager change on the shared audit trail (no-op if the
// core audit service is absent, e.g. in tests).
func (a *adminUI) audit(c echo.Context, action, key, detail string) {
	if a.p.core.Audit != nil {
		a.p.core.Audit.Record(c.Request().Context(), middleware.CurrentUserID(c), action, "product_field", key, detail)
	}
}

// Page renders the field-manager shell. "Show disabled" is off by default,
// matching the app's other admin lists.
func (a *adminUI) Page(c echo.Context) error {
	showDisabled := c.QueryParam("show_disabled") != ""
	fields, err := a.p.store.Fields(c.Request().Context(), showDisabled)
	if err != nil {
		return err
	}
	return response.RenderPage(c, FieldsPage(middleware.CurrentUserName(c), fields, showDisabled))
}

// Table re-renders just the fields table (the create/edit/toggle reload target).
func (a *adminUI) Table(c echo.Context) error {
	showDisabled := c.QueryParam("show_disabled") != ""
	fields, err := a.p.store.Fields(c.Request().Context(), showDisabled)
	if err != nil {
		return err
	}
	return response.RenderFragment(c, FieldsTable(fields, showDisabled))
}

// FieldForm opens the create (no id) or edit (id) modal.
func (a *adminUI) FieldForm(c echo.Context) error {
	var f *Field
	if idStr := c.Param("id"); idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return apperr.BadRequest("invalid id")
		}
		if f, err = a.p.store.Field(c.Request().Context(), id); err != nil {
			return err
		}
	}
	return response.RenderFragment(c, FieldForm(f))
}

// parseField reads the field form (shared by create/edit). It does NOT set the
// key (create slugs a new one; edit keeps the existing key).
func parseField(c echo.Context) (Field, error) {
	f := Field{
		Label:          strings.TrimSpace(c.FormValue("label")),
		Type:           strings.TrimSpace(c.FormValue("type")),
		DefaultValue:   strings.TrimSpace(c.FormValue("default_value")),
		Hint:           strings.TrimSpace(c.FormValue("hint")),
		Required:       c.FormValue("required") != "",
		Searchable:     c.FormValue("searchable") != "",
		ShowAtTill:     c.FormValue("show_at_till") != "",
		PrintOnLabel:   c.FormValue("print_on_label") != "",
		PrintOnReceipt: c.FormValue("print_on_receipt") != "",
	}
	if f.Label == "" {
		return f, apperr.Validation("label is required")
	}
	switch f.Type {
	case "text", "textarea", "number", "bool", "select", "date":
	default:
		return f, apperr.Validation("choose a field type")
	}
	if f.Type == "select" {
		for _, line := range strings.Split(c.FormValue("options"), "\n") {
			if o := strings.TrimSpace(line); o != "" {
				f.Options = append(f.Options, o)
			}
		}
		if len(f.Options) == 0 {
			return f, apperr.Validation("a dropdown needs at least one option")
		}
	}
	// A required field must carry a default: existing products have no stored value
	// and resolve to it, so without one they'd silently read blank. Bool is exempt —
	// an empty default legitimately means "No".
	if f.Required && f.Type != "bool" && f.DefaultValue == "" {
		return f, apperr.Validation("a required field needs a default value so existing products back-fill from it")
	}
	return f, nil
}

func (a *adminUI) CreateField(c echo.Context) error {
	ctx := c.Request().Context()
	f, err := parseField(c)
	if err != nil {
		return err
	}
	f.Key, err = a.uniqueKey(c, slugify(f.Label))
	if err != nil {
		return err
	}
	// New fields append to the end of the order (existing fields are numbered
	// 0..n-1 by the ▲▼ reorder, so n places this last).
	if f.SortOrder, err = a.p.store.FieldCount(ctx); err != nil {
		return err
	}
	if _, err := a.p.store.CreateField(ctx, f); err != nil {
		return err
	}
	a.audit(c, audit.ActionCreate, f.Key, "added custom field "+f.Label+" ("+f.Type+")")
	c.Response().Header().Set("HX-Trigger", response.ToastAnd(f.Label+" added", "success", "reload-ppfields", "close-modal"))
	return response.NoContent(c)
}

func (a *adminUI) UpdateField(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	f, err := parseField(c)
	if err != nil {
		return err
	}
	f.ID = id
	if err := a.p.store.UpdateField(ctx, f); err != nil {
		return err
	}
	a.audit(c, audit.ActionUpdate, f.Key, "updated custom field "+f.Label)
	c.Response().Header().Set("HX-Trigger", response.ToastAnd(f.Label+" saved", "success", "reload-ppfields", "close-modal"))
	return response.NoContent(c)
}

// MoveField shifts a field up or down one place in the order (the ▲▼ buttons),
// then reloads the table via the shared reload-ppfields event.
func (a *adminUI) MoveField(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := a.p.store.MoveField(c.Request().Context(), id, c.FormValue("dir") == "up"); err != nil {
		return err
	}
	c.Response().Header().Set("HX-Trigger", "reload-ppfields")
	return response.NoContent(c)
}

func (a *adminUI) SetActive(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	active := c.FormValue("active") == "1"
	if err := a.p.store.SetFieldActive(c.Request().Context(), id, active); err != nil {
		return err
	}
	msg := "Field disabled"
	if active {
		msg = "Field enabled"
	}
	a.audit(c, audit.ActionUpdate, strconv.FormatInt(id, 10), msg)
	c.Response().Header().Set("HX-Trigger", response.ToastAnd(msg, "success", "reload-ppfields"))
	return response.NoContent(c)
}

// uniqueKey returns base, or base_2, base_3, … until it finds a free key.
func (a *adminUI) uniqueKey(c echo.Context, base string) (string, error) {
	ctx := c.Request().Context()
	key := base
	for i := 2; ; i++ {
		exists, err := a.p.store.KeyExists(ctx, key)
		if err != nil {
			return "", err
		}
		if !exists {
			return key, nil
		}
		key = base + "_" + strconv.Itoa(i)
	}
}

// slugify turns a label into a stable form field key (lowercase, alnum + "_").
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		out = "field"
	}
	return out
}
