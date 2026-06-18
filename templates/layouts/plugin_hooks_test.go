package layouts

import (
	"testing"

	"karots-pos/internal/plugin"
)

// A plugin nav entry naming an existing section key must nest under it; one with
// a SectionLabel must appear as a new top-level section. This is what makes a
// plugin's admin pages reachable from the core sidebar/palette.
func TestWithPluginSections(t *testing.T) {
	reg := plugin.NewRegistry(plugin.Core{}, plugin.NewMux(), nil)
	reg.AddAdminNav(plugin.AdminNavEntry{Section: "setup", Href: "/admin/x", Label: "X", Key: "plug-x", Desc: "d"})
	reg.AddAdminNav(plugin.AdminNavEntry{SectionLabel: "Recharge", Icon: "📶", Href: "/admin/recharge", Label: "Carriers", Key: "plug-carriers"})

	var nested, topLevel bool
	for _, s := range AdminSections() {
		if s.Key == "setup" {
			for _, l := range s.Links {
				if l.Key == "plug-x" {
					nested = true
				}
			}
		}
		if s.Key == "Recharge" && len(s.Links) == 1 && s.Links[0].Key == "plug-carriers" {
			topLevel = true
		}
	}
	if !nested {
		t.Error("plugin link did not nest under the existing 'setup' section")
	}
	if !topLevel {
		t.Error("plugin top-level 'Recharge' section was not created")
	}
}
