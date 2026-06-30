package theme

import "testing"

func TestValidateInput(t *testing.T) {
	ok := "#aabbcc"
	bad := "zzz"
	tests := []struct {
		name    string
		in      Input
		wantErr bool
	}{
		{"valid", Input{Name: "Mine", Palette: "ocean", Mode: "auto", Density: "compact"}, false},
		{"valid custom accent", Input{Name: "Mine", Palette: "ocean", Mode: "dark", Density: "comfortable", Accent: &ok}, false},
		{"empty name", Input{Name: "", Palette: "ocean", Mode: "auto", Density: "compact"}, true},
		{"bad palette", Input{Name: "X", Palette: "nope", Mode: "auto", Density: "compact"}, true},
		{"bad mode", Input{Name: "X", Palette: "ocean", Mode: "sideways", Density: "compact"}, true},
		{"bad density", Input{Name: "X", Palette: "ocean", Mode: "auto", Density: "huge"}, true},
		{"bad accent", Input{Name: "X", Palette: "ocean", Mode: "auto", Density: "compact", Accent: &bad}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateInput(tc.in)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateInput(%+v) err=%v, wantErr=%v", tc.in, err, tc.wantErr)
			}
		})
	}
}
