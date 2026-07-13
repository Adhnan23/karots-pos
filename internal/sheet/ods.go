package sheet

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
)

// odsMime is the OpenDocument spreadsheet media type; it must be the first
// entry in the zip and stored uncompressed for readers to recognise the file.
const odsMime = "application/vnd.oasis.opendocument.spreadsheet"

// repeatCap bounds how far a number-columns/rows-repeated attribute is expanded
// for non-empty content, so a malformed file can't blow up memory. (Empty
// repeats — the huge trailing padding LibreOffice writes — are never expanded.)
const repeatCap = 4096

// readODS parses the first table of an ODS spreadsheet into rows of strings.
func readODS(b []byte) ([][]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, err
	}
	var content *zip.File
	for _, f := range zr.File {
		if f.Name == "content.xml" {
			content = f
			break
		}
	}
	if content == nil {
		return nil, errors.New("not a valid ODS file (no content.xml)")
	}
	rc, err := content.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	dec := xml.NewDecoder(rc)
	var rows [][]string
	var cur []string
	var cellText bytes.Buffer

	inCell := false
	cellRepeat := 1
	rowRepeat := 1
	pendingCols := 0 // empty cells seen but not yet committed (dropped if trailing)
	pendingRows := 0 // empty rows seen but not yet committed (dropped if trailing)
	sawTable := false

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "table":
				sawTable = true
			case "table-row":
				cur = cur[:0]
				pendingCols = 0
				rowRepeat = attrInt(t, "number-rows-repeated", 1)
			case "table-cell", "covered-table-cell":
				inCell = true
				cellText.Reset()
				cellRepeat = attrInt(t, "number-columns-repeated", 1)
			}
		case xml.CharData:
			if inCell {
				cellText.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "table-cell", "covered-table-cell":
				inCell = false
				txt := cellText.String()
				if txt == "" {
					pendingCols += cellRepeat
				} else {
					for i := 0; i < pendingCols; i++ {
						cur = append(cur, "")
					}
					pendingCols = 0
					n := cellRepeat
					if n > repeatCap {
						n = repeatCap
					}
					for i := 0; i < n; i++ {
						cur = append(cur, txt)
					}
				}
			case "table-row":
				if len(cur) == 0 {
					pendingRows += rowRepeat
				} else {
					for i := 0; i < pendingRows; i++ {
						rows = append(rows, nil)
					}
					pendingRows = 0
					n := rowRepeat
					if n > repeatCap {
						n = repeatCap
					}
					for i := 0; i < n; i++ {
						rows = append(rows, append([]string(nil), cur...))
					}
				}
			case "table":
				// Only the first table is read.
				if sawTable {
					return rows, nil
				}
			}
		}
	}
	return rows, nil
}

// attrInt reads an integer attribute by local name, returning def if absent.
func attrInt(e xml.StartElement, local string, def int) int {
	for _, a := range e.Attr {
		if a.Name.Local == local {
			if n, err := strconv.Atoi(a.Value); err == nil && n > 0 {
				return n
			}
		}
	}
	return def
}

func writeODS(w io.Writer, header []string, rows [][]string) error {
	zw := zip.NewWriter(w)

	// "mimetype" must come first and be stored (not deflated).
	mw, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		return err
	}
	if _, err := io.WriteString(mw, odsMime); err != nil {
		return err
	}

	cw, err := zw.Create("content.xml")
	if err != nil {
		return err
	}
	if err := writeODSContent(cw, header, rows); err != nil {
		return err
	}

	mfw, err := zw.Create("META-INF/manifest.xml")
	if err != nil {
		return err
	}
	if _, err := io.WriteString(mfw, odsManifest); err != nil {
		return err
	}
	return zw.Close()
}

const odsManifest = `<?xml version="1.0" encoding="UTF-8"?>
<manifest:manifest xmlns:manifest="urn:oasis:names:tc:opendocument:xmlns:manifest:1.0" manifest:version="1.2">
 <manifest:file-entry manifest:full-path="/" manifest:version="1.2" manifest:media-type="application/vnd.oasis.opendocument.spreadsheet"/>
 <manifest:file-entry manifest:full-path="content.xml" manifest:media-type="text/xml"/>
</manifest:manifest>`

func writeODSContent(w io.Writer, header []string, rows [][]string) error {
	// ODF's schema requires a table to declare its columns before any rows;
	// LibreOffice rejects a column-less table as corrupt. The column must also
	// carry a width style — a styleless column renders at zero width in some
	// readers (OnlyOffice), so the data is present but invisible. So we emit an
	// automatic column style with an explicit width and reference it, matching
	// what LibreOffice itself writes.
	ncols := len(header)
	for _, r := range rows {
		if len(r) > ncols {
			ncols = len(r)
		}
	}

	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<office:document-content ` +
		`xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" ` +
		`xmlns:style="urn:oasis:names:tc:opendocument:xmlns:style:1.0" ` +
		`xmlns:fo="urn:oasis:names:tc:opendocument:xmlns:xsl-fo-compatible:1.0" ` +
		`xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0" ` +
		`xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" office:version="1.2">`)
	b.WriteString(`<office:automatic-styles>` +
		`<style:style style:name="co1" style:family="table-column">` +
		`<style:table-column-properties fo:break-before="auto" style:column-width="2.5cm"/>` +
		`</style:style>` +
		`</office:automatic-styles>`)
	b.WriteString(`<office:body><office:spreadsheet>`)
	b.WriteString(`<table:table table:name="Sheet1">`)

	if ncols > 0 {
		b.WriteString(`<table:table-column table:style-name="co1"`)
		if ncols > 1 {
			b.WriteString(` table:number-columns-repeated="` + strconv.Itoa(ncols) + `"`)
		}
		b.WriteString(`/>`)
	}

	writeRow := func(cells []string) {
		b.WriteString(`<table:table-row>`)
		for _, c := range cells {
			b.WriteString(`<table:table-cell office:value-type="string"><text:p>`)
			_ = xml.EscapeText(&b, []byte(c))
			b.WriteString(`</text:p></table:table-cell>`)
		}
		b.WriteString(`</table:table-row>`)
	}

	if len(header) > 0 {
		writeRow(header)
	}
	for _, r := range rows {
		writeRow(r)
	}

	b.WriteString(`</table:table></office:spreadsheet></office:body></office:document-content>`)
	_, err := w.Write(b.Bytes())
	return err
}
