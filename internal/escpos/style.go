package escpos

import (
	"bytes"

	"karots-pos/internal/features/settings"
)

// ReceiptStyle is the system-user-locked look of the printed receipt. It tunes
// the shared Header/Footer AND the body separators/total so two shops on
// different styles print visibly different receipts — the whole point of the
// white-label look. Every core and plugin receipt inherits it for free because
// they all route through the shared primitives (Header, Footer, Divider,
// bigLine, Title). Keys mirror settings.ReceiptStyles; the web view mirrors the
// same keys in CSS (see .receipt[data-rstyle] in app.css).
type ReceiptStyle struct {
	Key string
	// HeaderRule is a full-width character drawn under the header block and above
	// the footer. "" draws no rule (the airier / framed looks).
	HeaderRule string
	// Body picks the separator drawn between the meta / items / totals sections
	// down the body — the receipt's visual skeleton, the biggest differentiator:
	//   "dash"    full-width "-"      (classic)
	//   "blank"   an empty line       (modern — whitespace, no rules)
	//   "dots"    full-width "."      (boxed)
	//   "compact" a short "-" run     (compact chit)
	Body string
	// HeaderLeft left-aligns the header block instead of centering it.
	HeaderLeft bool
	// Framed boxes the header block with an ASCII +==+ / | … | border.
	Framed bool
	// NameDouble prints the shop name double-width+height when it fits; false
	// prints it double-height-only (a smaller, plainer header).
	NameDouble bool
	// TotalMode is how the TOTAL line is emphasised:
	//   "double"  double-height bold (classic)
	//   "reverse" reverse-video (white-on-black) bar (boxed)
	//   "bold"    bold single line (modern, compact)
	TotalMode string
	// TitleDeco formats a receipt title (e.g. "REFUND"); must contain one %s.
	TitleDeco string
	// ThankYou prints the "Thank you! Come again." line in the footer.
	ThankYou bool
	// Breathing keeps the blank spacer lines around the header/footer; false
	// tightens the receipt (less paper).
	Breathing bool
}

// StyleFor resolves the receipt style from settings (unknown → classic). The
// four profiles are deliberately far apart — separator, header alignment,
// framing and total treatment all differ — so no two look "a little bit same".
func StyleFor(cfg settings.Settings) ReceiptStyle {
	switch settings.ValidReceiptStyle(cfg.ReceiptStyle) {
	case "modern": // left-aligned, no rule lines, airy whitespace, plain bold total
		return ReceiptStyle{Key: "modern", HeaderRule: "", Body: "blank", HeaderLeft: true, Framed: false, NameDouble: false, TotalMode: "bold", TitleDeco: "%s", ThankYou: true, Breathing: true}
	case "boxed": // framed header, dotted separators, reverse-video total bar
		return ReceiptStyle{Key: "boxed", HeaderRule: "", Body: "dots", HeaderLeft: false, Framed: true, NameDouble: true, TotalMode: "reverse", TitleDeco: "[ %s ]", ThankYou: true, Breathing: true}
	case "compact": // dense, short rules, no thank-you, no breathing
		return ReceiptStyle{Key: "compact", HeaderRule: "-", Body: "compact", HeaderLeft: false, Framed: false, NameDouble: false, TotalMode: "bold", TitleDeco: "%s", ThankYou: false, Breathing: false}
	default: // classic: centered, dashed rules, double-height total
		return ReceiptStyle{Key: "classic", HeaderRule: "-", Body: "dash", HeaderLeft: false, Framed: false, NameDouble: true, TotalMode: "double", TitleDeco: "*** %s ***", ThankYou: true, Breathing: true}
	}
}

// SamplePreview renders a small sample receipt for cfg and returns it as plain
// text — the SAME Header/Footer/rows the real printer gets, with the ESC/POS
// control sequences stripped — so the appearance panel can show exactly how the
// chosen ReceiptStyle looks (rule, name size, thank-you, spacing) without a
// separate mock that could drift. No logo/sub-name raster is used.
func SamplePreview(cfg settings.Settings) string {
	var b bytes.Buffer
	w := columns(cfg.ReceiptWidth)
	Header(&b, cfg, Options{})
	line(&b, leftRight("Widget", "500.00", w))
	line(&b, leftRight("Gadget x2", "300.00", w))
	divider(&b, cfg, w)
	bigLine(&b, cfg, "TOTAL", "800.00", w)
	Footer(&b, cfg)
	return plainText(b.Bytes())
}

// plainText strips the ESC/POS control sequences this package emits, leaving the
// human-readable lines (spacing/centering preserved). It knows only the commands
// used by Header/Footer/Title/bigLine — enough for the sample preview.
func plainText(raw []byte) string {
	var out bytes.Buffer
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch c {
		case esc: // ESC cmd [param]
			if i+1 < len(raw) {
				cmd := raw[i+1]
				i++
				if cmd != '@' && i+1 < len(raw) { // '@' (init) has no parameter
					i++
				}
			}
		case gs: // GS cmd param
			if i+2 < len(raw) {
				i += 2
			}
		case '\r':
			// drop
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}
