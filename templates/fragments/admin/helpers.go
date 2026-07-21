package adminfragments

import (
	"encoding/json"
	"strconv"
	"strings"

	"karots-pos/internal/features/categories"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/suppliers"
	"karots-pos/internal/features/units"
)

// categoryPickerData builds the x-data init string for the categoryPicker Alpine
// component (static/js/app.js): the category options as JSON plus the picker
// config. Used by both the product form and the products filter.
func categoryPickerData(nodes []categories.TreeNode, fieldName, selected string, includeAll bool, allLabel string, reload, allowCreate bool) string {
	type opt struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Depth int    `json:"depth"`
	}
	opts := make([]opt, 0, len(nodes))
	for _, n := range nodes {
		opts = append(opts, opt{ID: n.ID, Name: n.Name, Depth: n.Depth})
	}
	cfg := map[string]any{
		"name":        fieldName,
		"selected":    selected,
		"options":     opts,
		"includeAll":  includeAll,
		"allLabel":    allLabel,
		"reload":      reload,
		"allowCreate": allowCreate,
	}
	b, _ := json.Marshal(cfg)
	return "categoryPicker(" + string(b) + ")"
}

// ImportRowError is one failed CSV row, reported with its 1-based line number.
type ImportRowError struct {
	Line    int
	Message string
}

// ImportSummary is the outcome of a bulk CSV import, shown after upload.
type ImportSummary struct {
	Created int
	Updated int
	Skipped int
	Notes   []string
	Errors  []ImportRowError
}

// ImportConfig parameterizes the generic bulk-import modal (ImportModal) so the
// same dialog serves products, customers and suppliers.
type ImportConfig struct {
	Title       string   // e.g. "Import Customers (CSV)"
	Columns     string   // the CSV header line, shown verbatim
	Help        []string // bullet points of import rules (may contain HTML)
	PostURL     string   // multipart upload endpoint
	TemplateURL string   // download-template endpoint
}

// productCatID is the selected category ID for the product form picker, or ""
// when creating a new product.
func productCatID(p *products.Product) string {
	if p == nil {
		return ""
	}
	return strconv.FormatInt(p.CategoryID, 10)
}

// prefSupplierID is the product's preferred-supplier id (0 when none/new), for
// seeding the SupplierPicker on the product form.
func prefSupplierID(p *products.Product) int64 {
	if p == nil || p.PreferredSupplierID == nil {
		return 0
	}
	return *p.PreferredSupplierID
}

// prefSupplierName resolves the product's preferred-supplier display name from
// the supplied supplier list, for seeding the SupplierPicker's visible box.
func prefSupplierName(p *products.Product, sups []suppliers.Supplier) string {
	if p == nil || p.PreferredSupplierID == nil {
		return ""
	}
	for _, sp := range sups {
		if sp.ID == *p.PreferredSupplierID {
			return sp.Name
		}
	}
	return ""
}

// unitOptions adapts the unit list to OptionPicker choices ("Name (abbr)").
func unitOptions(us []units.Unit) []PickerOption {
	out := make([]PickerOption, 0, len(us))
	for _, u := range us {
		out = append(out, PickerOption{ID: u.ID, Label: u.Name + " (" + u.Abbreviation + ")"})
	}
	return out
}

// unitSelectedID is the product's unit when editing, else "pcs" (the most common
// unit) when it exists, else the first available unit — so a new product always
// defaults to a sensible, valid unit rather than whatever happens to sort first.
func unitSelectedID(p *products.Product, us []units.Unit) int64 {
	if p != nil {
		return p.UnitID
	}
	for _, u := range us {
		if strings.EqualFold(u.Abbreviation, "pcs") {
			return u.ID
		}
	}
	if len(us) > 0 {
		return us[0].ID
	}
	return 0
}

// jsIntArray renders a slice of IDs as a JS array literal, e.g. [3,7], for use
// in inline Alpine expressions (the collapsible category tree's visibility path).
func jsIntArray(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// Form field selectors for valOf, which prefills the product form for edits.
const (
	fName = iota
	fNameLocal
	fBarcode
	fCost
	fSelling
	fWholesale
	fTax
	fReorder
	fWarrantyMonths
)

// valOf returns the prefill value for a product form field, or "" when creating
// (p == nil). Money/number values use a plain fixed-decimal form (no separators)
// so they are valid <input> values.
func valOf(p *products.Product, field int) string {
	if p == nil {
		return ""
	}
	switch field {
	case fName:
		return p.Name
	case fNameLocal:
		if p.NameLocal != nil {
			return *p.NameLocal
		}
		return ""
	case fBarcode:
		if p.Barcode != nil {
			return *p.Barcode
		}
		return ""
	case fCost:
		return p.CostPrice.StringFixed(2)
	case fSelling:
		return p.SellingPrice.StringFixed(2)
	case fWholesale:
		return p.WholesalePrice.StringFixed(2)
	case fTax:
		return p.TaxRate.StringFixed(2)
	case fReorder:
		return strconv.Itoa(p.ReorderLevel)
	case fWarrantyMonths:
		return strconv.Itoa(p.WarrantyMonths)
	default:
		return ""
	}
}
