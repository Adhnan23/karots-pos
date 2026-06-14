package response

import (
	"encoding/json"
	"strings"
)

// Trigger builds an HX-Trigger header value from a set of client events.
// Example: Trigger(map[string]any{"closeModal": true, "showToast": ...}).
func Trigger(events map[string]any) string {
	b, _ := json.Marshal(events)
	return string(b)
}

// Toast builds an HX-Trigger value that fires a single "show-toast" event.
// The event name is hyphenated so it survives HTML attribute case-folding when
// Alpine listens for it (x-on:show-toast.window).
func Toast(message, level string) string {
	return Trigger(map[string]any{
		"show-toast": map[string]string{"message": message, "level": level},
	})
}

// ToastAnd builds an HX-Trigger that shows a toast and fires extra events,
// e.g. ToastAnd("Saved", "success", "reload-products", "close-modal").
//
// The events are emitted in the order given (not as a Go map, whose JSON keys
// json.Marshal would sort alphabetically). htmx dispatches HX-Trigger events on
// the triggering element in JSON order; since the triggering element is often a
// form inside the modal that "close-modal" empties, callers pass "close-modal"
// last so the list-refresh ("reload-*") and toast events fire while the form is
// still attached and can bubble to their from:body / .window listeners.
func ToastAnd(message, level string, events ...string) string {
	toast, _ := json.Marshal(map[string]string{"message": message, "level": level})
	var b strings.Builder
	b.WriteString(`{"show-toast":`)
	b.Write(toast)
	for _, e := range events {
		key, _ := json.Marshal(e) // safely quote/escape the event name
		b.WriteString(",")
		b.Write(key)
		b.WriteString(":true")
	}
	b.WriteByte('}')
	return b.String()
}
