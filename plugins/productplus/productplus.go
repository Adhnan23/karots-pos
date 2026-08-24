// Package productplus is the custom-product-fields plugin: an admin defines extra
// fields (text/number/bool/select) that are injected into the core product form
// and, opt-in, made searchable across every product-search surface. Core never
// imports it; it attaches through generic plugin hooks + the products search
// func-var seam.
package productplus

import (
	"context"
	"io/fs"

	"karots-pos/internal/features/products"
	"karots-pos/internal/plugin"
	"karots-pos/plugins/productplus/migrations"
)

func init() { plugin.Register(&Plugin{}) }

type Plugin struct {
	core  plugin.Core
	store *Store
}

func (p *Plugin) Name() string { return "Product Plus" }

func (p *Plugin) Migrations() (fs.FS, string) { return migrations.FS, "productplus" }

func (p *Plugin) Setup(reg *plugin.Registry) {
	p.core = reg.Core
	p.store = NewStore(reg.Core.DB)

	a := &adminUI{p: p}
	reg.Admin().GET("/productplus", a.Page)
	reg.Admin().GET("/productplus/table", a.Table)
	reg.Admin().GET("/productplus/form", a.FieldForm)
	reg.Admin().GET("/productplus/form/:id", a.FieldForm)
	reg.Admin().POST("/productplus", a.CreateField)
	reg.Admin().PUT("/productplus/:id", a.UpdateField)
	reg.Admin().POST("/productplus/:id/active", a.SetActive)

	reg.AddAdminNav(plugin.AdminNavEntry{
		SectionLabel: "Product Plus", Icon: "🧩",
		Href: "/admin/productplus", Label: "Custom fields", Key: "productplus",
		Desc: "Define extra product fields",
	})

	// Form injection + persistence (Task 6/7).
	reg.AddProductFormSection(plugin.ProductFormSection{Render: p.renderProductForm})
	reg.AddProductFormValidate(p.validateProductForm)
	reg.AddProductSaved(p.saveProductForm)

	// Search: register the hook AND set the products func-var seam. Only one plugin
	// owns product custom-field search, so setting the var here is safe.
	reg.AddProductSearchContributor(plugin.ProductSearchContributor{Match: p.matchProducts})
	products.SearchContributor = func(ctx context.Context, q string) ([]int64, error) {
		return p.store.MatchProductIDs(ctx, q)
	}
}

func (p *Plugin) matchProducts(ctx context.Context, q string) ([]int64, error) {
	return p.store.MatchProductIDs(ctx, q)
}
