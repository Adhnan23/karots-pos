package plugin

import "github.com/a-h/templ"

// Additive UI hooks. A plugin's Setup registers these; the template layer reads
// them through the exported getters to weave plugin UI into the core shell. They
// are additive only — overriding a whole page is done via the route Mux instead.

// AdminNavEntry adds an admin destination. If Section names an existing section
// key (e.g. "setup"), the link is nested under it; if Section is empty, a new
// top-level section is created from SectionLabel + Icon holding this one link.
type AdminNavEntry struct {
	Section      string // existing section key to nest under; "" = new top-level section
	SectionLabel string // label for the new top-level section (when Section == "")
	Icon         string // emoji/icon for the new top-level section
	Href         string
	Label        string
	Key          string
	Desc         string
}

// CashierTab adds a link to the cashier shell.
type CashierTab struct {
	Href  string
	Label string
	Key   string
}

// SettingsSection adds a panel to the admin Settings page.
type SettingsSection struct {
	Title string
	Body  templ.Component
}

// DashboardCard adds a card to the admin dashboard.
type DashboardCard struct {
	Component templ.Component
}

// PaletteEntry adds a command-palette destination.
type PaletteEntry struct {
	Href  string
	Label string
	Group string
}

// TenderMethod adds a payment method to the cashier POS split-tender selector.
// Value must be a payment_method enum value the plugin's migration added (e.g.
// "wallet"); Label is shown in the dropdown. The carrier picker and the
// post-checkout attribution for the wallet tender are handled in the cashier JS.
type TenderMethod struct {
	Value string
	Label string
}

// PosAction adds a control to the cashier POS screen's action bar (rendered
// inside the pos() Alpine scope). A plugin action typically opens its own popup
// and adds a service line to the cart by dispatching the window event
// "pos-add-service" with detail {id, name, price}, which the POS listens for.
type PosAction struct {
	Component templ.Component
}

// QuickActionTab adds a tab to the cashier POS quick-action strip rendered below
// the product grid. Each plugin contributes one tab (Key unique, Label shown on
// the tab button); its Component is the tab panel — typically a grid of cards that
// dispatch the "pos-add-service" window event to add a line to the cart. The strip
// is hidden entirely when no plugin registers a tab.
type QuickActionTab struct {
	Key       string
	Label     string
	Component templ.Component
}

var (
	adminNav        []AdminNavEntry
	cashierTabs     []CashierTab
	settingsSecs    []SettingsSection
	dashboardCards  []DashboardCard
	paletteEntries  []PaletteEntry
	posActions      []PosAction
	quickActionTabs []QuickActionTab
	tenderMethods   []TenderMethod
)

// Hook registration — plugins call these from Setup.
func (r *Registry) AddAdminNav(e AdminNavEntry)          { adminNav = append(adminNav, e) }
func (r *Registry) AddCashierTab(t CashierTab)           { cashierTabs = append(cashierTabs, t) }
func (r *Registry) AddSettingsSection(s SettingsSection) { settingsSecs = append(settingsSecs, s) }
func (r *Registry) AddDashboardCard(c DashboardCard)     { dashboardCards = append(dashboardCards, c) }
func (r *Registry) AddPaletteEntry(p PaletteEntry)       { paletteEntries = append(paletteEntries, p) }
func (r *Registry) AddPosAction(a PosAction)             { posActions = append(posActions, a) }
func (r *Registry) AddQuickActionTab(t QuickActionTab)   { quickActionTabs = append(quickActionTabs, t) }
func (r *Registry) AddTenderMethod(t TenderMethod)       { tenderMethods = append(tenderMethods, t) }

// Getters for the template layer.
func AdminNav() []AdminNavEntry           { return adminNav }
func CashierTabs() []CashierTab           { return cashierTabs }
func SettingsSections() []SettingsSection { return settingsSecs }
func DashboardCards() []DashboardCard     { return dashboardCards }
func PaletteEntries() []PaletteEntry      { return paletteEntries }
func PosActions() []PosAction             { return posActions }
func QuickActionTabs() []QuickActionTab   { return quickActionTabs }
func TenderMethods() []TenderMethod       { return tenderMethods }
