package ui

// ControlClass is the shared class for text inputs, selects, and textareas.
// Token-driven so it recolors with the active theme; min-h-control keeps a
// 44px+ touch target.
const ControlClass = "w-full rounded-token border border-line bg-surface text-body placeholder:text-muted px-3 py-2 min-h-control focus-visible:outline-none"

// Option is a single <select> option.
type Option struct{ Value, Label string }

// FieldProps configures the Field label/hint/error wrapper.
type FieldProps struct {
	Label    string
	Hint     string
	Error    string
	Required bool
}
