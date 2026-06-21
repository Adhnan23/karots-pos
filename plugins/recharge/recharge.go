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

func (p *Plugin) Name() string { return "Reload & Bills" }

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
	reg.Admin().POST("/recharge/devices", a.DeviceCreate)
	reg.Admin().POST("/recharge/devices/:id/delete", a.DeviceDelete)
	reg.Admin().GET("/recharge/report", a.Report)
	reg.Admin().GET("/recharge/ledger", a.Ledger)
	reg.Admin().GET("/recharge/refills", a.Refills)
	reg.Admin().GET("/recharge/tx/:id", a.TxView)
	reg.Admin().POST("/recharge/tx/:id/print", a.TxPrint)
	reg.Admin().GET("/recharge/devices/balances", a.Devices)
	reg.Admin().POST("/recharge/refill", a.Refill)

	ch := &cashierUI{p: p}
	reg.Cashier().GET("/recharge/carriers", ch.Carriers)
	reg.Cashier().GET("/recharge/devices", ch.Devices)
	reg.Cashier().GET("/recharge", ch.Recon)
	reg.Cashier().POST("/recharge/open", ch.SaveOpening)
	reg.Cashier().POST("/recharge/close", ch.SaveClosing)
	reg.Cashier().POST("/recharge/tx", ch.Tx)
	reg.Cashier().POST("/recharge/reload", ch.Reload)
	reg.Cashier().POST("/recharge/wallet", ch.Wallet)
	reg.AddQuickActionTab(plugin.QuickActionTab{Key: "reload", Label: "📶 Reload", Component: ReloadPanel()})
	reg.AddCashierTab(plugin.CashierTab{Href: "/cashier/recharge", Label: "Reload & Bills", Key: "recharge"})
	reg.AddTenderMethod(plugin.TenderMethod{Value: "wallet", Label: "Wallet (eZ Cash / mCash)"})

	reg.AddAdminNav(plugin.AdminNavEntry{
		SectionLabel: "Reload & Bills",
		Icon:         "📶",
		Href:         "/admin/recharge",
		Label:        "Carriers, devices & cards",
		Key:          "recharge-carriers",
		Desc:         "Carriers, devices, bank cards & reconciliation",
	})
	reg.AddAdminNav(plugin.AdminNavEntry{
		SectionLabel: "Reload & Bills",
		Icon:         "📶",
		Href:         "/admin/recharge/report",
		Label:        "Report",
		Key:          "recharge-report",
		Desc:         "Float balances & per-session reconciliation",
	})
	reg.AddAdminNav(plugin.AdminNavEntry{
		SectionLabel: "Reload & Bills",
		Icon:         "📶",
		Href:         "/admin/recharge/ledger",
		Label:        "Ledger",
		Key:          "recharge-ledger",
		Desc:         "Filterable money-movement log + CSV export",
	})
	reg.AddAdminNav(plugin.AdminNavEntry{
		SectionLabel: "Reload & Bills",
		Icon:         "📶",
		Href:         "/admin/recharge/refills",
		Label:        "Float refills",
		Key:          "recharge-refills",
		Desc:         "Refill device float from a supplier + refill history",
	})
}
