package escpos

import (
	"bytes"

	"karots-pos/internal/features/settings"
)

// ReceiptStyle is the system-user-locked look of the printed receipt. It only
// tunes the shared Header/Footer (where a shop's branding lives), so every core
// and plugin receipt inherits the style for free — the per-row helpers (Line,
// Divider, LeftRight) are unchanged. Keys mirror settings.ReceiptStyles.
type ReceiptStyle struct {
	Key string
	// Rule is a full-width character drawn as a line under the header block (and
	// above the footer). "" draws no rule.
	Rule string
	// NameDouble prints the shop name double-width+height when it fits; false
	// prints it double-height-only (a smaller, plainer header).
	NameDouble bool
	// ThankYou prints the "Thank you! Come again." line in the footer.
	ThankYou bool
	// Breathing keeps the blank spacer lines around the header/footer; false
	// tightens the receipt (less paper).
	Breathing bool
}

// StyleFor resolves the receipt style from settings (unknown → classic).
func StyleFor(cfg settings.Settings) ReceiptStyle {
	switch settings.ValidReceiptStyle(cfg.ReceiptStyle) {
	case "compact":
		return ReceiptStyle{Key: "compact", Rule: "", NameDouble: false, ThankYou: false, Breathing: false}
	case "bold":
		return ReceiptStyle{Key: "bold", Rule: "=", NameDouble: true, ThankYou: true, Breathing: true}
	case "minimal":
		return ReceiptStyle{Key: "minimal", Rule: "", NameDouble: true, ThankYou: false, Breathing: false}
	default: // classic
		return ReceiptStyle{Key: "classic", Rule: "-", NameDouble: true, ThankYou: true, Breathing: true}
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
	divider(&b, w)
	bigLine(&b, "TOTAL", "800.00", w)
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
