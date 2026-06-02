package response

import "encoding/json"

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
// e.g. ToastAnd("Saved", "success", "close-modal").
func ToastAnd(message, level string, events ...string) string {
	m := map[string]any{
		"show-toast": map[string]string{"message": message, "level": level},
	}
	for _, e := range events {
		m[e] = true
	}
	return Trigger(m)
}
