package adminfragments

import (
	"strconv"

	"karots-pos/internal/features/products"
)

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
