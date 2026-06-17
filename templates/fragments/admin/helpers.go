package adminfragments

import (
	"encoding/json"
	"strconv"
	"strings"

	"karots-pos/internal/features/categories"
	"karots-pos/internal/features/products"
)

// categoryPickerData builds the x-data init string for the categoryPicker Alpine
// component (static/js/app.js): the category options as JSON plus the picker
// config. Used by both the product form and the products filter.
func categoryPickerData(nodes []categories.TreeNode, fieldName, selected string, includeAll bool, allLabel string, reload bool) string {
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
		"name":       fieldName,
		"selected":   selected,
		"options":    opts,
		"includeAll": includeAll,
		"allLabel":   allLabel,
		"reload":     reload,
	}
	b, _ := json.Marshal(cfg)
	return "categoryPicker(" + string(b) + ")"
}

// ImportRowError is one failed CSV row, reported with its 1-based line number.
type ImportRowError struct {
	Line    int
	Message string
}

// ImportSummary is the outcome of a bulk product CSV import, shown after upload.
type ImportSummary struct {
	Created int
	Updated int
	Skipped int
	Notes   []string
	Errors  []ImportRowError
}

// productCatID is the selected category ID for the product form picker, or ""
// when creating a new product.
func productCatID(p *products.Product) string {
	if p == nil {
		return ""
	}
	return strconv.FormatInt(p.CategoryID, 10)
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
