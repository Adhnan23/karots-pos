package ui

import "testing"

func TestTabsMarksActive(t *testing.T) {
	tabs := []Tab{{"Sales", "/admin/receipts?tab=sales", "sales"}, {"Cash", "/admin/receipts?tab=cash", "cash"}}
	html := renderTo(t, Tabs(tabs, "cash"))
	for _, want := range []string{"Sales", "Cash", `href="/admin/receipts?tab=sales"`, "border-accent"} {
		if !contains(html, want) {
			t.Errorf("Tabs missing %q in:\n%s", want, html)
		}
	}
}

func TestDateRangeBarRenders(t *testing.T) {
	html := renderTo(t, DateRangeBar("/admin/reports/sales", "today", "", ""))
	for _, want := range []string{"Today", "From", "To", `action="/admin/reports/sales"`, "preset=today"} {
		if !contains(html, want) {
			t.Errorf("DateRangeBar missing %q in:\n%s", want, html)
		}
	}
}
