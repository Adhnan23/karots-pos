package aicatalog

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestCostFromMarkup(t *testing.T) {
	// 500 selling / 1.4 markup = 357.14 -> rounded whole = 357
	got, err := costFromMarkup(decimal.RequireFromString("500"), decimal.RequireFromString("1.4"))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !got.Equal(decimal.RequireFromString("357")) {
		t.Fatalf("want 357, got %s", got)
	}
}

func TestCostFromMarkupRejectsMarkupLEOne(t *testing.T) {
	if _, err := costFromMarkup(decimal.RequireFromString("500"), decimal.RequireFromString("1")); err == nil {
		t.Fatal("markup <= 1 should error")
	}
	if _, err := costFromMarkup(decimal.RequireFromString("500"), decimal.Zero); err == nil {
		t.Fatal("markup 0 should error (no divide by zero)")
	}
}

func TestParseIdentifyConfident(t *testing.T) {
	raw := []byte(`{"confident":true,"best":{"name":"Deep Groove Ball Bearing 6200 2RS","category":"Bearings/Deep Groove","specs":"10x30x9mm sealed","explanation":"Sealed radial bearing"},"options":[]}`)
	got, err := parseIdentify(raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !got.Confident || got.Best.Category != "Bearings/Deep Groove" {
		t.Fatalf("bad parse: %+v", got)
	}
}

func TestParseIdentifyStripsCodeFence(t *testing.T) {
	raw := []byte("```json\n{\"confident\":false,\"best\":{},\"options\":[{\"name\":\"A\",\"category\":\"X\"},{\"name\":\"B\",\"category\":\"Y\"}]}\n```")
	got, err := parseIdentify(raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Confident || len(got.Options) != 2 {
		t.Fatalf("bad parse: %+v", got)
	}
}

func TestParseIdentifyGarbageErrors(t *testing.T) {
	if _, err := parseIdentify([]byte("not json at all")); err == nil {
		t.Fatal("garbage should error, not panic")
	}
}
