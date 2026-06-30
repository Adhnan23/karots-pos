// Package theme owns the design-token model: a Theme (palette + mode + density +
// optional custom accent) renders to a block of CSS variables that the whole UI
// reads. The active theme's rendered CSS is cached process-wide and injected by
// the base layout, so switching themes recolors/resizes the entire app.
package theme

import (
	"strconv"
	"strings"
	"sync/atomic"
)

// Theme is one saved appearance bundle.
type Theme struct {
	ID        int64   `db:"id"         json:"id"`
	Name      string  `db:"name"       json:"name"`
	Palette   string  `db:"palette"    json:"palette"`
	Mode      string  `db:"mode"       json:"mode"`    // light | dark | auto
	Density   string  `db:"density"    json:"density"` // comfortable | compact | large_touch
	Accent    *string `db:"accent"     json:"accent,omitempty"`
	IsBuiltin bool    `db:"is_builtin" json:"is_builtin"`
}

// palette = the brand/interactive accent family (the customizable part). Area
// colors and status colors are fixed (wayfinding must stay consistent).
type palette struct{ Accent, AccentWeak string }

var palettes = map[string]palette{
	"classic": {"#4f46e5", "#e0e7ff"}, // indigo
	"emerald": {"#059669", "#d1fae5"},
	"ocean":   {"#0284c7", "#e0f2fe"},
	"sunset":  {"#ea580c", "#ffedd5"},
}

type density struct{ ControlH, Space, Radius string }

var densities = map[string]density{
	"comfortable": {"2.75rem", "1rem", "0.75rem"},
	"compact":     {"2.25rem", "0.75rem", "0.5rem"},
	"large_touch": {"3.25rem", "1.25rem", "1rem"},
}

// Fixed neutrals per mode.
var neutralsLight = map[string]string{
	"--surface": "#ffffff", "--surface-2": "#f8fafc", "--border": "#e2e8f0",
	"--text": "#0f172a", "--text-muted": "#64748b",
}
var neutralsDark = map[string]string{
	"--surface": "#0f172a", "--surface-2": "#1e293b", "--border": "#334155",
	"--text": "#f1f5f9", "--text-muted": "#94a3b8",
}

// Fixed wayfinding + status colors (same in both modes).
var fixedColors = map[string]string{
	"--success": "#16a34a", "--warning": "#d97706", "--danger": "#dc2626", "--info": "#2563eb",
	"--area-sell": "#059669", "--area-inventory": "#2563eb", "--area-purchasing": "#7c3aed",
	"--area-money": "#d97706", "--area-reports": "#0891b2", "--area-setup": "#475569",
}

// CSSVars renders the :root (light) and .dark variable blocks for a theme.
func CSSVars(t Theme) string {
	p, ok := palettes[t.Palette]
	if !ok {
		p = palettes["classic"]
	}
	accent := p.Accent
	if t.Accent != nil && isHex(*t.Accent) {
		accent = *t.Accent
	}
	accentFg := contrastFg(accent)

	d, ok := densities[t.Density]
	if !ok {
		d = densities["comfortable"]
	}

	common := map[string]string{
		"--accent": accent, "--accent-fg": accentFg, "--accent-weak": p.AccentWeak,
		"--ring":      accent,
		"--control-h": d.ControlH, "--space": d.Space, "--radius": d.Radius,
	}

	var b strings.Builder
	b.WriteString(":root{")
	writeVars(&b, neutralsLight)
	writeVars(&b, fixedColors)
	writeVars(&b, common)
	b.WriteString("}\n.dark{")
	writeVars(&b, neutralsDark)
	writeVars(&b, fixedColors)
	writeVars(&b, common)
	b.WriteString("}")
	return b.String()
}

func writeVars(b *strings.Builder, m map[string]string) {
	for k, v := range m {
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(v)
		b.WriteByte(';')
	}
}

// contrastFg returns the foreground (#ffffff or #0f172a) that reads best on hex,
// using the WCAG relative-luminance threshold. Invalid input -> white.
func contrastFg(hex string) string {
	r, g, bl, ok := parseHex(hex)
	if !ok {
		return "#ffffff"
	}
	// Relative luminance (sRGB, simple approximation).
	lum := (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(bl)) / 255.0
	if lum > 0.55 {
		return "#0f172a"
	}
	return "#ffffff"
}

func isHex(s string) bool {
	_, _, _, ok := parseHex(s)
	return ok
}

func parseHex(s string) (r, g, b int, ok bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseInt(s, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff), true
}

// Process-wide cache of the active theme's rendered CSS. Read on every page
// render (cheap), written at startup and when the active theme changes.
var currentCSS atomic.Value // string

func SetCurrentCSS(css string) { currentCSS.Store(css) }

func CurrentCSS() string {
	if v, ok := currentCSS.Load().(string); ok {
		return v
	}
	return CSSVars(Theme{Palette: "classic", Mode: "auto", Density: "comfortable"})
}
