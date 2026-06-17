package web

import (
	"fmt"
	"strings"

	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/suppliers"
	"karots-pos/internal/response"
	adminfragments "karots-pos/templates/fragments/admin"

	"github.com/labstack/echo/v4"
)

// supplierImportColumns is the CSV header for the supplier import template/export.
var supplierImportColumns = []string{
	"name", "contact_person", "phone", "address", "credit_days", "opening_balance",
}

// supplierImportSynonyms maps alternative header labels to a canonical column.
var supplierImportSynonyms = map[string]string{
	"supplier": "name", "vendor": "name", "supplier name": "name",
	"contact": "contact_person", "contact person": "contact_person", "person": "contact_person",
	"mobile": "phone", "tel": "phone", "telephone": "phone",
	"addr": "address",
	"credit": "credit_days", "credit days": "credit_days", "terms": "credit_days", "days": "credit_days",
	"balance": "opening_balance", "opening": "opening_balance",
	"opening balance": "opening_balance", "payable": "opening_balance",
	"outstanding": "opening_balance", "owed": "opening_balance",
}

func supplierImportConfig() adminfragments.ImportConfig {
	return adminfragments.ImportConfig{
		Title:       "Import Suppliers (CSV)",
		Columns:     strings.Join(supplierImportColumns, ", "),
		PostURL:     "/admin/suppliers/import",
		TemplateURL: "/admin/suppliers/import/template",
		Help: []string{
			"<b>name</b> matches an existing supplier (update); otherwise a new one is created.",
			"<b>opening_balance</b> is what we already owed this supplier at onboarding — applied to new suppliers only.",
			"<b>credit_days</b> and <b>opening_balance</b> are plain numbers (no currency symbol).",
		},
	}
}

// SupplierImportModal returns the upload dialog.
func (a *adminUI) SupplierImportModal(c echo.Context) error {
	return response.RenderFragment(c, adminfragments.ImportModal(supplierImportConfig()))
}

// SupplierImportTemplate streams an empty CSV with just the header row.
func (a *adminUI) SupplierImportTemplate(c echo.Context) error {
	return writeCSV(c, "suppliers-template", supplierImportColumns, nil)
}

// SupplierExportCSV streams active suppliers in the import column layout for round-trip edits.
func (a *adminUI) SupplierExportCSV(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.suppliers.List(ctx, "") // List returns all active suppliers (no limit)
	if err != nil {
		return err
	}
	out := make([][]string, 0, len(rows))
	for _, sp := range rows {
		out = append(out, []string{
			sp.Name, strDeref(sp.ContactPerson), strDeref(sp.Phone), strDeref(sp.Address),
			fmt.Sprintf("%d", sp.CreditDays), csvMoney(sp.OutstandingBalance),
		})
	}
	return writeCSV(c, "suppliers-export", supplierImportColumns, out)
}

// SupplierImport parses an uploaded CSV and upserts each row (best-effort).
func (a *adminUI) SupplierImport(c echo.Context) error {
	ctx := c.Request().Context()
	col, recs, err := readImportCSV(c, supplierImportSynonyms)
	if err != nil {
		return err
	}

	var sum adminfragments.ImportSummary
	line := 1
	for _, rec := range recs {
		line++
		if line-1 > maxImportRows {
			sum.Notes = append(sum.Notes, fmt.Sprintf("stopped at %d rows (limit)", maxImportRows))
			break
		}
		get := cellGetter(col, rec)
		if get("name") == "" {
			if strings.TrimSpace(strings.Join(rec, "")) != "" {
				sum.Errors = append(sum.Errors, adminfragments.ImportRowError{Line: line, Message: "missing name"})
				sum.Skipped++
			}
			continue
		}
		in := suppliers.CreateInput{
			Name:          get("name"),
			ContactPerson: nilIfEmptyPtr(get("contact_person")),
			Phone:         nilIfEmptyPtr(get("phone")),
			Address:       nilIfEmptyPtr(get("address")),
			CreditDays:    intCell(get("credit_days")),
		}
		res, ierr := a.s.suppliers.ImportOne(ctx, in, moneyCell(get("opening_balance")))
		if ierr != nil {
			sum.Errors = append(sum.Errors, adminfragments.ImportRowError{Line: line, Message: importErrMsg(ierr)})
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

	a.s.logAudit(c, audit.ActionCreate, "supplier", "",
		fmt.Sprintf("CSV import: %d created, %d updated, %d skipped", sum.Created, sum.Updated, sum.Skipped))
	c.Response().Header().Set("HX-Trigger", "reload-suppliers")
	return response.RenderFragment(c, adminfragments.ImportResultView(sum))
}

// nilIfEmptyPtr returns nil for a blank string, else a pointer to the trimmed value.
func nilIfEmptyPtr(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}
