package ui

import (
	"testing"

	"github.com/a-h/templ"
)

func TestInputRenders(t *testing.T) {
	html := renderTo(t, Input(templ.Attributes{"name": "qty", "inputmode": "numeric"}))
	for _, want := range []string{"<input", `name="qty"`, `inputmode="numeric"`, "min-h-control"} {
		if !contains(html, want) {
			t.Errorf("Input missing %q in:\n%s", want, html)
		}
	}
}

func TestSelectMarksSelected(t *testing.T) {
	opts := []Option{{"a", "Apple"}, {"b", "Banana"}}
	html := renderTo(t, Select(opts, "b", templ.Attributes{"name": "fruit"}))
	if !contains(html, `selected`) {
		t.Errorf("Select should mark selected option, got:\n%s", html)
	}
	if !contains(html, "Banana") || !contains(html, `name="fruit"`) {
		t.Errorf("Select missing options/name in:\n%s", html)
	}
}

func TestFieldShowsError(t *testing.T) {
	html := renderTo(t, Field(FieldProps{Label: "Name", Error: "required", Required: true}))
	for _, want := range []string{"Name", "required", "text-bad"} {
		if !contains(html, want) {
			t.Errorf("Field missing %q in:\n%s", want, html)
		}
	}
}

func TestToggleRenders(t *testing.T) {
	html := renderTo(t, Toggle("Enabled", templ.Attributes{"name": "on", "checked": true}))
	for _, want := range []string{"Enabled", "peer", `type="checkbox"`, "peer-checked:bg-accent"} {
		if !contains(html, want) {
			t.Errorf("Toggle missing %q in:\n%s", want, html)
		}
	}
}
