package layouts

import "karots-pos/internal/plugin"

// This file weaves plugin-contributed UI (registered via the plugin Registry's
// Add* hooks) into the core admin/cashier shells. It is the only place the
// template layer reads plugin.* — keeping the .templ files free of that import.
// All of it is additive; whole-page overrides go through the route Mux instead.

// withPluginSections merges plugin admin-nav entries into the core sections:
//   - an entry whose Section matches a core section key is appended to it;
//   - entries with no (or an unknown) section key are grouped by SectionLabel
//     into new top-level sections, appended after the core ones in registration
//     order.
func withPluginSections(core []AdminSection) []AdminSection {
	entries := plugin.AdminNav()
	if len(entries) == 0 {
		return core
	}
	idx := map[string]int{}
	for i, s := range core {
		idx[s.Key] = i
	}
	var newOrder []string
	newSecs := map[string]*AdminSection{}
	for _, e := range entries {
		link := AdminLink{Href: e.Href, Label: e.Label, Key: e.Key, Desc: e.Desc}
		if e.Section != "" {
			if i, ok := idx[e.Section]; ok {
				core[i].Links = append(core[i].Links, link)
				continue
			}
		}
		label, key := e.SectionLabel, e.SectionLabel
		if label == "" {
			label, key = e.Section, e.Section
		}
		s, ok := newSecs[key]
		if !ok {
			s = &AdminSection{Label: label, Href: e.Href, Key: key, Icon: e.Icon}
			newSecs[key] = s
			newOrder = append(newOrder, key)
		}
		s.Links = append(s.Links, link)
	}
	for _, k := range newOrder {
		core = append(core, *newSecs[k])
	}
	return core
}

// pluginPalette adapts plugin palette entries to the layout's paletteEntry type.
func pluginPalette() []paletteEntry {
	pe := plugin.PaletteEntries()
	if len(pe) == 0 {
		return nil
	}
	out := make([]paletteEntry, 0, len(pe))
	for _, p := range pe {
		out = append(out, paletteEntry{Href: p.Href, Label: p.Label, Group: p.Group})
	}
	return out
}

// navTab is a plugin cashier tab adapted for the cashier shell.
type navTab struct {
	Href, Label, Key string
}

// pluginCashierTabs adapts plugin cashier tabs for the terminal shell.
func pluginCashierTabs() []navTab {
	ct := plugin.CashierTabs()
	if len(ct) == 0 {
		return nil
	}
	out := make([]navTab, 0, len(ct))
	for _, t := range ct {
		out = append(out, navTab{Href: t.Href, Label: t.Label, Key: t.Key})
	}
	return out
}
