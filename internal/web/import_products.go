package web

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/products"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"
	adminfragments "karots-pos/templates/fragments/admin"

	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

// importColumns lists the CSV header for the product import template and export,
// in order. The parser also accepts a few synonyms (see importSynonyms).
var importColumns = []string{
	"name", "name_si", "barcode", "category", "unit",
	"cost_price", "selling_price", "wholesale_price", "tax_rate",
	"reorder_level", "opening_qty", "supplier", "track_serial", "warranty_months",
}

// importSynonyms maps alternative header labels to a canonical column name.
var importSynonyms = map[string]string{
	"product": "name", "item": "name",
	"sinhala": "name_si", "name (si)": "name_si",
	"code": "barcode", "sku": "barcode",
	"category path": "category", "categories": "category",
	"uom": "unit",
	"cost": "cost_price", "buying price": "cost_price", "cost price": "cost_price",
	"price": "selling_price", "selling": "selling_price", "selling price": "selling_price", "retail": "selling_price",
	"wholesale": "wholesale_price", "wholesale price": "wholesale_price",
	"tax": "tax_rate", "vat": "tax_rate", "tax %": "tax_rate", "tax%": "tax_rate",
	"reorder": "reorder_level", "reorder level": "reorder_level", "min": "reorder_level",
	"qty": "opening_qty", "quantity": "opening_qty", "stock": "opening_qty",
	"opening": "opening_qty", "opening stock": "opening_qty",
	"vendor": "supplier", "preferred supplier": "supplier",
	"serial": "track_serial", "track serial": "track_serial",
	"warranty": "warranty_months", "warranty (months)": "warranty_months",
}

const maxImportRows = 5000

// ProductImportModal returns the upload dialog (instructions + file form).
func (a *adminUI) ProductImportModal(c echo.Context) error {
	return response.RenderFragment(c, adminfragments.ImportProductsModal())
}

// ProductImportTemplate streams an empty CSV with just the header row.
func (a *adminUI) ProductImportTemplate(c echo.Context) error {
	return writeCSV(c, "products-template", importColumns, nil)
}

// ProductExportCSV streams the whole active catalog in the import column layout,
// so an owner can export, edit in a spreadsheet, and re-upload (round-trip).
func (a *adminUI) ProductExportCSV(c echo.Context) error {
	ctx := c.Request().Context()
	q := products.ListQuery{Limit: 100000, Page: 1}
	rows, _, err := a.s.products.List(ctx, q)
	if err != nil {
		return err
	}
	out := make([][]string, 0, len(rows))
	for _, p := range rows {
		supplier := ""
		if p.PreferredSupplierName != nil {
			supplier = *p.PreferredSupplierName
		}
		barcode := ""
		if p.Barcode != nil {
			barcode = *p.Barcode
		}
		nameSi := ""
		if p.NameSi != nil {
			nameSi = *p.NameSi
		}
		out = append(out, []string{
			p.Name, nameSi, barcode, p.CategoryName, p.UnitAbbr,
			csvMoney(p.CostPrice), csvMoney(p.SellingPrice), csvMoney(p.WholesalePrice), csvMoney(p.TaxRate),
			strconv.Itoa(p.ReorderLevel), p.StockQty.String(), supplier,
			strconv.FormatBool(p.TrackSerial), strconv.Itoa(p.WarrantyMonths),
		})
	}
	return writeCSV(c, "products-export", importColumns, out)
}

// ProductImport parses an uploaded CSV and upserts each row, returning a summary
// fragment. It is best-effort: invalid rows are reported by line number while the
// rest still import.
func (a *adminUI) ProductImport(c echo.Context) error {
	ctx := c.Request().Context()
	fh, err := c.FormFile("file")
	if err != nil {
		return apperr.BadRequest("please choose a CSV file")
	}
	f, err := fh.Open()
	if err != nil {
		return apperr.BadRequest("could not read the uploaded file")
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true

	header, err := r.Read()
	if err != nil {
		return apperr.BadRequest("the file is empty or not valid CSV")
	}
	col := mapImportHeader(header)
	if _, ok := col["name"]; !ok {
		return apperr.BadRequest("CSV must have a 'name' column")
	}

	// Resolve units once: match by lower-case name or abbreviation; fall back to
	// the first unit when a row leaves it blank.
	unitsList, err := a.s.units.List(ctx)
	if err != nil {
		return err
	}
	if len(unitsList) == 0 {
		return apperr.BadRequest("add at least one unit before importing products")
	}
	unitByKey := map[string]int64{}
	for _, u := range unitsList {
		unitByKey[strings.ToLower(u.Name)] = u.ID
		unitByKey[strings.ToLower(u.Abbreviation)] = u.ID
	}
	defaultUnitID := unitsList[0].ID

	var sum adminfragments.ImportSummary
	line := 1 // header was line 1
	for {
		rec, rerr := r.Read()
		if errors.Is(rerr, io.EOF) {
			break
		}
		line++
		if rerr != nil {
			sum.Errors = append(sum.Errors, adminfragments.ImportRowError{Line: line, Message: "unreadable row: " + rerr.Error()})
			sum.Skipped++
			continue
		}
		if line-1 > maxImportRows {
			sum.Notes = append(sum.Notes, fmt.Sprintf("stopped at %d rows (limit)", maxImportRows))
			break
		}
		get := func(key string) string {
			if i, ok := col[key]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		if get("name") == "" {
			// Silently skip fully-blank trailing rows; flag others.
			if strings.TrimSpace(strings.Join(rec, "")) != "" {
				sum.Errors = append(sum.Errors, adminfragments.ImportRowError{Line: line, Message: "missing name"})
				sum.Skipped++
			}
			continue
		}

		// Resolve unit.
		unitID := defaultUnitID
		if u := get("unit"); u != "" {
			id, ok := unitByKey[strings.ToLower(u)]
			if !ok {
				sum.Errors = append(sum.Errors, adminfragments.ImportRowError{Line: line, Message: "unknown unit '" + u + "'"})
				sum.Skipped++
				continue
			}
			unitID = id
		}

		// Resolve category path (blank → Uncategorized).
		catPath := get("category")
		if catPath == "" {
			catPath = "Uncategorized"
		}
		catID, cerr := a.s.categories.FindOrCreateByPath(ctx, catPath)
		if cerr != nil || catID == 0 {
			sum.Errors = append(sum.Errors, adminfragments.ImportRowError{Line: line, Message: "could not resolve category"})
			sum.Skipped++
			continue
		}

		// Resolve preferred supplier (blank → none).
		supID, serr := a.s.suppliers.FindOrCreateByName(ctx, get("supplier"))
		if serr != nil {
			sum.Errors = append(sum.Errors, adminfragments.ImportRowError{Line: line, Message: "could not resolve supplier"})
			sum.Skipped++
			continue
		}

		row := products.ImportRow{
			Name:              get("name"),
			NameSi:            get("name_si"),
			Barcode:           get("barcode"),
			CategoryID:        catID,
			UnitID:            unitID,
			UserID:            middleware.CurrentUserID(c),
			PreferredSupplier: supID,
			Cost:              moneyCell(get("cost_price")),
			Selling:           moneyCell(get("selling_price")),
			Wholesale:         moneyCell(get("wholesale_price")),
			Tax:               moneyCell(get("tax_rate")),
			Reorder:           intCell(get("reorder_level")),
			WarrantyMonths:    intCell(get("warranty_months")),
			TrackSerial:       boolCell(get("track_serial")),
			OpeningQty:        moneyCell(get("opening_qty")),
		}
		res, ierr := a.s.products.ImportOne(ctx, row)
		if ierr != nil {
			msg := "import failed"
			if ae, ok := apperr.As(ierr); ok {
				msg = ae.Message
			}
			sum.Errors = append(sum.Errors, adminfragments.ImportRowError{Line: line, Message: msg})
			sum.Skipped++
			continue
		}
		switch res.Action {
		case "created":
			sum.Created++
		case "updated":
			sum.Updated++
		}
		if res.Note != "" {
			sum.Notes = append(sum.Notes, fmt.Sprintf("Line %d: %s", line, res.Note))
		}
	}

	a.s.logAudit(c, audit.ActionCreate, "product", "",
		fmt.Sprintf("CSV import: %d created, %d updated, %d skipped", sum.Created, sum.Updated, sum.Skipped))

	// Refresh the product list behind the modal.
	c.Response().Header().Set("HX-Trigger", "reload-products")
	return response.RenderFragment(c, adminfragments.ImportProductsResult(sum))
}

// mapImportHeader builds canonical-column → index from a header row, applying
// synonyms and stripping a leading UTF-8 BOM from the first cell.
func mapImportHeader(header []string) map[string]int {
	col := map[string]int{}
	for i, h := range header {
		key := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))
		if canon, ok := importSynonyms[key]; ok {
			key = canon
		}
		if _, dup := col[key]; !dup {
			col[key] = i
		}
	}
	return col
}

func moneyCell(s string) decimal.Decimal {
	if strings.TrimSpace(s) == "" {
		return decimal.Zero
	}
	d, err := money.Parse(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}

func intCell(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func boolCell(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "y":
		return true
	}
	return false
}
