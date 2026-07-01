package ui

import "testing"

func TestPageHeaderRenders(t *testing.T) {
	html := renderTo(t, PageHeader(PageHeaderProps{
		Title:    "Products",
		Subtitle: "Manage your catalog",
		Crumbs:   []Crumb{{"Home", "/admin"}, {"Products", ""}},
	}))
	for _, want := range []string{"Products", "Manage your catalog", "Home", `href="/admin"`} {
		if !contains(html, want) {
			t.Errorf("PageHeader missing %q in:\n%s", want, html)
		}
	}
}

func TestEmptyStateRenders(t *testing.T) {
	html := renderTo(t, EmptyState("box", "No products", "Add your first product to begin"))
	for _, want := range []string{"<svg", "No products", "Add your first product"} {
		if !contains(html, want) {
			t.Errorf("EmptyState missing %q in:\n%s", want, html)
		}
	}
}
