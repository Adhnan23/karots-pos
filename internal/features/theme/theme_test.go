package theme

import (
	"strings"
	"testing"
)

func TestContrastFg(t *testing.T) {
	tests := []struct {
		name, hex, want string
	}{
		{"dark accent -> white fg", "#4f46e5", "#ffffff"},
		{"light accent -> dark fg", "#fde047", "#0f172a"},
		{"black -> white", "#000000", "#ffffff"},
		{"white -> dark", "#ffffff", "#0f172a"},
		{"short hex ok", "#fff", "#0f172a"},
		{"invalid falls back to white", "nonsense", "#ffffff"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := contrastFg(tc.hex); got != tc.want {
				t.Errorf("contrastFg(%q) = %q, want %q", tc.hex, got, tc.want)
			}
		})
	}
}

func TestCSSVarsContainsContract(t *testing.T) {
	css := CSSVars(Theme{Palette: "emerald", Mode: "auto", Density: "comfortable"})
	for _, v := range []string{
		"--surface:", "--text:", "--accent:", "--accent-fg:", "--area-sell:",
		"--area-money:", "--control-h:", "--radius:",
	} {
		if !strings.Contains(css, v) {
			t.Errorf("CSSVars missing variable %q", v)
		}
	}
	if !strings.Contains(css, ":root") || !strings.Contains(css, ".dark") {
		t.Errorf("CSSVars must emit both :root and .dark blocks")
	}
}

func TestCSSVarsPaletteAndDensityFallback(t *testing.T) {
	// Unknown palette -> classic accent; unknown density -> comfortable control height.
	css := CSSVars(Theme{Palette: "does-not-exist", Mode: "light", Density: "bogus"})
	if !strings.Contains(css, palettes["classic"].Accent) {
		t.Errorf("unknown palette should fall back to classic accent")
	}
	if !strings.Contains(css, densities["comfortable"].ControlH) {
		t.Errorf("unknown density should fall back to comfortable")
	}
}

func TestCSSVarsCustomAccentOverrides(t *testing.T) {
	custom := "#aa0000"
	css := CSSVars(Theme{Palette: "emerald", Mode: "auto", Density: "comfortable", Accent: &custom})
	if !strings.Contains(css, "--accent:"+custom) {
		t.Errorf("custom accent should override palette accent")
	}
}

func TestCurrentCSSCache(t *testing.T) {
	SetCurrentCSS("/*x*/")
	if CurrentCSS() != "/*x*/" {
		t.Errorf("CurrentCSS should return the value set by SetCurrentCSS")
	}
}
