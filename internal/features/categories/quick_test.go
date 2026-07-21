package categories

import "testing"

func TestCleanNameTrims(t *testing.T) {
	got, err := CleanName("  9V Batteries  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "9V Batteries" {
		t.Errorf("got %q, want %q", got, "9V Batteries")
	}
}

func TestCleanNameRejectsBlank(t *testing.T) {
	for _, in := range []string{"", "   ", "\t\n"} {
		if _, err := CleanName(in); err == nil {
			t.Errorf("CleanName(%q) returned no error", in)
		}
	}
}

// The parent is supplied structurally by the row that was tapped, so a ">" in
// the name is part of the name — it must not silently create extra levels.
func TestCleanNameRejectsAPathSeparator(t *testing.T) {
	if _, err := CleanName("Batteries > 9V"); err == nil {
		t.Error("a name containing '>' was accepted")
	}
}

func TestCleanNameRejectsOverlyLongNames(t *testing.T) {
	long := ""
	for i := 0; i < 81; i++ {
		long += "x"
	}
	if _, err := CleanName(long); err == nil {
		t.Error("an 81-character name was accepted")
	}
}
