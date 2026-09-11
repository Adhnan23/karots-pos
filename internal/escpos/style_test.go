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
	if StyleFor(settings.Settings{ReceiptStyle: "boxed"}).Key != "boxed" {
		t.Fatal("known style must pass through")
	}
}

// The four profiles must be far apart on the axes a customer sees: body
// separator, header alignment/framing, and total treatment. If two profiles
// ever collapse onto the same combination, they'd print "a little bit same" —
// the exact complaint this rework fixes.
func TestStylesAreDistinct(t *testing.T) {
	keys := settings.ReceiptStyles // classic, modern, boxed, compact
	seen := map[string]string{}
	for _, k := range keys {
		s := StyleFor(settings.Settings{ReceiptStyle: k})
		sig := s.Body + "|left=" + b2s(s.HeaderLeft) + "|frame=" + b2s(s.Framed) + "|total=" + s.TotalMode
		if other, dup := seen[sig]; dup {
			t.Errorf("styles %q and %q are visually identical (%s)", other, k, sig)
		}
		seen[sig] = k
	}
	if len(seen) != len(keys) {
		t.Fatalf("got %d distinct looks, want %d", len(seen), len(keys))
	}
}

// The body separator (the receipt's skeleton) must actually change per style.
func TestDividerHonoursStyle(t *testing.T) {
	render := func(style string) string {
		var b bytes.Buffer
		divider(&b, settings.Settings{ReceiptWidth: "80", ReceiptStyle: style}, 48)
		return b.String()
	}
	classic := render("classic") // full dash rule
	modern := render("modern")   // blank line, no rule
	boxed := render("boxed")     // dotted
	if !strings.Contains(classic, "----") {
		t.Error("classic divider should be a dash rule")
	}
	if strings.TrimSpace(modern) != "" {
		t.Error("modern divider should be blank whitespace")
	}
	if !strings.Contains(boxed, "....") {
		t.Error("boxed divider should be dotted")
	}
}

// The TOTAL emphasis must differ: classic is double-height (GS ! 0x01), boxed is
// reverse-video (GS B 1), modern/compact are a plain bold line (neither).
func TestTotalModeHonoursStyle(t *testing.T) {
	render := func(style string) []byte {
		var b bytes.Buffer
		bigLine(&b, settings.Settings{ReceiptWidth: "80", ReceiptStyle: style}, "TOTAL", "800.00", 48)
		return b.Bytes()
	}
	if !bytes.Contains(render("classic"), []byte{gs, '!', 0x01}) {
		t.Error("classic TOTAL should be double-height (GS ! 0x01)")
	}
	if !bytes.Contains(render("boxed"), []byte{gs, 'B', 1}) {
		t.Error("boxed TOTAL should be reverse-video (GS B 1)")
	}
	modern := render("modern")
	if bytes.Contains(modern, []byte{gs, '!', 0x01}) || bytes.Contains(modern, []byte{gs, 'B', 1}) {
		t.Error("modern TOTAL should be a plain bold line, neither double nor reverse")
	}
}

// Footer still honours the thank-you / rule knobs.
func TestFooterHonoursStyle(t *testing.T) {
	render := func(style string) string {
		var b bytes.Buffer
		Footer(&b, settings.Settings{ReceiptWidth: "80", ReceiptStyle: style})
		return b.String()
	}
	if !strings.Contains(render("classic"), "Thank you") {
		t.Error("classic footer should thank the customer")
	}
	if strings.Contains(render("compact"), "Thank you") {
		t.Error("compact footer should omit the thank-you line")
	}
}

func b2s(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
