package tspl

import (
	"strings"
	"testing"
)

func TestDocumentBasics(t *testing.T) {
	out := string(Document(Input{
		Name:      "Onion 1kg",
		Code:      "4001234567890",
		Format:    "CODE128",
		PriceText: "Rs. 250.00",
		ShowPrice: true,
		Count:     3,
		WidthMM:   50,
		HeightMM:  25,
		GapMM:     2,
	}))

	wants := []string{
		"SIZE 50 mm, 25 mm",
		"GAP 2 mm, 0 mm",
		"CLS",
		"\"4001234567890\"", // barcode value
		"\"128\"",           // CODE128 type token
		"Rs. 250.00",        // price line
		"PRINT 3,1",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q\n---\n%s", w, out)
		}
	}
}

func TestDocumentDefaultsAndFormats(t *testing.T) {
	// Zero sizes/count fall back to sane defaults.
	out := string(Document(Input{Name: "x", Code: "123"}))
	if !strings.Contains(out, "SIZE 50 mm, 25 mm") {
		t.Errorf("expected default 50x25 size, got:\n%s", out)
	}
	if !strings.Contains(out, "PRINT 1,1") {
		t.Errorf("expected default count 1, got:\n%s", out)
	}

	cases := map[string]string{
		"EAN13":   "EAN13",
		"ean8":    "EAN8",
		"upc":     "UPCA",
		"CODE39":  "39",
		"unknown": "128",
	}
	for format, token := range cases {
		out := string(Document(Input{Code: "1", Format: format, Count: 1}))
		if !strings.Contains(out, "\""+token+"\"") {
			t.Errorf("format %q: expected type %q in:\n%s", format, token, out)
		}
	}
}

func TestPriceHiddenWhenNotShown(t *testing.T) {
	out := string(Document(Input{Code: "1", PriceText: "Rs. 9", ShowPrice: false, Count: 1}))
	if strings.Contains(out, "Rs. 9") {
		t.Errorf("price should be omitted when ShowPrice is false:\n%s", out)
	}
}

func TestAsciiSanitisesAndEscapes(t *testing.T) {
	out := string(Document(Input{Name: "Coke \"500\" අ", Code: "1", Count: 1}))
	if strings.Contains(out, "අ") {
		t.Errorf("non-ASCII rune should be replaced:\n%s", out)
	}
	if !strings.Contains(out, "\\\"500\\\"") {
		t.Errorf("embedded quotes should be escaped:\n%s", out)
	}
}
