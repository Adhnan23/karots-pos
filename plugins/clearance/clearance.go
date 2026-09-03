// Package clearance flags slow-moving stock (has stock but no sale in N days),
// suggests a margin-safe markdown the owner approves or adjusts, and offers to
// apply that discount at the till. Core never imports it; it attaches through
// generic plugin hooks. Depends on no other plugin.
package clearance

import (
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
	// routes + hooks wired in Task 6 and Task 7.
}
