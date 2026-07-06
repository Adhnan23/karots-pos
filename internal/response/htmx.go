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

// PrintPrompt builds an HX-Trigger that shows a success toast, then fires a
// "money-print" event carrying the slip reprint url (and whether to reload after,
// for admin balance views), then the given trailing bare events (e.g. "close-modal"
// or a "reload-*" list refresh). "money-print" precedes the trailing events so it
// bubbles to the body bridge before the triggering modal/form is torn down. Used
// when "ask before printing" is on, so the client offers Print / Skip.
func PrintPrompt(message, url string, reload bool, trailing ...string) string {
	toast, _ := json.Marshal(map[string]string{"message": message, "level": "success"})
	mp, _ := json.Marshal(map[string]any{"url": url, "reload": reload})
	var b strings.Builder
	b.WriteString(`{"show-toast":`)
	b.Write(toast)
	b.WriteString(`,"money-print":`)
	b.Write(mp)
	for _, e := range trailing {
		key, _ := json.Marshal(e)
		b.WriteString(",")
		b.Write(key)
		b.WriteString(":true")
	}
	b.WriteByte('}')
	return b.String()
}
