package productplus

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"karots-pos/internal/apperr"

	"github.com/a-h/templ"
)

// FieldValue pairs a field definition with the value to render for a product
// (the stored value, or the field default when the product has none).
type FieldValue struct {
	Field Field
	Value string
}

// renderProductForm returns the plugin's fragment of custom-field controls for a
// product. productID == 0 on create → every field renders its default. Fails soft
// (returns NopComponent) so a plugin error never blocks the core product form.
func (p *Plugin) renderProductForm(ctx context.Context, productID int64) (templ.Component, error) {
	fields, err := p.store.ActiveFields(ctx)
	if err != nil || len(fields) == 0 {
		return templ.NopComponent, err
	}
	values := map[int64]string{}
	if productID > 0 {
		if values, err = p.store.Values(ctx, productID); err != nil {
			return templ.NopComponent, err
		}
	}
	rows := make([]FieldValue, 0, len(fields))
	for _, f := range fields {
		v, ok := values[f.ID]
		if !ok {
			v = f.DefaultValue
		}
		rows = append(rows, FieldValue{Field: f, Value: v})
	}
	return ProductFieldsFragment(rows), nil
}

// validateProductForm blocks a save when a required custom field is blank. Fails
// open on a read error — a plugin problem must never trap a product save.
func (p *Plugin) validateProductForm(ctx context.Context, form url.Values) error {
	fields, err := p.store.ActiveFields(ctx)
	if err != nil {
		return nil
	}
	for _, f := range fields {
		val := strings.TrimSpace(form.Get("pp_" + f.Key))
		// ponytail: required enforced here (the admin product form) only; other
		// create paths (till quick-add, capture app) resolve to the default and
		// surface in the review queue. Widen only if a shop actually needs it.
		if f.Required && val == "" {
			return apperr.Validation(f.Label + " is required")
		}
		if val == "" {
			continue
		}
		// The <select>/type=number inputs constrain the UI; this is the server
		// backstop for a direct/API POST that bypasses them.
		switch f.Type {
		case "number":
			if _, err := strconv.ParseFloat(val, 64); err != nil {
				return apperr.Validation(f.Label + " must be a number")
			}
		case "select":
			if !slices.Contains(f.Options, val) {
				return apperr.Validation(val + " is not a valid " + f.Label + " option")
			}
		case "date":
			if _, err := time.Parse("2006-01-02", val); err != nil {
				return apperr.Validation(f.Label + " must be a date")
			}
		}
	}
	return nil
}

// saveProductForm persists custom values from the product POST. A row is written
// only when the value differs from the field default (absence = default); a value
// equal to the default deletes any existing row so the table stays sparse.
func (p *Plugin) saveProductForm(ctx context.Context, productID int64, form url.Values) error {
	fields, err := p.store.ActiveFields(ctx)
	if err != nil {
		return err
	}
	for _, f := range fields {
		raw, present := form["pp_"+f.Key]
		val := ""
		if present {
			val = strings.TrimSpace(raw[0])
		}
		// Bool is a checkbox: checked posts "1", unchecked posts nothing. Treat that
		// absence as an explicit No (""), NOT as "use the default" — otherwise
		// unticking a default-Yes field would silently snap back to Yes.
		if f.Type == "bool" {
			val = ""
			if present && strings.TrimSpace(raw[len(raw)-1]) == "1" {
				val = "1"
			}
			if val == f.DefaultValue {
				if derr := p.store.DeleteValue(ctx, f.ID, productID); derr != nil {
					return derr
				}
			} else if serr := p.store.SetValue(ctx, f.ID, productID, val); serr != nil {
				return serr
			}
			continue
		}
		if !present || val == f.DefaultValue {
			if derr := p.store.DeleteValue(ctx, f.ID, productID); derr != nil {
				return derr
			}
			continue
		}
		if serr := p.store.SetValue(ctx, f.ID, productID, val); serr != nil {
			return serr
		}
	}
	return nil
}
