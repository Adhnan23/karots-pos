package web

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/customers"
	"karots-pos/internal/response"
	adminfragments "karots-pos/templates/fragments/admin"

	"github.com/labstack/echo/v4"
)

// customerImportColumns is the CSV header for the customer import template/export.
var customerImportColumns = []string{
	"name", "phone", "address", "credit_limit", "opening_balance",
}

// customerImportSynonyms maps alternative header labels to a canonical column.
var customerImportSynonyms = map[string]string{
	"customer": "name", "customer name": "name", "full name": "name",
	"mobile": "phone", "contact": "phone", "tel": "phone", "telephone": "phone",
	"addr": "address",
	"credit": "credit_limit", "credit limit": "credit_limit", "limit": "credit_limit",
	"balance": "opening_balance", "opening": "opening_balance",
	"opening balance": "opening_balance", "due": "opening_balance",
	"outstanding": "opening_balance", "owed": "opening_balance",
}

func customerImportConfig() adminfragments.ImportConfig {
	return adminfragments.ImportConfig{
		Title:       "Import Customers (CSV)",
		Columns:     strings.Join(customerImportColumns, ", "),
		PostURL:     "/admin/customers/import",
		TemplateURL: "/admin/customers/import/template",
		Help: []string{
			"<b>phone</b> matches an existing customer (update); blank or new phone creates one.",
			"<b>opening_balance</b> is what the customer already owed at onboarding — applied to new customers only.",
			"<b>credit_limit</b> and <b>opening_balance</b> are plain numbers (no currency symbol).",
		},
	}
}

// CustomerImportModal returns the upload dialog.
func (a *adminUI) CustomerImportModal(c echo.Context) error {
	return response.RenderFragment(c, adminfragments.ImportModal(customerImportConfig()))
}

// CustomerImportTemplate streams an empty CSV with just the header row.
func (a *adminUI) CustomerImportTemplate(c echo.Context) error {
	return writeCSV(c, "customers-template", customerImportColumns, nil)
}

// CustomerExportCSV streams active customers in the import column layout for round-trip edits.
func (a *adminUI) CustomerExportCSV(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := a.s.customers.AllActive(ctx)
	if err != nil {
		return err
	}
	out := make([][]string, 0, len(rows))
	for _, cu := range rows {
		out = append(out, []string{
			cu.Name, strDeref(cu.Phone), strDeref(cu.Address),
			csvMoney(cu.CreditLimit), csvMoney(cu.OutstandingBalance),
		})
	}
	return writeCSV(c, "customers-export", customerImportColumns, out)
}

// CustomerImport parses an uploaded CSV and upserts each row (best-effort).
func (a *adminUI) CustomerImport(c echo.Context) error {
	ctx := c.Request().Context()
	col, recs, err := readImportCSV(c, customerImportSynonyms)
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
		row := customers.ImportRow{
			Name:           get("name"),
			Phone:          get("phone"),
			Address:        get("address"),
			CreditLimit:    moneyCell(get("credit_limit")),
			OpeningBalance: moneyCell(get("opening_balance")),
		}
		res, ierr := a.s.customers.ImportOne(ctx, row)
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

	a.s.logAudit(c, audit.ActionCreate, "customer", "",
		fmt.Sprintf("CSV import: %d created, %d updated, %d skipped", sum.Created, sum.Updated, sum.Skipped))
	c.Response().Header().Set("HX-Trigger", "reload-customers")
	return response.RenderFragment(c, adminfragments.ImportResultView(sum))
}

// --- shared CSV import helpers (used by customer & supplier imports) ---

// readImportCSV opens the uploaded "file", reads the header (mapping synonyms),
// and returns the column index map plus all data records.
func readImportCSV(c echo.Context, synonyms map[string]string) (map[string]int, [][]string, error) {
	fh, err := c.FormFile("file")
	if err != nil {
		return nil, nil, apperr.BadRequest("please choose a CSV file")
	}
	f, err := fh.Open()
	if err != nil {
		return nil, nil, apperr.BadRequest("could not read the uploaded file")
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true

	header, err := r.Read()
	if err != nil {
		return nil, nil, apperr.BadRequest("the file is empty or not valid CSV")
	}
	col := map[string]int{}
	for i, h := range header {
		key := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))
		if canon, ok := synonyms[key]; ok {
			key = canon
		}
		if _, dup := col[key]; !dup {
			col[key] = i
		}
	}
	if _, ok := col["name"]; !ok {
		return nil, nil, apperr.BadRequest("CSV must have a 'name' column")
	}
	var recs [][]string
	for {
		rec, rerr := r.Read()
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return nil, nil, apperr.BadRequest("could not parse CSV: " + rerr.Error())
		}
		recs = append(recs, rec)
	}
	return col, recs, nil
}

// cellGetter returns a trimmed-cell accessor for a record by canonical column name.
func cellGetter(col map[string]int, rec []string) func(string) string {
	return func(key string) string {
		if i, ok := col[key]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
}

// importErrMsg unwraps an apperr to its user-facing message, else a generic one.
func importErrMsg(err error) string {
	if ae, ok := apperr.As(err); ok {
		return ae.Message
	}
	return "import failed"
}

func strDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
