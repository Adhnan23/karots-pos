package plugin

import (
	"context"
	"encoding/json"

	"github.com/a-h/templ"
)

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

// ReportCard adds a card to the core Reports hub (/admin/reports). Registered by
// a plugin so its report shows alongside the built-in ones when it's enabled.
type ReportCard struct {
	Href  string
	Label string
	Desc  string
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

// CashierMenuRoot adds a card at the ROOT of the cashier menu (alongside the
// product-group cards). Tapping it navigates into ChildrenURL, a GET that returns
// the menu-node JSON protocol ({"nodes":[…]}). This replaces the old quick-action
// strip: a plugin's actions live in the same drill-down menu as products.
type CashierMenuRoot struct {
	Key         string
	Emoji       string
	Label       string
	ChildrenURL string
}

// DrawerSection contributes an extra panel to the till OPEN and CLOSE dialogs
// (e.g. a plugin's per-session sub-ledger the cashier counts alongside the
// drawer). Core renders an empty slot; client-side it loads each section's form
// fragment and posts it to the section's save URL around the drawer call. The
// fragment is plain inputs (no hx-*); core never references the plugin's domain.
type DrawerSection struct {
	Key          string // stable id, e.g. "recharge"
	OpenFormURL  string // GET → HTML input rows for the Open-till dialog
	CloseFormURL string // GET → HTML input rows for the Close-register dialog
	SaveOpenURL  string // POST (form-encoded) target, after the till opens
	SaveCloseURL string // POST (form-encoded) target, before the till closes
}

// LogoutGuard reports whether a user has unfinished plugin work that must be
// resolved before they may log out — e.g. an open recharge float session. When
// Block is true the web layer refuses to log the user out and instead sends them
// to Redirect (a page where they can resolve it) with Reason shown as a banner.
// Guards must be cheap (one indexed query) and fail open (return Block=false on
// error) so a plugin issue can never trap a user in a non-logout-able state.
type LogoutGuard func(ctx context.Context, userID int64) (block bool, redirect, reason string)

var (
	adminNav         []AdminNavEntry
	cashierTabs      []CashierTab
	settingsSecs     []SettingsSection
	dashboardCards   []DashboardCard
	paletteEntries   []PaletteEntry
	reportCards      []ReportCard
	posActions       []PosAction
	cashierMenuRoots []CashierMenuRoot
	drawerSections   []DrawerSection
	tenderMethods    []TenderMethod
	logoutGuards     []LogoutGuard
	receiptTabs      []ReceiptTab
)

// ReceiptTab adds a tab to the unified Receipts page on BOTH the admin and cashier
// shells. The core page renders a tab button (Label) plus a panel that lazy-loads
// the plugin's own fragment endpoint for the current role: CashierHref on the
// cashier shell, AdminHref on the admin shell. Key must be unique across plugins.
// A plugin may register several (e.g. one per receipt kind).
type ReceiptTab struct {
	Key         string
	Label       string
	CashierHref string
	AdminHref   string
}

// Hook registration — plugins call these from Setup.
func (r *Registry) AddAdminNav(e AdminNavEntry)          { adminNav = append(adminNav, e) }
func (r *Registry) AddCashierTab(t CashierTab)           { cashierTabs = append(cashierTabs, t) }
func (r *Registry) AddSettingsSection(s SettingsSection) { settingsSecs = append(settingsSecs, s) }
func (r *Registry) AddDashboardCard(c DashboardCard)     { dashboardCards = append(dashboardCards, c) }
func (r *Registry) AddPaletteEntry(p PaletteEntry)       { paletteEntries = append(paletteEntries, p) }
func (r *Registry) AddReportCard(rc ReportCard)          { reportCards = append(reportCards, rc) }
func (r *Registry) AddPosAction(a PosAction)             { posActions = append(posActions, a) }
func (r *Registry) AddCashierMenuRoot(m CashierMenuRoot) {
	cashierMenuRoots = append(cashierMenuRoots, m)
}
func (r *Registry) AddDrawerSection(s DrawerSection) { drawerSections = append(drawerSections, s) }
func (r *Registry) AddTenderMethod(t TenderMethod) { tenderMethods = append(tenderMethods, t) }
func (r *Registry) AddLogoutGuard(g LogoutGuard)   { logoutGuards = append(logoutGuards, g) }
func (r *Registry) AddReceiptTab(t ReceiptTab)     { receiptTabs = append(receiptTabs, t) }

// Getters for the template layer.
func AdminNav() []AdminNavEntry           { return adminNav }
func CashierTabs() []CashierTab           { return cashierTabs }
func SettingsSections() []SettingsSection { return settingsSecs }
func DashboardCards() []DashboardCard     { return dashboardCards }
func PaletteEntries() []PaletteEntry      { return paletteEntries }
func ReportCards() []ReportCard           { return reportCards }
func PosActions() []PosAction             { return posActions }
func CashierMenuRoots() []CashierMenuRoot { return cashierMenuRoots }
func DrawerSections() []DrawerSection      { return drawerSections }

// CashierMenuRootsJSON renders the menu roots as a JSON array for the cashier
// Alpine scope: [{"emoji":"📶","label":"Reload & Bills","url":"/cashier/recharge/menu"}].
// Field names are explicit json tags (lower-case) since the cashier JS reads
// r.emoji / r.label / r.url.
func CashierMenuRootsJSON() string {
	type r struct {
		Emoji string `json:"emoji"`
		Label string `json:"label"`
		URL   string `json:"url"`
	}
	out := make([]r, 0, len(cashierMenuRoots))
	for _, m := range cashierMenuRoots {
		out = append(out, r{m.Emoji, m.Label, m.ChildrenURL})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// DrawerSectionsJSON renders the drawer sections for the cashier Alpine scope:
// [{"key":"recharge","openFormUrl":"…","closeFormUrl":"…","saveOpenUrl":"…","saveCloseUrl":"…"}].
func DrawerSectionsJSON() string {
	type s struct {
		Key          string `json:"key"`
		OpenFormURL  string `json:"openFormUrl"`
		CloseFormURL string `json:"closeFormUrl"`
		SaveOpenURL  string `json:"saveOpenUrl"`
		SaveCloseURL string `json:"saveCloseUrl"`
	}
	out := make([]s, 0, len(drawerSections))
	for _, d := range drawerSections {
		out = append(out, s{d.Key, d.OpenFormURL, d.CloseFormURL, d.SaveOpenURL, d.SaveCloseURL})
	}
	b, _ := json.Marshal(out)
	return string(b)
}
func TenderMethods() []TenderMethod { return tenderMethods }
func LogoutGuards() []LogoutGuard   { return logoutGuards }
func ReceiptTabs() []ReceiptTab     { return receiptTabs }
