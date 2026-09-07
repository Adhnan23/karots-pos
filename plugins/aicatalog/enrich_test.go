package aicatalog

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestSellFromMarkup(t *testing.T) {
	// 357 cost * 1.4 markup = 499.8 -> whole = 500
	got, err := sellFromMarkup(decimal.RequireFromString("357"), decimal.RequireFromString("1.4"))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !got.Equal(decimal.RequireFromString("500")) {
		t.Fatalf("want 500, got %s", got)
	}
	if _, err := sellFromMarkup(decimal.RequireFromString("100"), decimal.RequireFromString("1")); err == nil {
		t.Fatal("markup <= 1 should error")
	}
}

func TestParseEnrichArrayWithSummaryProse(t *testing.T) {
	// A one-line summary before the array, plus a stray id we didn't send: the
	// array must still parse, keyed by id, and tolerate the surrounding text.
	raw := []byte("Here are your 2 items, all identified:\n" +
		`[{"id":17,"name":"Deep Groove Bearing 6200 2RS","category":"Bearings","confident":true},` +
		`{"id":18,"name":"Clutch Cable","category":"Clutch","confident":false}]` +
		"\nLet me know if anything's off.")
	got, err := parseEnrich(raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	if got[0].ID != 17 || got[0].Category != "Bearings" || !got[0].Confident {
		t.Fatalf("bad first result: %+v", got[0])
	}
	if got[1].ID != 18 || got[1].Confident {
		t.Fatalf("bad second result: %+v", got[1])
	}
}

func TestParseEnrichNoArrayErrors(t *testing.T) {
	if _, err := parseEnrich([]byte("sorry, I could not identify any of these")); err == nil {
		t.Fatal("a reply with no JSON array should error, not panic")
	}
}
