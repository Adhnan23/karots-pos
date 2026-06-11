// Package escpos renders a sale as an ESC/POS byte stream for a thermal receipt
// printer (e.g. Xprinter POS-80). It uses the printer's built-in font rather
// than raster graphics, so printing is fast and immune to the PDF/codepage
// garbage that browser printing to a raw CUPS queue produces. The width is
// driven by the shop's receipt_width setting (80mm = 48 cols, 58mm = 32 cols).
package escpos

import (
	"bytes"
	"strings"

	"karots-pos/internal/datetime"
	"karots-pos/internal/features/sales"
	"karots-pos/internal/features/settings"
	"karots-pos/internal/money"

	"github.com/shopspring/decimal"
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

// Options carries pre-rendered raster blocks (ESC/POS GS v 0) that the built-in
// font can't produce: the shop logo and the secondary (non-Latin) shop name.
// Both are optional; nil means "skip". They are produced by internal/receiptimg.
type Options struct {
	Logo    []byte // raster for the logo (top of the receipt)
	SubName []byte // raster for the secondary shop name (under the main name)
}

// Document builds the ESC/POS byte stream for a completed sale.
func Document(d sales.Detail, cfg settings.Settings, opts Options) []byte {
	w := columns(cfg.ReceiptWidth)
	sym := cfg.CurrencySymbol
	if sym == "" {
		sym = "Rs."
	}

	var b bytes.Buffer
	b.Write([]byte{esc, '@'})    // initialize
	b.Write([]byte{esc, 't', 0}) // code page PC437 (Latin)

	// --- Header (centered) ---
	b.Write([]byte{esc, 'a', 1}) // center
	// Logo at the very top (rendered as a full-width raster, centered on canvas).
	if len(opts.Logo) > 0 {
		b.Write(opts.Logo)
		line(&b, "")
	}
	b.Write([]byte{esc, 'E', 1})   // bold on
	b.Write([]byte{gs, '!', 0x11}) // double width + height
	line(&b, ascii(cfg.ShopName))
	b.Write([]byte{gs, '!', 0x00})  // normal size
	b.Write([]byte{esc, 'E', 0})    // bold off
	// Secondary shop name (Sinhala/Tamil) is rendered as an image by the caller
	// because the built-in font can't draw it; printed here if supplied.
	if len(opts.SubName) > 0 {
		b.Write(opts.SubName)
	}
	line(&b, "") // breathing room between the name and the address block
	if s := deref(cfg.Address); s != "" {
		for _, ln := range wrap(ascii(s), w) {
			line(&b, ln)
		}
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
	line(&b, leftRight("Date:", datetime.DateTime(d.Sale.CreatedAt), w))
	line(&b, leftRight("Cashier:", ascii(d.Sale.CashierName), w))
	if d.Sale.CustomerName != nil && *d.Sale.CustomerName != "" {
		line(&b, leftRight("Customer:", ascii(*d.Sale.CustomerName), w))
	}
	divider(&b, w)

	// --- Items ---
	itemDisc := decimal.Zero
	for _, it := range d.Items {
		for _, ln := range wrap(ascii(it.ProductName), w) {
			line(&b, ln)
		}
		qty := money.Display(it.Quantity) + " " + ascii(it.UnitAbbr) + " x " + money.Display(it.UnitPrice)
		line(&b, leftRight("  "+qty, money.Display(it.Subtotal), w))
		if it.Discount.IsPositive() {
			itemDisc = itemDisc.Add(it.Discount)
			line(&b, leftRight("  Discount"+discSuffix(it.DiscountType, it.DiscountValue), "-"+money.Display(it.Discount), w))
		}
	}
	divider(&b, w)

	// --- Totals ---
	// Sale.Discount holds item discounts + bill discount; split them so the
	// receipt mirrors the cashier screen (item discounts shown per line above).
	line(&b, leftRight("Subtotal", money.Format(sym, d.Sale.Subtotal), w))
	if itemDisc.IsPositive() {
		line(&b, leftRight("Item discounts", "-"+money.Format(sym, itemDisc), w))
	}
	if billDisc := d.Sale.Discount.Sub(itemDisc); billDisc.IsPositive() {
		line(&b, leftRight("Bill discount"+discSuffix(d.Sale.DiscountType, d.Sale.DiscountValue), "-"+money.Format(sym, billDisc), w))
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
		for _, ln := range wrap(ascii(s), w) {
			line(&b, ln)
		}
	}
	line(&b, "Thank you! Come again.")

	// Credit line in the small built-in font (Font B), centered.
	line(&b, "")
	b.Write([]byte{esc, 'M', 1}) // select Font B (smaller)
	line(&b, "POS built by Adhnan")
	line(&b, "adhnanmsa@gmail.com | 0769626396")
	b.Write([]byte{esc, 'M', 0}) // back to Font A

	// Feed past the head-to-cutter gap (plus a little margin) before cutting, so
	// the last printed line clears the blade instead of being sheared at the end.
	b.Write([]byte{esc, 'd', feedBeforeCut})
	b.Write([]byte{gs, 'V', 1})

	return b.Bytes()
}

// wrap breaks s into lines of at most w characters, preferring to break at
// spaces. A single word longer than w is hard-split so it never overflows.
func wrap(s string, w int) []string {
	if w <= 0 {
		return []string{s}
	}
	var lines []string
	cur := ""
	for _, word := range strings.Fields(s) {
		for len(word) > w { // word itself too long: hard-split
			if cur != "" {
				lines = append(lines, cur)
				cur = ""
			}
			lines = append(lines, word[:w])
			word = word[w:]
		}
		switch {
		case cur == "":
			cur = word
		case len(cur)+1+len(word) <= w:
			cur += " " + word
		default:
			lines = append(lines, cur)
			cur = word
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
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

// pctLabel renders a percentage value without trailing zeros (10.00 -> "10%").
func pctLabel(v decimal.Decimal) string {
	s := v.String()
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s + "%"
}

// discSuffix is the parenthetical shown next to a discount entered as a
// percentage, e.g. " (10%)"; blank for a fixed amount.
func discSuffix(dtype string, value decimal.Decimal) string {
	if dtype == "percent" && value.IsPositive() {
		return " (" + pctLabel(value) + ")"
	}
	return ""
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
