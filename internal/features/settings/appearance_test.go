package settings

import "testing"

func TestValidAppearanceFallsBack(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"unknown skin → default", ValidSkin("nope"), "default"},
		{"known skin passes", ValidSkin("teal"), "teal"},
		{"blank skin → default", ValidSkin(""), "default"},
		{"unknown density → comfortable", ValidDensity("huge"), "comfortable"},
		{"known density passes", ValidDensity("compact"), "compact"},
		{"blank receipt → classic", ValidReceiptStyle(""), "classic"},
		{"known receipt passes", ValidReceiptStyle("boxed"), "boxed"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q want %q", c.name, c.got, c.want)
		}
	}
}
