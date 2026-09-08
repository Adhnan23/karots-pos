// Package repairs tracks device repair jobs from drop-off to pickup and settles
// the money as a normal core sale. Core never imports it; it attaches only
// through generic plugin hooks and is inert when not built.
package repairs

import (
	"context"
	"io/fs"

	"karots-pos/internal/features/products"
	"karots-pos/internal/plugin"
	"karots-pos/plugins/repairs/migrations"
)

func init() { plugin.Register(&Plugin{}) }

type Plugin struct {
	core         plugin.Core
	store        *Store
	labourProdID int64
	defWarranty  int
}

func (p *Plugin) Name() string                { return "Repairs" }
func (p *Plugin) Migrations() (fs.FS, string) { return migrations.FS, "repairs" }

func (p *Plugin) Setup(reg *plugin.Registry) {
	p.core = reg.Core
	p.store = NewStore(reg.Core.DB)

	ctx := context.Background()
	labourID, defWarranty, err := p.store.EnsureConfig(ctx, func() (int64, error) {
		return p.ensureLabourProduct(ctx)
	})
	if err == nil {
		p.labourProdID = labourID
		p.defWarranty = defWarranty
	}

	a := &adminUI{p: p}
	reg.Admin().GET("/repairs", a.List)
	reg.Admin().GET("/repairs/new", a.NewForm)
	reg.Admin().POST("/repairs", a.Create)
	reg.Admin().GET("/repairs/report", a.Report)
	reg.Admin().GET("/repairs/receipts", a.Receipts)
	reg.Admin().GET("/repairs/suggest", a.Suggest)
	reg.Admin().GET("/repairs/:id", a.Detail)
	reg.Admin().POST("/repairs/:id", a.Update)
	reg.Admin().POST("/repairs/:id/part", a.AddPart)
	reg.Admin().POST("/repairs/part/:pid/delete", a.RemovePart)
	reg.Admin().POST("/repairs/:id/charge", a.AddCharge)
	reg.Admin().POST("/repairs/charge/:cid/delete", a.RemoveCharge)
	reg.Admin().POST("/repairs/:id/deposit", a.TakeDeposit)
	reg.Admin().POST("/repairs/:id/status", a.SetStatus)
	reg.Admin().POST("/repairs/:id/collect", a.Collect)
	reg.Admin().POST("/repairs/:id/cancel", a.Cancel)
	reg.Admin().GET("/repairs/:id/receipt", a.RepairReceipt)

	ch := &cashierUI{p: p}
	reg.Cashier().GET("/repairs/menu", ch.MenuRoot)
	reg.Cashier().GET("/repairs/apply", ch.ApplyForm)
	reg.Cashier().POST("/repairs", ch.Create)
	reg.Cashier().POST("/repairs/record", ch.Record)
	reg.Cashier().GET("/repairs/receipts", ch.Receipts)
	reg.Cashier().GET("/repairs/:id", ch.Detail)
	reg.Cashier().POST("/repairs/:id/part", ch.AddPart)
	reg.Cashier().POST("/repairs/:id/charge", ch.AddCharge)
	reg.Cashier().POST("/repairs/:id/deposit", ch.TakeDeposit)
	reg.Cashier().GET("/repairs/:id/receipt", ch.RepairReceipt)

	reg.AddAdminNav(plugin.AdminNavEntry{
		SectionLabel: "Repairs", Icon: "🔧",
		Href: "/admin/repairs", Label: "Repairs", Key: "repairs",
		Desc: "Track repair jobs, parts, warranty",
	})
	reg.AddCashierMenuRoot(plugin.CashierMenuRoot{
		Key: "repairs", Emoji: "🔧", Label: "Repairs", ChildrenURL: "/cashier/repairs/menu",
	})
	reg.AddReceiptTab(plugin.ReceiptTab{
		Key: "repairs", Label: "Repairs",
		CashierHref: "/cashier/repairs/receipts", AdminHref: "/admin/repairs/receipts",
	})
	reg.AddReportCard(plugin.ReportCard{
		Href: "/admin/repairs/report", Label: "🔧 Repairs", Desc: "Repair jobs, revenue & warranty",
	})
	reg.AddActivityContributor(plugin.ActivityContributor{Source: "repairs", List: p.store.ActivityRows})
	reg.AddPaletteEntry(plugin.PaletteEntry{Href: "/admin/repairs", Label: "Repairs", Group: "Repairs"})
	// DashboardCard wired in Task 9 (needs the ReadyCard component).
}

// ensureLabourProduct returns the hidden is_service product used for labour and
// custom charge lines, creating it once if absent.
func (p *Plugin) ensureLabourProduct(ctx context.Context) (int64, error) {
	if existing, err := p.core.Products.FindByName(ctx, "Repair Service"); err != nil {
		return 0, err
	} else if existing != nil {
		return existing.ID, nil
	}
	catID, unitID, err := p.store.serviceDefaults(ctx)
	if err != nil {
		return 0, err
	}
	prod, err := p.core.Products.Create(ctx, products.CreateInput{
		Name: "Repair Service", CategoryID: catID, UnitID: unitID,
		CostPrice: "0", SellingPrice: "0", WholesalePrice: "0", TaxRate: "0", IsService: true,
	})
	if err != nil {
		return 0, err
	}
	return prod.ID, nil
}
