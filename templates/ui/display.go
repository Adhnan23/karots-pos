package ui

// BadgeClass returns the pill class for a tone. Unknown tone → neutral.
// Status tones (ok/warn/bad/info) are FIXED colors from Phase 0a, so white
// foreground on them is contrast-safe and intentional.
func BadgeClass(tone string) string {
	base := "inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-xs font-medium "
	switch tone {
	case "ok":
		return base + "bg-ok text-white"
	case "warn":
		return base + "bg-warn text-white"
	case "bad":
		return base + "bg-bad text-white"
	case "info":
		return base + "bg-info text-white"
	case "accent":
		return base + "bg-accent text-accent-fg"
	default:
		return base + "bg-surface-2 text-body"
	}
}

// areaText returns the area-accent text class, or "text-accent" when no area is
// given. Area keys are the fixed Phase 0a set (sell/inventory/purchasing/money/
// reports/setup).
func areaText(area string) string {
	if area == "" {
		return "text-accent"
	}
	return "text-area-" + area
}

// StatTileProps configures a value-forward metric tile.
type StatTileProps struct {
	Label string
	Value string
	Sub   string
	Icon  string
	Area  string
}

// ActionTileProps configures a big icon nav tile.
type ActionTileProps struct {
	Title string
	Desc  string
	Href  string
	Icon  string
	Area  string
}
