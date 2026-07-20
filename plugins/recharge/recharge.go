// Package recharge is the mobile-recharge plugin: it sells airtime top-ups as
// non-stocked service lines through the core sale/receipt/cash path, and tracks
// per-carrier float reconciliation per cash-drawer session. It is the reference
// plugin that exercises every framework seam (is_service + price_override, the
// per-plugin migrator, route Mux, and the additive UI hooks).
package recharge

import (
	"io/fs"

	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/lockers"
	"karots-pos/internal/plugin"
	"karots-pos/plugins/recharge/migrations"
)

func init() { plugin.Register(&Plugin{}) }

// Plugin implements plugin.Plugin for mobile recharge.
type Plugin struct {
	core  plugin.Core
	store *Store
	// cashflow & lockers are core services the plugin builds over the shared
	// Core.DB pool (plugin → core only — core never learns about the plugin). A
	// "bank" is a core kind="bank" locker; bill-pay / get-money move money through
	// cashflow.Move so every leg gets a CR- receipt and shows in core Cash Flow.
	cashflow *cashflow.Service
	lockers  *lockers.Service
}

func (p *Plugin) Name() string { return "Reload & Bills" }

// Migrations runs under goose_db_version_recharge, independent of core.
func (p *Plugin) Migrations() (fs.FS, string) { return migrations.FS, "recharge" }

// Setup wires the plugin's services, routes and UI hooks onto the registry.
func (p *Plugin) Setup(reg *plugin.Registry) {
	p.core = reg.Core
	p.store = NewStore(reg.Core.DB)
	// Build our own cashflow/lockers services over the shared pool. cashflow reuses
	// the same sales service the core UI uses (Core.Sales) for its overdraw guard.
	p.cashflow = cashflow.NewService(reg.Core.DB, reg.Core.Sales)
	p.lockers = lockers.NewService(reg.Core.DB)

	a := &adminUI{p: p}
	reg.Admin().GET("/recharge", a.Hub)
	reg.Admin().GET("/recharge/carriers", a.Carriers)
	reg.Admin().GET("/recharge/table", a.CarriersTable)
	reg.Admin().GET("/recharge/device-form", a.DeviceForm)
	reg.Admin().POST("/recharge", a.CarrierCreate)
	reg.Admin().GET("/recharge/:id/edit", a.CarrierEditForm)
	reg.Admin().POST("/recharge/:id/rename", a.CarrierRename)
	reg.Admin().POST("/recharge/:id/toggle", a.CarrierToggle)
	reg.Admin().POST("/recharge/devices", a.DeviceCreate)
	reg.Admin().POST("/recharge/devices/:id/delete", a.DeviceDelete)
	reg.Admin().GET("/recharge/devices/:id/edit", a.DeviceEditForm)
	reg.Admin().POST("/recharge/devices/:id/update", a.DeviceUpdate)
	reg.Admin().POST("/recharge/devices/:id/toggle", a.DeviceToggle)
	reg.Admin().GET("/recharge/report", a.Report)
	reg.Admin().GET("/recharge/ledger", a.Ledger)
	reg.Admin().GET("/recharge/refills", a.Refills)
	reg.Admin().GET("/recharge/tx/:id", a.TxView)
	reg.Admin().POST("/recharge/tx/:id/print", a.TxPrint)
	reg.Admin().GET("/recharge/bill/:id", a.BillView)
	reg.Admin().POST("/recharge/bill/:id/print", a.BillPrint)
	reg.Admin().GET("/recharge/receipts/bill", a.ReceiptsBill)
	reg.Admin().GET("/recharge/receipts/recharge", a.ReceiptsFloat)
	reg.Admin().GET("/recharge/devices/balances", a.Devices)
	reg.Admin().POST("/recharge/refill", a.Refill)

	ch := &cashierUI{p: p}
	reg.Cashier().GET("/recharge/carriers", ch.Carriers)
	reg.Cashier().GET("/recharge/devices", ch.Devices)
	reg.Cashier().GET("/recharge/banks", ch.Banks)
	reg.Cashier().GET("/recharge", ch.Recon)
	reg.Cashier().POST("/recharge/open", ch.SaveOpening)
	reg.Cashier().POST("/recharge/close", ch.SaveClosing)
	reg.Cashier().POST("/recharge/tx", ch.Tx)
	reg.Cashier().GET("/recharge/tx/:id", ch.TxView)
	reg.Cashier().POST("/recharge/tx/:id/print", ch.TxPrint)
	reg.Cashier().POST("/recharge/bank-tx", ch.BankTx)
	reg.Cashier().GET("/recharge/bill/:id", ch.BillView)
	reg.Cashier().POST("/recharge/bill/:id/print", ch.BillPrint)
	reg.Cashier().GET("/recharge/receipts/bill", ch.ReceiptsBill)
	reg.Cashier().GET("/recharge/receipts/recharge", ch.ReceiptsFloat)
	reg.Cashier().POST("/recharge/reload", ch.Reload)
	reg.Cashier().POST("/recharge/wallet", ch.Wallet)
	reg.Cashier().GET("/recharge/menu", ch.MenuRoot)
	reg.Cashier().GET("/recharge/menu/reload/carriers", ch.MenuReloadCarriers)
	reg.Cashier().GET("/recharge/menu/reload/devices", ch.MenuReloadDevices)
	reg.Cashier().POST("/recharge/menu/reload", ch.MenuReloadAdd)
	reg.Cashier().GET("/recharge/menu/bill", ch.MenuBill)
	reg.Cashier().GET("/recharge/menu/float", ch.MenuFloat)
	// Opening/closing float inputs shown inside the core till Open/Close dialogs.
	reg.Cashier().GET("/recharge/drawer/open", ch.DrawerOpenFields)
	reg.Cashier().GET("/recharge/drawer/close", ch.DrawerCloseFields)

	// Two receipt tabs on the unified Receipts page (admin + cashier): bill payments
	// and float (reload) transactions, each reprintable.
	reg.AddReceiptTab(plugin.ReceiptTab{
		Key: "recharge-bill", Label: "Bills",
		CashierHref: "/cashier/recharge/receipts/bill", AdminHref: "/admin/recharge/receipts/bill",
	})
	reg.AddReceiptTab(plugin.ReceiptTab{
		Key: "recharge-float", Label: "Reload",
		CashierHref: "/cashier/recharge/receipts/recharge", AdminHref: "/admin/recharge/receipts/recharge",
	})

	reg.AddCashierTab(plugin.CashierTab{Href: "/cashier/recharge", Label: "Reload & Bills", Key: "recharge"})
	// A "Reload" card at the root of the cashier POS menu (alongside the product-
	// group cards) drills straight into carrier → device → amount. Bill payments
	// and float transactions stay on the dedicated Reload & Bills tab above, whose
	// forms are server-rendered and HTMX-wired on page load.
	reg.AddCashierMenuRoot(plugin.CashierMenuRoot{
		Key: "recharge", Emoji: "📲", Label: "Reload", ChildrenURL: "/cashier/recharge/menu",
	})
	// Float opening/closing rides the core till Open/Close dialogs via this section;
	// the save URLs are the existing recon endpoints (open = SaveOpening, close =
	// SaveClosing). No separate float open/close step or logout guard is needed —
	// closing the till counts and closes the float in the same dialog.
	reg.AddDrawerSection(plugin.DrawerSection{
		Key:          "recharge",
		OpenFormURL:  "/cashier/recharge/drawer/open",
		CloseFormURL: "/cashier/recharge/drawer/close",
		SaveOpenURL:  "/cashier/recharge/open",
		SaveCloseURL: "/cashier/recharge/close",
	})
	reg.AddTenderMethod(plugin.TenderMethod{Value: "wallet", Label: "Wallet (eZ Cash / mCash)"})

	// Surface the Reload & Bills report in the core Reports hub too.
	reg.AddReportCard(plugin.ReportCard{
		Href:  "/admin/recharge/report",
		Label: "📶 Reload & Bills",
		Desc:  "Float on hand, service charge earned & movement by type",
	})

	// The first entry defines the section's sidebar target — the hub page, which
	// lists the links below as cards (matching the core admin sections).
	reg.AddAdminNav(plugin.AdminNavEntry{
		SectionLabel: "Reload & Bills",
		Icon:         "📶",
		Href:         "/admin/recharge",
		Label:        "Reload & Bills",
		Key:          "recharge-hub",
		Desc:         "Reload, bills & float — overview",
	})
	reg.AddAdminNav(plugin.AdminNavEntry{
		SectionLabel: "Reload & Bills",
		Icon:         "📶",
		Href:         "/admin/recharge/carriers",
		Label:        "Carriers & devices",
		Key:          "recharge-carriers",
		Desc:         "Carriers & devices; manage banks under Money → Cash Lockers",
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
