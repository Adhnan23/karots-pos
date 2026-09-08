// Package repairs tracks device repair jobs from drop-off to pickup and settles
// the money as a normal core sale. Core never imports it; it attaches only
// through generic plugin hooks and is inert when not built.
package repairs

import (
	"io/fs"

	"karots-pos/internal/plugin"
	"karots-pos/plugins/repairs/migrations"
)

func init() { plugin.Register(&Plugin{}) }

type Plugin struct {
	core  plugin.Core
	store *Store
}

func (p *Plugin) Name() string               { return "Repairs" }
func (p *Plugin) Migrations() (fs.FS, string) { return migrations.FS, "repairs" }

func (p *Plugin) Setup(reg *plugin.Registry) {
	p.core = reg.Core
	p.store = NewStore(reg.Core.DB)
	// routes + hooks added in later tasks
}
