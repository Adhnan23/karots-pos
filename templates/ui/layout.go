package ui

// Crumb is one breadcrumb entry. An empty Href renders as the current
// (non-link) page.
type Crumb struct{ Label, Href string }

// PageHeaderProps configures the page header (breadcrumb + title + actions).
type PageHeaderProps struct {
	Title    string
	Subtitle string
	Crumbs   []Crumb
}
