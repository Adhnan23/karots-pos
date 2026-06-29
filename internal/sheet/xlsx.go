package sheet

import (
	"bytes"
	"errors"
	"io"

	"github.com/xuri/excelize/v2"
)

func readXLSX(b []byte) ([][]string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, errors.New("the workbook has no sheets")
	}
	return f.GetRows(sheets[0]) // first sheet; trailing empty cells trimmed per row
}

func writeXLSX(w io.Writer, header []string, rows [][]string) error {
	f := excelize.NewFile()
	defer f.Close()
	sheet := f.GetSheetName(0) // "Sheet1"

	rowIdx := 1
	writeRow := func(vals []string) error {
		for ci, v := range vals {
			cell, err := excelize.CoordinatesToCellName(ci+1, rowIdx)
			if err != nil {
				return err
			}
			// SetCellStr keeps every cell as text so barcodes/codes don't get
			// coerced to numbers (which would drop leading zeros).
			if err := f.SetCellStr(sheet, cell, v); err != nil {
				return err
			}
		}
		rowIdx++
		return nil
	}

	if len(header) > 0 {
		if err := writeRow(header); err != nil {
			return err
		}
	}
	for _, r := range rows {
		if err := writeRow(r); err != nil {
			return err
		}
	}
	return f.Write(w)
}
