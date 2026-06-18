// Package recharge is the mobile-recharge plugin: it sells airtime top-ups as
// non-stocked service lines through the core sale/receipt/cash path, and tracks
// per-carrier float reconciliation per cash-drawer session. It is the reference
// plugin that exercises every framework seam (is_service + price_override, the
// per-plugin migrator, route Mux, and the additive UI hooks).
package recharge

import (
	"io/fs"

	"karots-pos/internal/plugin"
	"karots-pos/plugins/recharge/migrations"
)

func init() { plugin.Register(&Plugin{}) }

// Plugin implements plugin.Plugin for mobile recharge.
type Plugin struct {
	core  plugin.Core
	store *Store
}

func (p *Plugin) Name() string { return "Mobile Recharge" }

// Migrations runs under goose_db_version_recharge, independent of core.
func (p *Plugin) Migrations() (fs.FS, string) { return migrations.FS, "recharge" }

// Setup wires the plugin's services, routes and UI hooks onto the registry.
func (p *Plugin) Setup(reg *plugin.Registry) {
	p.core = reg.Core
	p.store = NewStore(reg.Core.DB)

	a := &adminUI{p: p}
	reg.Admin().GET("/recharge", a.Carriers)
	reg.Admin().GET("/recharge/table", a.CarriersTable)
	reg.Admin().POST("/recharge", a.CarrierCreate)
	reg.Admin().POST("/recharge/:id/delete", a.CarrierDelete)

	ch := &cashierUI{p: p}
	reg.Cashier().GET("/recharge/carriers", ch.Carriers)
	reg.AddPosAction(plugin.PosAction{Component: ReloadButton()})

	reg.AddAdminNav(plugin.AdminNavEntry{
		SectionLabel: "Recharge",
		Icon:         "📶",
		Href:         "/admin/recharge",
		Label:        "Carriers",
		Key:          "recharge-carriers",
		Desc:         "Mobile-recharge carriers & reconciliation",
	})
}
