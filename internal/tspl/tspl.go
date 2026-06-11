// Package tspl renders a barcode price label as a TSPL/TSPL2 byte stream for a
// thermal label printer (e.g. Xprinter XP-365B). Like internal/escpos it uses
// the printer's built-in commands — the BARCODE command draws a scannable
// barcode natively, so no image/barcode library is needed and the binary stays
// dependency-free. The stream is sent raw via internal/printing.Raw.
package tspl

import (
	"fmt"
	"strconv"
	"strings"
)

// dotsPerMM is the resolution of the XP-365B class printers (203 dpi).
const dotsPerMM = 8

// narrow is the barcode's narrow-module width in dots (2 = 0.25mm, scannable).
const narrow = 2

// Input describes a single label run (one design, printed Count times).
type Input struct {
	Name      string // product / item name (top line)
	Code      string // barcode value
	Format    string // CODE128 | EAN13 | EAN8 | UPC | CODE39 (defaults to CODE128)
	PriceText string // pre-formatted price (e.g. "Rs. 250.00")
	ShowPrice bool
	Count     int // number of copies (>=1)
	WidthMM   int // label width  (mm)
	HeightMM  int // label height (mm)
	GapMM     int // gap between labels (mm)
}

// barcodeType maps the UI format name to the TSPL BARCODE type token.
func barcodeType(format string) string {
	switch strings.ToUpper(strings.TrimSpace(format)) {
	case "EAN13":
		return "EAN13"
	case "EAN8":
		return "EAN8"
	case "UPC", "UPCA", "UPC-A":
		return "UPCA"
	case "CODE39", "39":
		return "39"
	default:
		return "128" // CODE128
	}
}

// Document builds the TSPL byte stream for the label run.
func Document(in Input) []byte {
	w := in.WidthMM
	if w <= 0 {
		w = 50
	}
	h := in.HeightMM
	if h <= 0 {
		h = 25
	}
	gap := in.GapMM
	if gap < 0 {
		gap = 0
	}
	count := in.Count
	if count < 1 {
		count = 1
	}

	widthDots := w * dotsPerMM
	heightDots := h * dotsPerMM

	// Layout margins (dots). Content is laid out top-down with DIRECTION 0 so the
	// label's leading edge is the top — origin (0,0) is the top-left corner.
	const (
		marginX      = 16
		marginTop    = 12
		marginBottom = 12
		nameLineH    = 26 // height reserved for the name line (font "2")
		readableH    = 26 // height of the barcode's human-readable digits
		priceLineH   = 28 // height reserved for the price line (font "3")
	)

	var b strings.Builder
	fmt.Fprintf(&b, "SIZE %d mm, %d mm\r\n", w, h)
	fmt.Fprintf(&b, "GAP %d mm, 0 mm\r\n", gap)
	b.WriteString("DIRECTION 0\r\n")
	b.WriteString("REFERENCE 0,0\r\n")
	b.WriteString("CLS\r\n")

	// Name line (top), centered, truncated to what fits at font "2" (12 dots/char).
	const nameCharW = 12
	nameH := 0
	if name := ascii(in.Name); name != "" {
		if maxChars := (widthDots - 2*marginX) / nameCharW; maxChars > 0 && len(name) > maxChars {
			name = name[:maxChars]
		}
		x := centerX(widthDots, len(name)*nameCharW, marginX)
		fmt.Fprintf(&b, "TEXT %d,%d,\"2\",0,1,1,\"%s\"\r\n", x, marginTop, tsplEscape(name))
		nameH = nameLineH
	}

	// Price line (optional, bottom). Reserve its height up front so the barcode
	// never overlaps it.
	priceH := 0
	price := ""
	if in.ShowPrice {
		if price = ascii(in.PriceText); price != "" {
			priceH = priceLineH
		}
	}

	// Barcode (centered) fills the space between the name and the reserved bottom
	// area, leaving room for its own human-readable digits underneath.
	code := tsplEscape(ascii(in.Code))
	barY := marginTop + nameH
	barH := heightDots - barY - readableH - priceH - marginBottom
	if barH < 40 {
		barH = 40
	}
	barX := centerX(widthDots, barcodeWidthDots(in.Format, ascii(in.Code), narrow), marginX)
	fmt.Fprintf(&b, "BARCODE %d,%d,\"%s\",%d,1,0,%d,%d,\"%s\"\r\n",
		barX, barY, barcodeType(in.Format), barH, narrow, narrow*2, code)

	if priceH > 0 {
		const priceCharW = 16 // font "3"
		priceY := barY + barH + readableH
		x := centerX(widthDots, len(price)*priceCharW, marginX)
		fmt.Fprintf(&b, "TEXT %d,%d,\"3\",0,1,1,\"%s\"\r\n", x, priceY, tsplEscape(price))
	}

	fmt.Fprintf(&b, "PRINT %s,1\r\n", strconv.Itoa(count))
	return []byte(b.String())
}

// centerX returns the x offset that horizontally centers content of the given
// width on a label widthDots wide, never less than the left margin.
func centerX(widthDots, contentWidth, margin int) int {
	x := (widthDots - contentWidth) / 2
	if x < margin {
		x = margin
	}
	return x
}

// barcodeWidthDots estimates the printed width of a barcode so it can be
// centered. Widths come from each symbology's encoding; CODE128 accounts for the
// digit-pair (Code C) compression the printer applies to all-numeric data.
func barcodeWidthDots(format, code string, narrowDots int) int {
	switch barcodeType(format) {
	case "EAN13", "UPCA":
		return 95 * narrowDots // 95 modules
	case "EAN8":
		return 67 * narrowDots
	case "39":
		// Code39: ~13 narrow-equivalents per char incl. wide bars and the gap,
		// plus the start/stop (*) characters.
		return (len(code) + 2) * 13 * narrowDots
	default: // CODE128
		symbols := len(code)
		if isAllDigits(code) {
			symbols = (len(code) + 1) / 2 // Code C packs two digits per symbol
		}
		modules := 11*(symbols+2) + 13 // start + data + checksum (11 each) + stop (13)
		return modules * narrowDots
	}
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// tsplEscape escapes the characters that are special inside a TSPL quoted
// string: a backslash and a double quote are written as \\ and \".
func tsplEscape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return s
}

// ascii keeps printable ASCII and replaces anything else with '?', so the
// printer's built-in font never renders garbage for non-Latin text. (Use the
// HTML label sheet if you need non-Latin glyphs.)
func ascii(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 0x20 && r < 0x7F:
			b.WriteRune(r)
		case r == '\n' || r == '\t':
			b.WriteByte(' ')
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}
