package icons

import (
	"context"
	"strings"
	"testing"
)

// requiredIcons MUST all be registered — these are consumed by nav, areas,
// and common actions in later phases.
var requiredIcons = []string{
	// areas / nav
	"home", "box", "boxes", "receipt", "cart", "wallet", "chart", "settings",
	"users", "truck",
	// actions
	"plus", "search", "edit", "trash", "check", "x", "chevron-right",
	"chevron-down", "menu", "filter", "download", "print", "refresh", "logout",
	"palette",
}

func TestRequiredIconsRegistered(t *testing.T) {
	for _, n := range requiredIcons {
		if !Has(n) {
			t.Errorf("required icon %q not registered", n)
		}
	}
}

func TestIconRenders(t *testing.T) {
	var b strings.Builder
	if err := Icon("box", "w-5 h-5 text-area-inventory").Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"<svg", `viewBox="0 0 24 24"`, `stroke="currentColor"`, "w-5 h-5 text-area-inventory"} {
		if !strings.Contains(out, want) {
			t.Errorf("Icon render missing %q in:\n%s", want, out)
		}
	}
}

func TestUnknownIconIsSafe(t *testing.T) {
	var b strings.Builder
	if err := Icon("__nope__", "w-4 h-4").Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "<svg") || !strings.Contains(out, "</svg>") {
		t.Errorf("unknown icon should still render a valid empty svg, got:\n%s", out)
	}
}
