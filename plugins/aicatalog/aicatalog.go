// Package aicatalog is an AI-assisted fast product-entry plugin. Core never
// imports it; it attaches through generic plugin hooks and reuses core product,
// stock and category services for all writes. Depends on no other plugin.
package aicatalog

import (
	"context"
	"io/fs"

	"karots-pos/internal/features/categories"
	"karots-pos/internal/plugin"
	"karots-pos/plugins/aicatalog/migrations"
)

func init() { plugin.Register(&Plugin{}) }

type Plugin struct {
	core  plugin.Core
	store *Store
	cats  *categories.Service
}

func (p *Plugin) Name() string                { return "AI Catalog" }
func (p *Plugin) Migrations() (fs.FS, string) { return migrations.FS, "aicatalog" }

func (p *Plugin) Setup(reg *plugin.Registry) {
	p.core = reg.Core
	p.store = NewStore(reg.Core.DB)
	p.cats = categories.NewService(reg.Core.DB)

	a := &adminUI{p: p}
	reg.Admin().GET("/ai-catalog", a.Page)
	reg.Admin().POST("/ai-catalog/settings", a.SaveSettings)
	reg.Admin().POST("/ai-catalog/models", a.ModelList)
	reg.Admin().POST("/ai-catalog/test-key", a.TestKey)
	reg.Admin().POST("/ai-catalog/identify", a.Identify)
	reg.Admin().POST("/ai-catalog/identify/manual", a.IdentifyManual)
	reg.Admin().POST("/ai-catalog/pick", a.Pick)
	reg.Admin().POST("/ai-catalog/category", a.Category)
	reg.Admin().POST("/ai-catalog/barcode", a.Barcode)
	reg.Admin().POST("/ai-catalog/price", a.Price)
	reg.Admin().POST("/ai-catalog/qty", a.Qty)
	reg.Admin().POST("/ai-catalog/save", a.Save)
	reg.Admin().GET("/ai-catalog/optimize", a.OptimizePreview)
	reg.Admin().POST("/ai-catalog/optimize/manual", a.OptimizeManual)
	reg.Admin().POST("/ai-catalog/optimize/apply", a.OptimizeApply)
	reg.Admin().POST("/ai-catalog/optimize/revert", a.OptimizeRevert)

	reg.AddAdminNav(plugin.AdminNavEntry{
		SectionLabel: "AI Catalog", Icon: "🤖",
		Href: "/admin/ai-catalog", Label: "Add products", Key: "aicatalog",
		Desc: "AI-assisted fast product entry",
	})

	// Info-popup row: show what the AI decided this product is.
	reg.AddProductDetailContributor(plugin.ProductDetailContributor{
		Rows: func(ctx context.Context, id int64) ([]plugin.DetailRow, error) {
			it, err := p.store.GetItem(ctx, id)
			if err != nil || it == nil || it.ResolvedName == "" {
				return nil, err
			}
			return []plugin.DetailRow{{Label: "AI identified", Value: it.ResolvedName}}, nil
		},
	})
}
