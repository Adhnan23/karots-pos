// Package clearance flags slow-moving stock (has stock but no sale in N days),
// suggests a margin-safe markdown the owner approves or adjusts, and offers to
// apply that discount at the till. Core never imports it; it attaches through
// generic plugin hooks. Depends on no other plugin.
package clearance

import (
	"context"
	"io/fs"

	"karots-pos/internal/plugin"
	"karots-pos/plugins/clearance/migrations"
)

func init() { plugin.Register(&Plugin{}) }

type Plugin struct {
	core  plugin.Core
	store *Store
}

func (p *Plugin) Name() string                { return "Clearance" }
func (p *Plugin) Migrations() (fs.FS, string) { return migrations.FS, "clearance" }

func (p *Plugin) Setup(reg *plugin.Registry) {
	p.core = reg.Core
	p.store = NewStore(reg.Core.DB)

	a := &adminUI{p: p}
	reg.Admin().GET("/clearance", a.Page)
	reg.Admin().POST("/clearance/settings", a.SaveSettings)
	reg.Admin().POST("/clearance/:pid/approve", a.Approve)
	reg.Admin().POST("/clearance/:pid/dismiss", a.Dismiss)

	reg.AddAdminNav(plugin.AdminNavEntry{
		SectionLabel: "Clearance", Icon: "🏷️",
		Href: "/admin/clearance", Label: "Stale stock", Key: "clearance",
		Desc: "Markdowns for slow-moving stock",
	})

	// Till-card pin + the add-to-cart markdown suggestion + info-popup row.
	reg.AddProductBadgeProvider(plugin.ProductBadgeProvider{Batch: p.store.BadgesFor})
	reg.AddProductSaleSuggestionProvider(plugin.ProductSaleSuggestionProvider{Batch: p.store.SuggestionsFor})
	reg.AddProductDetailContributor(plugin.ProductDetailContributor{
		Rows: func(ctx context.Context, id int64) ([]plugin.DetailRow, error) {
			m, err := p.store.SuggestionsFor(ctx, []int64{id})
			if err != nil || len(m) == 0 {
				return nil, err
			}
			return []plugin.DetailRow{{Label: "Clearance", Value: m[id].Label}}, nil
		},
	})
}
