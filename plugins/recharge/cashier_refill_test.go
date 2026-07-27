package recharge

import (
	"testing"

	adminfragments "karots-pos/templates/fragments/admin"
)

// TestRefillSourceAllowed pins the server-side guard on where a cashier may pay a
// supplier float refill from. Filtering the picker to the cashier's drawer + the
// cashier-access lockers is not enough on its own: a cashier could hand-craft a
// "locker:N" for a safe the owner keeps off-limits. This is the rule the POST
// enforces before any money moves (mirrors core web's allowedSource for the
// supplier counter — the same hole its audit once caught).
func TestRefillSourceAllowed(t *testing.T) {
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
			if got := refillSourceAllowed(offered, tc.value); got != tc.want {
				t.Fatalf("refillSourceAllowed(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}
