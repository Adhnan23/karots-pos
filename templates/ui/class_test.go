package ui

import "testing"

func TestButtonClass(t *testing.T) {
    cases := []struct{ variant, size string; wantHas []string }{
        {"primary", "md", []string{"bg-accent", "text-accent-fg", "min-h-control"}},
        {"secondary", "md", []string{"bg-surface-2", "text-body", "border-line"}},
        {"ghost", "sm", []string{"text-body", "text-sm"}},
        {"danger", "lg", []string{"bg-bad", "text-white", "text-base"}},
        {"nonsense", "nonsense", []string{"bg-accent", "text-accent-fg"}}, // fallback primary/md
    }
    for _, c := range cases {
        got := ButtonClass(c.variant, c.size)
        for _, want := range c.wantHas {
            if !contains(got, want) {
                t.Errorf("ButtonClass(%q,%q)=%q, missing %q", c.variant, c.size, got, want)
            }
        }
    }
}

func contains(s, sub string) bool {
    return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
    for i := 0; i+len(sub) <= len(s); i++ {
        if s[i:i+len(sub)] == sub {
            return i
        }
    }
    return -1
}
