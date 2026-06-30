// Package icons holds the embedded inline-SVG icon set. Each icon is the INNER
// markup of a 24x24 stroke icon; the Icon component wraps it in an <svg> with
// currentColor so icons inherit the surrounding text color (and thus each
// area's accent). Shipped inside the binary — no runtime fetch.
package icons

import "sort"

// paths maps an icon name to its inner SVG markup (paths/shapes only).
// Geometry is original simple line art per the project icon rules.
var paths = map[string]string{
	"plus":          `<line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/>`,
	"x":             `<line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/>`,
	"check":         `<polyline points="20 6 9 17 4 12"/>`,
	"search":        `<circle cx="11" cy="11" r="7"/><line x1="21" y1="21" x2="16.65" y2="16.65"/>`,
	"chevron-right": `<polyline points="9 18 15 12 9 6"/>`,
	"chevron-down":  `<polyline points="6 9 12 15 18 9"/>`,
	"menu":          `<line x1="3" y1="6" x2="21" y2="6"/><line x1="3" y1="12" x2="21" y2="12"/><line x1="3" y1="18" x2="21" y2="18"/>`,
	"home":          `<path d="M3 11.5 12 4l9 7.5"/><path d="M5 10v9a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-9"/><path d="M9 20v-6h6v6"/>`,
	"box":           `<rect x="4" y="8" width="16" height="12" rx="1"/><path d="M4 8 7 4h10l3 4"/><line x1="12" y1="8" x2="12" y2="20"/>`,
	"boxes":         `<rect x="8" y="3" width="8" height="8" rx="1"/><rect x="3" y="13" width="8" height="8" rx="1"/><rect x="13" y="13" width="8" height="8" rx="1"/>`,
	"receipt":       `<path d="M5 3v18l2-1 2 1 2-1 2 1 2-1 2 1V3l-2 1-2-1-2 1-2-1-2 1Z"/><line x1="8" y1="8" x2="16" y2="8"/><line x1="8" y1="12" x2="16" y2="12"/>`,
	"cart":          `<circle cx="9" cy="20" r="1.5"/><circle cx="18" cy="20" r="1.5"/><path d="M2 3h2l2.5 13h11l2-9H6"/>`,
	"wallet":        `<rect x="3" y="6" width="18" height="13" rx="2"/><path d="M16 12h5v3h-5a1.5 1.5 0 0 1 0-3Z"/>`,
	"chart":         `<line x1="4" y1="20" x2="20" y2="20"/><rect x="6" y="12" width="3" height="6"/><rect x="11" y="8" width="3" height="10"/><rect x="16" y="14" width="3" height="4"/>`,
	"settings":      `<line x1="4" y1="8" x2="20" y2="8"/><line x1="4" y1="16" x2="20" y2="16"/><circle cx="9" cy="8" r="2"/><circle cx="15" cy="16" r="2"/>`,
	"users":         `<circle cx="9" cy="8" r="3"/><path d="M3 20c0-3.5 2.7-6 6-6s6 2.5 6 6"/><path d="M16 5.5a3 3 0 0 1 0 5.5"/><path d="M21 20c0-2.5-1.3-4.5-3.5-5.3"/>`,
	"truck":         `<rect x="2" y="7" width="12" height="9" rx="1"/><path d="M14 10h4l3 3v3h-7Z"/><circle cx="7" cy="18" r="1.5"/><circle cx="17" cy="18" r="1.5"/>`,
	"edit":          `<path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z"/>`,
	"trash":         `<polyline points="3 6 21 6"/><path d="M19 6v13a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/><path d="M8 6V4a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v2"/><line x1="10" y1="11" x2="10" y2="17"/><line x1="14" y1="11" x2="14" y2="17"/>`,
	"filter":        `<polygon points="3 4 21 4 14 13 14 19 10 21 10 13"/>`,
	"download":      `<path d="M12 3v12"/><polyline points="7 11 12 16 17 11"/><path d="M5 21h14"/>`,
	"print":         `<path d="M6 9V3h12v6"/><rect x="4" y="9" width="16" height="8" rx="1"/><path d="M8 17h8v4H8Z"/><circle cx="17" cy="12" r="0.8"/>`,
	"refresh":       `<path d="M21 12a9 9 0 1 1-3-6.7"/><polyline points="21 3 21 8 16 8"/>`,
	"logout":        `<path d="M15 4h3a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-3"/><polyline points="10 17 15 12 10 7"/><line x1="15" y1="12" x2="3" y2="12"/>`,
	"palette":       `<path d="M12 3a9 9 0 1 0 0 18 2 2 0 0 0 2-2 2 2 0 0 1 2-2h1a4 4 0 0 0 4-4 9 9 0 0 0-9-8Z"/><circle cx="7.5" cy="11" r="1"/><circle cx="11" cy="7.5" r="1"/><circle cx="16" cy="9" r="1"/>`,
}

// Has reports whether an icon name is registered.
func Has(name string) bool { _, ok := paths[name]; return ok }

// Names returns all registered icon names, sorted.
func Names() []string {
	out := make([]string, 0, len(paths))
	for k := range paths {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// inner returns the raw inner SVG for a name, or "" if unknown.
func inner(name string) string { return paths[name] }
