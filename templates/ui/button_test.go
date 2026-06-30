package ui

import (
    "testing"

    "github.com/a-h/templ"
)

func TestButtonRenders(t *testing.T) {
    html := renderTo(t, Button(ButtonProps{
        Variant: "primary",
        Attrs:   templ.Attributes{"hx-get": "/x", "disabled": true},
    }))
    for _, want := range []string{"<button", `type="button"`, "bg-accent", `hx-get="/x"`, "disabled"} {
        if !contains(html, want) {
            t.Errorf("Button render missing %q in:\n%s", want, html)
        }
    }
}

func TestButtonSubmitType(t *testing.T) {
    html := renderTo(t, Button(ButtonProps{Type: "submit"}))
    if !contains(html, `type="submit"`) {
        t.Errorf("expected submit type, got:\n%s", html)
    }
}
