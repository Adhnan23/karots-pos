// Package alternatives groups interchangeable products (group → quality tiers →
// member products) to power group-aware counter search, alternative suggestions
// with a till-card tier pin, and a reorder report rolled up to the tier. Core never
// imports it; it attaches through generic plugin hooks + the products func-var seams.
// It depends on no other plugin — it works standalone or mixed with any others.
package alternatives

import (
	"context"
	"io/fs"

	"karots-pos/internal/plugin"
	"karots-pos/plugins/alternatives/migrations"
)

func init() { plugin.Register(&Plugin{}) }

type Plugin struct {
	core  plugin.Core
	store *Store
}

func (p *Plugin) Name() string                { return "Alternatives" }
func (p *Plugin) Migrations() (fs.FS, string) { return migrations.FS, "alternatives" }

func (p *Plugin) Setup(reg *plugin.Registry) {
	p.core = reg.Core
	p.store = NewStore(reg.Core.DB)

	a := &adminUI{p: p}
	reg.Admin().GET("/alternatives", a.Page)
	reg.Admin().GET("/alternatives/new", a.GroupForm)
	reg.Admin().POST("/alternatives", a.CreateGroup)
	reg.Admin().GET("/alternatives/reorder", a.Reorder)
	reg.Admin().GET("/alternatives/reorder/export", a.ReorderCSV)
	reg.Admin().GET("/alternatives/:id", a.GroupDetail)
	reg.Admin().GET("/alternatives/:id/tiers", a.TiersSection) // reload fragment
	reg.Admin().PUT("/alternatives/:id", a.UpdateGroup)
	reg.Admin().POST("/alternatives/:id/active", a.SetGroupActive)
	reg.Admin().POST("/alternatives/:id/tiers", a.CreateTier)
	reg.Admin().PUT("/alternatives/tiers/:tid", a.UpdateTier)
	reg.Admin().POST("/alternatives/tiers/:tid/active", a.SetTierActive)
	reg.Admin().GET("/alternatives/tiers/:tid/search", a.ProductSearch) // add-product results
	reg.Admin().POST("/alternatives/tiers/:tid/members", a.AddMember)
	reg.Admin().DELETE("/alternatives/members/:pid", a.RemoveMember)

	reg.AddAdminNav(plugin.AdminNavEntry{
		SectionLabel: "Alternatives", Icon: "🔀",
		Href: "/admin/alternatives", Label: "Product groups", Key: "alternatives",
		Desc: "Interchangeable product groups",
	})

	// Counter search: any group whose name / tier name / member name matches → the
	// whole group. Composes with other plugins via the core fan-out bridge.
	reg.AddProductSearchContributor(plugin.ProductSearchContributor{
		Match: func(ctx context.Context, q string) ([]int64, error) {
			return p.store.MatchProductIDs(ctx, q)
		},
	})
	// Till-card tier pin.
	reg.AddProductBadgeProvider(plugin.ProductBadgeProvider{Batch: p.store.BadgesFor})
}
