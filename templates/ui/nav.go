package ui

// Tab is one entry in a Tabs strip. Target is the href; Key matches the active
// argument to mark the current tab.
type Tab struct {
	Label  string
	Target string
	Key    string
}

// RangePreset is one quick date-range chip. Key is the value sent as ?preset=
// to the adopting page's action URL.
type RangePreset struct{ Key, Label string }

// rangePresets is the fixed set of quick ranges, authored fresh for the new
// date bar — conventional date-picker options.
var rangePresets = []RangePreset{
	{"today", "Today"},
	{"this-week", "This week"},
	{"this-month", "This month"},
	{"last-week", "Last week"},
	{"last-month", "Last month"},
	{"this-year", "This year"},
}
