package ui

import (
    "context"
    "strings"
    "testing"

    "github.com/a-h/templ"
)

// renderTo renders a templ component to a string for assertion in tests.
func renderTo(t *testing.T, c templ.Component) string {
    t.Helper()
    var b strings.Builder
    if err := c.Render(context.Background(), &b); err != nil {
        t.Fatalf("render: %v", err)
    }
    return b.String()
}
