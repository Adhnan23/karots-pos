package escpos

import (
	"bytes"
	"strings"
	"testing"

	"karots-pos/internal/features/settings"
)

func TestStyleForFallsBackToClassic(t *testing.T) {
	if StyleFor(settings.Settings{ReceiptStyle: "nope"}).Key != "classic" {
		t.Fatal("unknown style must resolve to classic")
	}
	if StyleFor(settings.Settings{ReceiptStyle: "bold"}).Key != "bold" {
		t.Fatal("known style must pass through")
	}
}

func TestStylesAreDistinct(t *testing.T) {
	bold := StyleFor(settings.Settings{ReceiptStyle: "bold"})
	compact := StyleFor(settings.Settings{ReceiptStyle: "compact"})
	if bold.Rule == "" {
		t.Error("bold should draw a rule")
	}
	if compact.ThankYou {
		t.Error("compact should omit the thank-you line")
	}
	if bold.Rule == compact.Rule && bold.ThankYou == compact.ThankYou {
		t.Error("bold and compact must differ visibly")
	}
}

// Footer output must reflect the style: bold prints a rule + thank-you; compact
// prints neither.
func TestFooterHonoursStyle(t *testing.T) {
	render := func(style string) string {
		var b bytes.Buffer
		Footer(&b, settings.Settings{ReceiptWidth: "80", ReceiptStyle: style})
		return b.String()
	}
	bold := render("bold")
	if !strings.Contains(bold, "====") {
		t.Error("bold footer should contain a '=' rule")
	}
	if !strings.Contains(bold, "Thank you") {
		t.Error("bold footer should thank the customer")
	}
	compact := render("compact")
	if strings.Contains(compact, "Thank you") {
		t.Error("compact footer should omit the thank-you line")
	}
	if strings.Contains(compact, "====") {
		t.Error("compact footer should have no rule")
	}
}
