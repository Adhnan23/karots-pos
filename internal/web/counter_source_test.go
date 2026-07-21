package web

import (
	"testing"

	adminfragments "karots-pos/templates/fragments/admin"
)

// TestAllowedSource pins the server-side check on where counter cash may come
// from.
//
// Filtering the picker was not enough on its own. During development a cashier
// posted a hand-crafted "locker:N" for a safe marked off-limits and the server
// happily took 500 out of it and returned 200. The menu is a convenience; this
// is the rule.
func TestAllowedSource(t *testing.T) {
	offered := []adminfragments.LocationChoice{
		{Value: "till:2", Label: "My drawer — Amal"},
		{Value: "locker:1", Label: "Shop float"},
	}
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"own drawer", "till:2", true},
		{"an offered locker", "locker:1", true},
		{"a locker the owner keeps to themselves", "locker:7", false},
		{"another cashier's drawer", "till:3", false},
		{"nothing at all", "", false},
		{"whitespace", "   ", false},
		{"external", "external", false},
		{"a near miss", "locker:11", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := allowedSource(offered, tc.value); got != tc.want {
				t.Fatalf("allowedSource(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}
