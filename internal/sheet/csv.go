package sheet

import (
	"bytes"
	"encoding/csv"
	"io"
	"strings"
)

func readCSV(b []byte) ([][]string, error) {
	r := csv.NewReader(bytes.NewReader(b))
	r.FieldsPerRecord = -1 // ragged rows are fine; callers index by header
	r.TrimLeadingSpace = true
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	// Strip a UTF-8 BOM that Excel/Windows often prepends to the first cell.
	if len(rows) > 0 && len(rows[0]) > 0 {
		rows[0][0] = strings.TrimPrefix(rows[0][0], "\ufeff")
	}
	return rows, nil
}

func writeCSV(w io.Writer, header []string, rows [][]string) error {
	cw := csv.NewWriter(w)
	if len(header) > 0 {
		if err := cw.Write(header); err != nil {
			return err
		}
	}
	for _, r := range rows {
		if err := cw.Write(r); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
