package layouts

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"karots-pos/internal/middleware"
)

// skinAttr / densityAttr read the system-user-locked look from the request
// context (set by web.withAppearance) so base.templ can stamp it on <html>.
// Both default safely, so a context without appearance still renders.
func skinAttr(ctx context.Context) string    { s, _ := middleware.AppearanceCtx(ctx); return s }
func densityAttr(ctx context.Context) string { _, d := middleware.AppearanceCtx(ctx); return d }

// customSkinStyle returns a <style> body defining the --brand-* ramp for the
// "custom" skin, derived from the single chosen brand hex. Empty unless the
// active skin is "custom" with a valid colour — so base.templ injects nothing
// for the built-in skins. The whole ramp comes from one colour: 600 is the
// picked hex, the others are lighter/darker steps, so a shop only picks one
// colour and the buttons/links/tints stay coherent in light and dark.
func customSkinStyle(ctx context.Context) string {
	skin, _ := middleware.AppearanceCtx(ctx)
	if skin != "custom" {
		return ""
	}
	r, g, b, ok := parseHex(middleware.CustomBrandCtx(ctx))
	if !ok {
		return ""
	}
	hex := func(r, g, b int) string { return fmt.Sprintf("#%02x%02x%02x", clamp(r), clamp(g), clamp(b)) }
	lighten := func(v, pct int) int { return v + (255-v)*pct/100 }
	darken := func(v, pct int) int { return v * (100 - pct) / 100 }
	ramp := func(pct int, up bool) string {
		if up {
			return hex(lighten(r, pct), lighten(g, pct), lighten(b, pct))
		}
		return hex(darken(r, pct), darken(g, pct), darken(b, pct))
	}
	radius, radiusLg := radiusFor(middleware.CustomRadiusCtx(ctx))
	return "<style>:root[data-skin=\"custom\"]{" +
		"--brand-50:" + ramp(92, true) + ";" +
		"--brand-100:" + ramp(82, true) + ";" +
		"--brand-500:" + ramp(12, true) + ";" +
		"--brand-600:" + hex(r, g, b) + ";" +
		"--brand-700:" + ramp(18, false) + ";" +
		fmt.Sprintf("--brand-rgb:%d,%d,%d;", r, g, b) +
		"--brand-text-dark:" + ramp(45, true) + ";" +
		"--radius:" + radius + ";--radius-lg:" + radiusLg + ";" +
		"}</style>"
}

// radiusFor maps the card-shape keyword to its (--radius, --radius-lg) values.
func radiusFor(kw string) (string, string) {
	switch kw {
	case "sharp":
		return "0.25rem", "0.375rem"
	case "round":
		return "0.875rem", "1.25rem"
	default: // rounded
		return "0.5rem", "0.75rem"
	}
}

func clamp(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// parseHex accepts #RGB or #RRGGBB (already validated upstream, but re-checked
// here so a bad value renders nothing rather than a broken colour).
func parseHex(s string) (r, g, bl int, ok bool) {
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
	return int(v>>16) & 0xff, int(v>>8) & 0xff, int(v) & 0xff, true
}
