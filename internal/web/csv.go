package web

import (
	"encoding/csv"
	"net/http"

	"karots-pos/internal/sheet"

	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

// wantsCSV reports whether the caller asked for a CSV download (?format=csv).
func wantsCSV(c echo.Context) bool { return c.QueryParam("format") == "csv" }

// writeSheet streams rows as a downloadable spreadsheet in the format named by
// the ?format= query (csv | xlsx | ods, default csv). filename is the base name
// without extension. Used by the import/export/template downloads so the user
// gets the format they keep their data in.
func writeSheet(c echo.Context, filename string, header []string, rows [][]string) error {
	f := sheet.FormatFrom(c.QueryParam("format"))
	c.Response().Header().Set(echo.HeaderContentType, f.ContentType())
	c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="`+filename+f.Ext()+`"`)
	c.Response().WriteHeader(http.StatusOK)
	return sheet.Write(c.Response(), f, header, rows)
}

// writeCSV streams rows as a downloadable CSV attachment. filename is the base
// name (without extension). A leading header row is written when non-empty.
func writeCSV(c echo.Context, filename string, header []string, rows [][]string) error {
	c.Response().Header().Set(echo.HeaderContentType, "text/csv; charset=utf-8")
	c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="`+filename+`.csv"`)
	c.Response().WriteHeader(http.StatusOK)
	w := csv.NewWriter(c.Response())
	if len(header) > 0 {
		_ = w.Write(header)
	}
	for _, r := range rows {
		_ = w.Write(r)
	}
	w.Flush()
	return w.Error()
}

// csvMoney formats a decimal as a plain 2dp string for CSV cells (no symbol or
// thousands separators, so spreadsheets parse it as a number).
func csvMoney(d decimal.Decimal) string { return d.StringFixed(2) }
