package ui

import "testing"

func TestModalRenders(t *testing.T) {
	html := renderTo(t, Modal("confirm", "Are you sure?"))
	for _, want := range []string{"Are you sure?", "open-modal-confirm", "x-show", "<svg"} {
		if !contains(html, want) {
			t.Errorf("Modal missing %q in:\n%s", want, html)
		}
	}
}
