package ui

import "testing"

func TestBadgeClass(t *testing.T) {
	cases := map[string]string{
		"ok":       "bg-ok",
		"bad":      "bg-bad",
		"accent":   "bg-accent",
		"nonsense": "bg-surface-2", // neutral fallback
	}
	for tone, want := range cases {
		if got := BadgeClass(tone); !contains(got, want) {
			t.Errorf("BadgeClass(%q)=%q missing %q", tone, got, want)
		}
	}
}

func TestActionTileRenders(t *testing.T) {
	html := renderTo(t, ActionTile(ActionTileProps{
		Title: "New Sale", Desc: "Start a transaction", Href: "/cashier", Icon: "receipt", Area: "sell",
	}))
	for _, want := range []string{"New Sale", "Start a transaction", `href="/cashier"`, "<svg", "area-sell"} {
		if !contains(html, want) {
			t.Errorf("ActionTile missing %q in:\n%s", want, html)
		}
	}
}

func TestStatTileRenders(t *testing.T) {
	html := renderTo(t, StatTile(StatTileProps{Label: "Today sales", Value: "Rs 12,500", Icon: "chart", Area: "reports"}))
	for _, want := range []string{"Today sales", "Rs 12,500", "<svg"} {
		if !contains(html, want) {
			t.Errorf("StatTile missing %q in:\n%s", want, html)
		}
	}
}
