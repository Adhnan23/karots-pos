// Package escpos renders a sale as an ESC/POS byte stream for a thermal receipt
// printer (e.g. Xprinter POS-80). It uses the printer's built-in font rather
// than raster graphics, so printing is fast and immune to the PDF/codepage
// garbage that browser printing to a raw CUPS queue produces. The width is
// driven by the shop's receipt_width setting (80mm = 48 cols, 58mm = 32 cols).
package escpos

import (
	"bytes"
	"strings"

	"karots-pos/internal/features/sales"
	"karots-pos/internal/features/settings"
	"karots-pos/internal/money"
)

// ESC/POS control bytes.
const (
	esc = 0x1B
	gs  = 0x1D
)

// feedBeforeCut is how many blank lines to feed before the cut. The cutter sits
// below the print head, so this must cover that gap plus a small bottom margin,
// otherwise the last line is cut off.
const feedBeforeCut = 8

// columns is the character width of a print line for the configured roll.
func columns(width string) int {
	if width == "58" {
		return 32
	}
	return 48
}

// Document builds the ESC/POS byte stream for a completed sale.
func Document(d sales.Detail, cfg settings.Settings) []byte {
	w := columns(cfg.ReceiptWidth)
	sym := cfg.CurrencySymbol
	if sym == "" {
		sym = "Rs."
	}

	var b bytes.Buffer
	b.Write([]byte{esc, '@'})    // initialize
	b.Write([]byte{esc, 't', 0}) // code page PC437 (Latin)

	// --- Header (centered) ---
	b.Write([]byte{esc, 'a', 1})    // center
	b.Write([]byte{esc, 'E', 1})    // bold on
	b.Write([]byte{gs, '!', 0x11})  // double width + height
	line(&b, ascii(cfg.ShopName))
	b.Write([]byte{gs, '!', 0x00})  // normal size
	b.Write([]byte{esc, 'E', 0})    // bold off
	if s := deref(cfg.Address); s != "" {
		line(&b, ascii(s))
	}
	if s := deref(cfg.Phone); s != "" {
		line(&b, "Tel: "+ascii(s))
	}
	if cfg.TaxRegistered {
		if s := deref(cfg.TaxRegNo); s != "" {
			line(&b, "VAT: "+ascii(s))
		}
	}

	// --- Meta (left) ---
	b.Write([]byte{esc, 'a', 0}) // left
	divider(&b, w)
	line(&b, leftRight("Receipt:", d.Sale.ReceiptNo, w))
	line(&b, leftRight("Date:", d.Sale.CreatedAt.Format("2006-01-02 15:04"), w))
	line(&b, leftRight("Cashier:", ascii(d.Sale.CashierName), w))
	if d.Sale.CustomerName != nil && *d.Sale.CustomerName != "" {
		line(&b, leftRight("Customer:", ascii(*d.Sale.CustomerName), w))
	}
	divider(&b, w)

	// --- Items ---
	for _, it := range d.Items {
		line(&b, ascii(it.ProductName))
		qty := money.Display(it.Quantity) + " " + ascii(it.UnitAbbr) + " x " + money.Display(it.UnitPrice)
		line(&b, leftRight("  "+qty, money.Display(it.Subtotal), w))
	}
	divider(&b, w)

	// --- Totals ---
	line(&b, leftRight("Subtotal", money.Format(sym, d.Sale.Subtotal), w))
	if d.Sale.Discount.IsPositive() {
		line(&b, leftRight("Discount", "-"+money.Format(sym, d.Sale.Discount), w))
	}
	if d.Sale.Tax.IsPositive() {
		line(&b, leftRight("Tax", money.Format(sym, d.Sale.Tax), w))
	}
	b.Write([]byte{esc, 'E', 1}) // bold total
	line(&b, leftRight("TOTAL", money.Format(sym, d.Sale.Total), w))
	b.Write([]byte{esc, 'E', 0})
	line(&b, leftRight("Paid", money.Format(sym, d.Sale.PaidAmount), w))
	if d.Sale.ChangeGiven.IsPositive() {
		line(&b, leftRight("Change", money.Format(sym, d.Sale.ChangeGiven), w))
	}
	for _, p := range d.Payments {
		line(&b, leftRight(capitalize(ascii(p.Method)), money.Format(sym, p.Amount), w))
	}
	if d.Sale.Status == "credit" {
		divider(&b, w)
		b.Write([]byte{esc, 'a', 1})
		line(&b, "*** CREDIT SALE - BALANCE DUE ***")
		b.Write([]byte{esc, 'a', 0})
	}
	divider(&b, w)

	// --- Footer (centered) ---
	b.Write([]byte{esc, 'a', 1})
	if s := deref(cfg.ReceiptFooter); s != "" {
		line(&b, ascii(s))
	}
	line(&b, "Thank you! Come again.")

	// Feed past the head-to-cutter gap (plus a little margin) before cutting, so
	// the last printed line clears the blade instead of being sheared at the end.
	b.Write([]byte{esc, 'd', feedBeforeCut})
	b.Write([]byte{gs, 'V', 1})

	return b.Bytes()
}

func line(b *bytes.Buffer, s string) {
	b.WriteString(s)
	b.WriteByte('\n')
}

func divider(b *bytes.Buffer, w int) { line(b, strings.Repeat("-", w)) }

// leftRight pads a left and right label out to w columns. The left side is
// truncated if the two together would overflow the line.
func leftRight(l, r string, w int) string {
	if len(l)+len(r) >= w {
		max := w - len(r) - 1
		if max < 0 {
			max = 0
		}
		if len(l) > max {
			l = l[:max]
		}
	}
	pad := w - len(l) - len(r)
	if pad < 1 {
		pad = 1
	}
	return l + strings.Repeat(" ", pad) + r
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ascii keeps printable ASCII and replaces anything else (including UTF-8
// multibyte runes) with '?', so the printer's PC437 code page never renders
// the CJK garbage seen when raw bytes are misinterpreted. Non-Latin shop text
// (e.g. Sinhala) won't print on the built-in font — use the HTML receipt for that.
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

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
