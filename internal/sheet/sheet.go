// Package sheet reads and writes tabular data as CSV, XLSX (Excel) or ODS
// (OpenDocument / LibreOffice). It lets the import/export features accept and
// produce whichever format the user keeps their data in, without each call site
// caring about the encoding. All cells are treated as text so identifiers like
// barcodes keep their leading zeros.
package sheet

import (
	"errors"
	"io"
	"strings"
)

// Format is a supported spreadsheet encoding.
type Format string

const (
	CSV  Format = "csv"
	XLSX Format = "xlsx"
	ODS  Format = "ods"
)

// FormatFrom normalises a user-supplied value (e.g. a ?format= query) to a
// supported Format, defaulting to CSV for anything unrecognised.
func FormatFrom(s string) Format {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "xlsx", "excel", "xls":
		return XLSX
	case "ods", "odf", "ods-spreadsheet":
		return ODS
	default:
		return CSV
	}
}

// Ext is the file extension (with dot) for the format.
func (f Format) Ext() string {
	switch f {
	case XLSX:
		return ".xlsx"
	case ODS:
		return ".ods"
	default:
		return ".csv"
	}
}

// ContentType is the MIME type to serve a download of this format with.
func (f Format) ContentType() string {
	switch f {
	case XLSX:
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ODS:
		return "application/vnd.oasis.opendocument.spreadsheet"
	default:
		return "text/csv; charset=utf-8"
	}
}

// formatForName picks the format from a filename's extension.
func formatForName(name string) Format {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".xlsx"):
		return XLSX
	case strings.HasSuffix(lower, ".ods"):
		return ODS
	default:
		return CSV
	}
}

// Read parses an uploaded spreadsheet into rows of strings (the header row is
// the first element). The format is chosen from the filename's extension; an
// unknown extension is treated as CSV. The reader is fully consumed.
func Read(filename string, r io.Reader) ([][]string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	switch formatForName(filename) {
	case XLSX:
		return readXLSX(b)
	case ODS:
		return readODS(b)
	default:
		return readCSV(b)
	}
}

// Write streams header + rows to w in the given format. A blank header is
// skipped (used for templates that are header-only by passing rows=nil).
func Write(w io.Writer, f Format, header []string, rows [][]string) error {
	switch f {
	case XLSX:
		return writeXLSX(w, header, rows)
	case ODS:
		return writeODS(w, header, rows)
	case CSV:
		return writeCSV(w, header, rows)
	}
	return errors.New("unknown sheet format")
}
