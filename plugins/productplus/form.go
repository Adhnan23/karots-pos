package productplus

import (
	"context"
	"net/url"
	"strings"

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
		// ponytail: required enforced here (the admin product form) only; other
		// create paths (CSV import, till quick-add, capture app) resolve to the
		// default. Widen to those paths only if a shop actually needs it.
		if f.Required && strings.TrimSpace(form.Get("pp_"+f.Key)) == "" {
			return apperr.Validation(f.Label + " is required")
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
