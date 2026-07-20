package products

import (
	"strings"
	"testing"
)

func TestSearchTokensSplitsOnWhitespace(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"yellow", []string{"yellow"}},
		{"Yellow Flip Flop", []string{"yellow", "flip", "flop"}},
		{"  spaced   out  ", []string{"spaced", "out"}},
		{"UPPER lower MiXeD", []string{"upper", "lower", "mixed"}},
	}
	for _, tc := range cases {
		got := searchTokens(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("searchTokens(%q) = %v, want %v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("searchTokens(%q) = %v, want %v", tc.in, got, tc.want)
			}
		}
	}
}

// squashedName must agree with the SQL translate() call in searchClause and
// with migration 0044's functional index. If they drift, the index silently
// stops being used and "flipflop" stops finding "Flip Flop".
func TestSquashedNameMatchesSQLTranslateSet(t *testing.T) {
	cases := map[string]string{
		"Flip Flop":  "flipflop",
		"A-4":        "a4",
		"Multi_Part": "multipart",
		"a/b.c":      "abc",
		"Already":    "already",
		"":           "",
	}
	for in, want := range cases {
		if got := squashedName(in); got != want {
			t.Fatalf("squashedName(%q) = %q, want %q", in, got, want)
		}
	}
	// Every character the Go helper strips must also appear in the SQL
	// translate() 'from' set, or the two implementations disagree.
	for _, r := range squashChars {
		if !strings.ContainsRune(" -_/.", r) {
			t.Fatalf("squashChars contains %q, which the SQL translate() set omits", r)
		}
	}
}

// The regression this whole change exists for: word order must not matter.
// "yellow flip flop size 4" has to find "Bigo Trivago Yellow Flip Flop Size 4",
// which the old single-substring search could not do.
func TestSearchTokensMakeOrderIrrelevant(t *testing.T) {
	const product = "Bigo Trivago Yellow Flip Flop Size 4"
	matches := func(query string) bool {
		for _, tok := range searchTokens(query) {
			plain := strings.Contains(strings.ToLower(product), tok)
			squashed := strings.Contains(squashedName(product), squashedName(tok))
			if !plain && !squashed {
				return false
			}
		}
		return true
	}
	for _, q := range []string{
		"yellow flip flop size 4",
		"size 4 yellow",
		"flip flop yellow",
		"yellow flipflop size 4", // joined word, needs the squashed form
		"BIGO yellow",            // case-insensitive
	} {
		if !matches(q) {
			t.Errorf("query %q should match %q but did not", q, product)
		}
	}
	for _, q := range []string{
		"yellow flip flop size 5", // wrong size: a token genuinely absent
		"green flip flop",
	} {
		if matches(q) {
			t.Errorf("query %q should NOT match %q", q, product)
		}
	}
}

func TestFuzzyThresholdSQLTracksTheConstant(t *testing.T) {
	if fuzzyThresholdSQL != "0.45" {
		t.Fatalf("fuzzyThresholdSQL = %q, want the rendering of %v",
			fuzzyThresholdSQL, fuzzyThreshold)
	}
	if !strings.Contains(searchClause, fuzzyThresholdSQL) {
		t.Fatal("searchClause does not embed fuzzyThresholdSQL")
	}
}
