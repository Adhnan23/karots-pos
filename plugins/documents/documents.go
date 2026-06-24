package documents

import (
	"io/fs"

	"karots-pos/internal/plugin"
	"karots-pos/plugins/documents/migrations"
)

func init() { plugin.Register(&Plugin{}) }

// Plugin implements plugin.Plugin for the communication-store counter.
type Plugin struct {
	core  plugin.Core
	store *Store
}

func (p *Plugin) Name() string { return "Documents" }

// Migrations runs under goose_db_version_documents, independent of core.
func (p *Plugin) Migrations() (fs.FS, string) { return migrations.FS, "documents" }

// Setup wires the plugin's services, routes and UI hooks onto the registry.
func (p *Plugin) Setup(reg *plugin.Registry) {
	p.core = reg.Core
	p.store = NewStore(reg.Core.DB)

	a := &adminUI{p: p}
	reg.Admin().GET("/documents", a.Hub)
	reg.Admin().POST("/documents/services", a.ServiceCreate)
	reg.Admin().POST("/documents/services/:id/delete", a.ServiceDelete)
	reg.Admin().POST("/documents/services/:id/price", a.PriceAdd)
	reg.Admin().POST("/documents/price/:id/delete", a.PriceDelete)
	reg.Admin().POST("/documents/services/:id/consumable", a.ConsumableAdd)
	reg.Admin().POST("/documents/consumable/:id/delete", a.ConsumableDelete)
	reg.Admin().GET("/documents/report", a.Report)
	reg.Admin().GET("/documents/labour", a.Labour)
	reg.Admin().POST("/documents/labour/:job/pay", a.PayJob)
	reg.Admin().POST("/documents/labour/:job/dismiss", a.DismissJob)

	ch := &cashierUI{p: p}
	reg.Cashier().GET("/documents/services", ch.Services)
	reg.Cashier().GET("/documents/prices", ch.PriceRows)
	reg.Cashier().GET("/documents/quote", ch.Quote)
	reg.Cashier().POST("/documents/record", ch.Record)

	reg.AddQuickActionTab(plugin.QuickActionTab{Key: "photocopy", Label: "🖨 Photocopy", Component: JobPanel()})
	reg.AddReportCard(plugin.ReportCard{
		Href:  "/admin/documents/report",
		Label: "🖨 Documents",
		Desc:  "Print/copy revenue, paper used & labour",
	})
	reg.AddAdminNav(plugin.AdminNavEntry{
		SectionLabel: "Communication Store", Icon: "🖨",
		Href: "/admin/documents", Label: "Communication Store", Key: "documents-hub",
		Desc: "Services, pricing & consumables",
	})
	reg.AddAdminNav(plugin.AdminNavEntry{
		SectionLabel: "Communication Store", Icon: "🖨",
		Href: "/admin/documents/report", Label: "Report", Key: "documents-report",
		Desc: "Revenue, paper used & profit",
	})
	reg.AddAdminNav(plugin.AdminNavEntry{
		SectionLabel: "Communication Store", Icon: "🖨",
		Href: "/admin/documents/labour", Label: "Labour payments", Key: "documents-labour",
		Desc: "Pay labour on custom jobs",
	})
}
