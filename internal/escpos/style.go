package escpos

import "karots-pos/internal/features/settings"

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
