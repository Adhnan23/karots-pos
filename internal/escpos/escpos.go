// Package escpos renders a sale as an ESC/POS byte stream for a thermal receipt
// printer (e.g. Xprinter POS-80). It uses the printer's built-in font rather
// than raster graphics, so printing is fast and immune to the PDF/codepage
// garbage that browser printing to a raw CUPS queue produces. The width is
// driven by the shop's receipt_width setting (80mm = 48 cols, 58mm = 32 cols).
package escpos

import (
	"bytes"
	"strconv"
	"strings"
	"time"

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
	// Serials maps a product id to pre-formatted serial lines for serial-tracked
	// items (e.g. "SN: ABC123 (wty 2027-06-13)"); printed under each matching line.
	Serials map[int64][]string
	// CustomerDue is the customer's outstanding balance after this sale (prior
	// debt + this sale's unpaid portion). Used only on credit/partial sales to
	// print the "Total due" line; the zero value means no balance line.
	CustomerDue decimal.Decimal
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
	b.Write([]byte{gs, '!', 0x00}) // normal size
	b.Write([]byte{esc, 'E', 0})   // bold off
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
		for _, sn := range opts.Serials[it.ProductID] {
			for _, ln := range wrap(ascii(sn), w) {
				line(&b, "  "+ln)
			}
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
	// TOTAL — emphasized and set off with blank lines so it stands out.
	line(&b, "")
	bigLine(&b, "TOTAL", money.Format(sym, d.Sale.Total), w)

	// Payment breakdown: one line per tender (cash/card/online/...). This
	// replaces the old single "Paid" row — the per-method lines already show it.
	if len(d.Payments) > 0 {
		line(&b, "")
		for _, p := range d.Payments {
			line(&b, leftRight(capitalize(ascii(p.Method)), money.Format(sym, p.Amount), w))
		}
	}

	// Change/Due last, emphasized. A credit/partial sale shows what is still
	// owed (Due) instead of Change, plus the customer's running Total due — shown
	// on every due receipt, whether this is their first due or a continuing
	// balance. Both appear only when money is actually outstanding (thisDue > 0).
	thisDue := d.Sale.Total.Sub(d.Sale.PaidAmount)
	switch {
	case d.Sale.Status == "credit" && thisDue.IsPositive():
		line(&b, "")
		bigLine(&b, "DUE", money.Format(sym, thisDue), w)
		// Total due = the customer's running balance. Fall back to this sale's
		// due if the balance lookup was unavailable, so we never print 0.00.
		totalDue := opts.CustomerDue
		if totalDue.LessThan(thisDue) {
			totalDue = thisDue
		}
		bigLine(&b, "TOTAL DUE", money.Format(sym, totalDue), w)
	case d.Sale.ChangeGiven.IsPositive():
		line(&b, "")
		bigLine(&b, "CHANGE", money.Format(sym, d.Sale.ChangeGiven), w)
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

// TestDocument builds a short ESC/POS slip used to verify printer wiring from the
// settings page: it prints the shop name, a marker and a timestamp, then cuts —
// just enough to confirm bytes reach the printer at the configured width.
func TestDocument(cfg settings.Settings) []byte {
	w := columns(cfg.ReceiptWidth)

	var b bytes.Buffer
	b.Write([]byte{esc, '@'})    // initialize
	b.Write([]byte{esc, 't', 0}) // code page PC437 (Latin)

	b.Write([]byte{esc, 'a', 1}) // center
	b.Write([]byte{esc, 'E', 1}) // bold on
	b.Write([]byte{gs, '!', 0x11})
	name := cfg.ShopName
	if strings.TrimSpace(name) == "" {
		name = "POS"
	}
	line(&b, ascii(name))
	b.Write([]byte{gs, '!', 0x00})
	b.Write([]byte{esc, 'E', 0})
	line(&b, "")
	line(&b, "*** PRINTER TEST ***")
	line(&b, "")

	b.Write([]byte{esc, 'a', 0}) // left
	divider(&b, w)
	line(&b, leftRight("Width:", cfg.ReceiptWidth+"mm ("+strconv.Itoa(w)+" cols)", w))
	line(&b, leftRight("Printed:", datetime.DateTime(time.Now()), w))
	divider(&b, w)

	b.Write([]byte{esc, 'a', 1}) // center
	line(&b, "If you can read this,")
	line(&b, "your printer is set up.")

	b.Write([]byte{esc, 'd', feedBeforeCut})
	b.Write([]byte{gs, 'V', 1})
	return b.Bytes()
}

// ReturnDocument builds the ESC/POS byte stream for a refund slip — the proof a
// customer is handed after a return. It mirrors Document's header/footer but
// lists only the returned lines and the refund/credit split.
func ReturnDocument(rr sales.ReturnReceipt, cfg settings.Settings, opts Options) []byte {
	w := columns(cfg.ReceiptWidth)
	sym := cfg.CurrencySymbol
	if sym == "" {
		sym = "Rs."
	}

	var b bytes.Buffer
	b.Write([]byte{esc, '@'})
	b.Write([]byte{esc, 't', 0})

	// --- Header (centered) ---
	b.Write([]byte{esc, 'a', 1})
	if len(opts.Logo) > 0 {
		b.Write(opts.Logo)
		line(&b, "")
	}
	b.Write([]byte{esc, 'E', 1})
	b.Write([]byte{gs, '!', 0x11})
	line(&b, ascii(cfg.ShopName))
	b.Write([]byte{gs, '!', 0x00})
	b.Write([]byte{esc, 'E', 0})
	if len(opts.SubName) > 0 {
		b.Write(opts.SubName)
	}
	line(&b, "")
	b.Write([]byte{esc, 'E', 1})
	line(&b, "*** REFUND ***")
	b.Write([]byte{esc, 'E', 0})

	// --- Meta (left) ---
	b.Write([]byte{esc, 'a', 0})
	divider(&b, w)
	line(&b, leftRight("Orig. receipt:", rr.ReceiptNo, w))
	line(&b, leftRight("Date:", datetime.DateTime(rr.CreatedAt), w))
	if rr.CustomerName != nil && *rr.CustomerName != "" {
		line(&b, leftRight("Customer:", ascii(*rr.CustomerName), w))
	}
	if rr.Reason != nil && *rr.Reason != "" {
		for _, ln := range wrap("Reason: "+ascii(*rr.Reason), w) {
			line(&b, ln)
		}
	}
	divider(&b, w)

	// --- Returned items ---
	for _, it := range rr.Items {
		for _, ln := range wrap(ascii(it.ProductName), w) {
			line(&b, ln)
		}
		qty := money.Display(it.Quantity) + " " + ascii(it.UnitAbbr)
		line(&b, leftRight("  "+qty, money.Format(sym, it.Refund), w))
	}
	divider(&b, w)

	// --- Totals ---
	b.Write([]byte{esc, 'E', 1})
	line(&b, leftRight("CASH REFUND", money.Format(sym, rr.Refund), w))
	b.Write([]byte{esc, 'E', 0})
	if rr.CreditReduction.IsPositive() {
		line(&b, leftRight("Credit reduced", money.Format(sym, rr.CreditReduction), w))
	}
	if rr.RemainingBalance != nil {
		line(&b, leftRight("Balance due", money.Format(sym, *rr.RemainingBalance), w))
	}
	divider(&b, w)

	// --- Footer (centered) ---
	b.Write([]byte{esc, 'a', 1})
	line(&b, "Refund slip - please retain.")

	b.Write([]byte{esc, 'd', feedBeforeCut})
	b.Write([]byte{gs, 'V', 1})

	return b.Bytes()
}

// WarrantySlip is the data for a printed warranty-replacement slip — the proof
// a customer is handed when a faulty unit is swapped for a new one.
type WarrantySlip struct {
	ProductName   string
	OldSerial     string
	NewSerial     string
	WarrantyUntil string // pre-formatted date (e.g. "2027-06-13")
	WarrantyLeft  string // pre-formatted remaining cover (e.g. "11 mo left"); optional
	CustomerName  string
}

// WarrantyDocument builds the ESC/POS byte stream for a replacement slip. It
// mirrors the receipt header/footer and lists the swapped serials.
func WarrantyDocument(s WarrantySlip, cfg settings.Settings, opts Options) []byte {
	w := columns(cfg.ReceiptWidth)

	var b bytes.Buffer
	b.Write([]byte{esc, '@'})
	b.Write([]byte{esc, 't', 0})

	// --- Header (centered) ---
	b.Write([]byte{esc, 'a', 1})
	if len(opts.Logo) > 0 {
		b.Write(opts.Logo)
		line(&b, "")
	}
	b.Write([]byte{esc, 'E', 1})
	b.Write([]byte{gs, '!', 0x11})
	line(&b, ascii(cfg.ShopName))
	b.Write([]byte{gs, '!', 0x00})
	b.Write([]byte{esc, 'E', 0})
	if len(opts.SubName) > 0 {
		b.Write(opts.SubName)
	}
	line(&b, "")
	b.Write([]byte{esc, 'E', 1})
	line(&b, "*** WARRANTY REPLACEMENT ***")
	b.Write([]byte{esc, 'E', 0})

	// --- Body (left) ---
	b.Write([]byte{esc, 'a', 0})
	divider(&b, w)
	line(&b, leftRight("Date:", datetime.Date(time.Now()), w))
	if s.CustomerName != "" {
		line(&b, leftRight("Customer:", ascii(s.CustomerName), w))
	}
	for _, ln := range wrap("Product: "+ascii(s.ProductName), w) {
		line(&b, ln)
	}
	divider(&b, w)
	for _, ln := range wrap("Returned serial: "+ascii(s.OldSerial), w) {
		line(&b, ln)
	}
	b.Write([]byte{esc, 'E', 1})
	for _, ln := range wrap("New serial: "+ascii(s.NewSerial), w) {
		line(&b, ln)
	}
	b.Write([]byte{esc, 'E', 0})
	if s.WarrantyUntil != "" {
		until := s.WarrantyUntil
		if s.WarrantyLeft != "" {
			until += " (" + s.WarrantyLeft + ")"
		}
		line(&b, leftRight("Warranty until:", ascii(until), w))
	}
	divider(&b, w)

	// --- Footer (centered) ---
	b.Write([]byte{esc, 'a', 1})
	line(&b, "Replacement slip - please retain.")

	b.Write([]byte{esc, 'd', feedBeforeCut})
	b.Write([]byte{gs, 'V', 1})

	return b.Bytes()
}

// DebtSlip is the data for a printed credit payment slip — the proof a
// customer is handed after making a payment toward their outstanding balance.
type DebtSlip struct {
	ReceiptNo, Date, CustomerName, CustomerPhone, Method, CashierName string
	Amount                                                            decimal.Decimal
	BalanceBefore, BalanceAfter, CreditLimit                          *decimal.Decimal
}

// DebtDocument builds the ESC/POS byte stream for a credit payment slip.
// It mirrors the receipt header/footer and lists the payment details and
// updated balance (if available).
func DebtDocument(s DebtSlip, cfg settings.Settings, opts Options) []byte {
	w := columns(cfg.ReceiptWidth)
	sym := cfg.CurrencySymbol
	if sym == "" {
		sym = "Rs."
	}
	var b bytes.Buffer
	b.Write([]byte{esc, '@'})
	b.Write([]byte{esc, 't', 0})
	// header
	b.Write([]byte{esc, 'a', 1})
	if len(opts.Logo) > 0 {
		b.Write(opts.Logo)
		line(&b, "")
	}
	b.Write([]byte{esc, 'E', 1})
	b.Write([]byte{gs, '!', 0x11})
	line(&b, ascii(cfg.ShopName))
	b.Write([]byte{gs, '!', 0x00})
	b.Write([]byte{esc, 'E', 0})
	if len(opts.SubName) > 0 {
		b.Write(opts.SubName)
	}
	line(&b, "")
	b.Write([]byte{esc, 'E', 1})
	line(&b, "*** CREDIT PAYMENT ***")
	b.Write([]byte{esc, 'E', 0})
	// meta
	b.Write([]byte{esc, 'a', 0})
	divider(&b, w)
	line(&b, leftRight("Receipt:", s.ReceiptNo, w))
	line(&b, leftRight("Date:", s.Date, w))
	line(&b, leftRight("Customer:", ascii(s.CustomerName), w))
	if s.CustomerPhone != "" {
		line(&b, leftRight("Phone:", ascii(s.CustomerPhone), w))
	}
	divider(&b, w)
	// amount
	b.Write([]byte{esc, 'E', 1})
	line(&b, leftRight("Amount paid", money.Format(sym, s.Amount), w))
	b.Write([]byte{esc, 'E', 0})
	line(&b, leftRight("Method:", s.Method, w))
	// balances (omitted for backfilled rows)
	if s.BalanceBefore != nil && s.BalanceAfter != nil {
		divider(&b, w)
		line(&b, leftRight("Previous balance", money.Format(sym, *s.BalanceBefore), w))
		b.Write([]byte{esc, 'E', 1})
		line(&b, leftRight("Remaining balance", money.Format(sym, *s.BalanceAfter), w))
		b.Write([]byte{esc, 'E', 0})
		if s.CreditLimit != nil {
			line(&b, leftRight("Credit limit", money.Format(sym, *s.CreditLimit), w))
		}
	}
	divider(&b, w)
	b.Write([]byte{esc, 'a', 1})
	if s.CashierName != "" {
		line(&b, "Served by: "+ascii(s.CashierName))
	}
	line(&b, "Thank you - please retain.")
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

// bigLine prints a label/amount line in double-height bold text for emphasis
// (TOTAL, CHANGE, DUE, TOTAL DUE). Double-height only (not double-width) keeps
// the character count the same, so leftRight's padding still lines up on the
// 48/58-column roll without risk of overflow.
func bigLine(b *bytes.Buffer, l, r string, w int) {
	b.Write([]byte{esc, 'E', 1})   // bold on
	b.Write([]byte{gs, '!', 0x01}) // double height
	line(b, leftRight(l, r, w))
	b.Write([]byte{gs, '!', 0x00}) // normal size
	b.Write([]byte{esc, 'E', 0})   // bold off
}

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
// ASCII sanitises an arbitrary string to the printable ASCII subset the built-in
// thermal font can render (anything else becomes '?'). Exported for callers that
// hand-build slips outside this package (e.g. the cashflow money receipt), so a
// stray em-dash or a non-Latin name can't reach the printer as codepage garbage.
func ASCII(s string) string { return ascii(s) }

func ascii(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 0x20 && r < 0x7F:
			b.WriteRune(r)
		case r == '\n' || r == '\t':
			b.WriteByte(' ')
		// Map the common typographic punctuation to its ASCII equivalent so a
		// stray "smart" character prints as itself, not as '?'. Em/en/figure
		// dashes and the horizontal bar all become a plain hyphen.
		case r == '‐' || r == '‑' || r == '‒' || r == '–' ||
			r == '—' || r == '―' || r == '−':
			b.WriteByte('-')
		case r == '‘' || r == '’' || r == '‚' || r == '′':
			b.WriteByte('\'')
		case r == '“' || r == '”' || r == '„' || r == '″':
			b.WriteByte('"')
		case r == '…': // ellipsis
			b.WriteString("...")
		case r == ' ': // non-breaking space
			b.WriteByte(' ')
		case r == '•' || r == '·': // bullet / middle dot
			b.WriteByte('*')
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
