// Package ui is the shared, token-driven Templ component library used across
// admin, cashier, and plugin pages. Class-selection logic lives here in plain
// Go so it can be unit-tested; the .templ files are thin wrappers over it.
package ui

// base classes shared by every button variant.
const btnBase = "inline-flex items-center justify-center gap-2 rounded-token font-medium min-h-control transition-colors focus-visible:outline-none disabled:opacity-50 disabled:pointer-events-none"

// ButtonClass returns the full class string for a button variant+size.
// Unknown variant falls back to primary; unknown size to md.
func ButtonClass(variant, size string) string {
    v := map[string]string{
        "primary":   "bg-accent text-accent-fg hover:opacity-90",
        "secondary": "bg-surface-2 text-body border border-line hover:bg-surface",
        "ghost":     "bg-transparent text-body hover:bg-surface-2",
        "danger":    "bg-bad text-white hover:opacity-90",
    }[variant]
    if v == "" {
        v = "bg-accent text-accent-fg hover:opacity-90"
    }
    s := map[string]string{
        "sm": "text-sm px-3 py-1.5",
        "md": "text-sm px-4 py-2",
        "lg": "text-base px-5 py-2.5",
    }[size]
    if s == "" {
        s = "text-sm px-4 py-2"
    }
    return btnBase + " " + v + " " + s
}

// buttonType defaults the button type attribute to "button" so a component
// dropped inside a form never submits by accident.
func buttonType(t string) string {
    if t == "submit" {
        return "submit"
    }
    return "button"
}
