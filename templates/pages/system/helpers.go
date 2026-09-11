package systempages

// defaultHex gives the colour input a valid starting value: the stored custom
// hex, or the default indigo when none is set (an <input type=color> needs a
// concrete #rrggbb).
func defaultHex(stored string) string {
	if stored == "" {
		return "#4f46e5"
	}
	return stored
}
