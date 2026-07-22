package expenses

import (
	"reflect"
	"testing"
)

func TestMergedCategoriesKeepsDefaultsFirst(t *testing.T) {
	got := MergedCategories(nil)
	want := DefaultCategories()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("with no DB rows, merged should equal defaults\n got: %v\nwant: %v", got, want)
	}
}

func TestMergedCategoriesAppendsNewDBCategories(t *testing.T) {
	got := MergedCategories([]string{"Bags", "Ink"})
	// Defaults first (canonical order), then the two new ones alphabetically.
	want := append(DefaultCategories(), "Bags", "Ink")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("new DB categories should append after defaults\n got: %v\nwant: %v", got, want)
	}
}

func TestMergedCategoriesDedupesCaseInsensitively(t *testing.T) {
	// "electricity" collides with default "Electricity"; "  water " with "Water".
	got := MergedCategories([]string{"electricity", "  water ", "Bags"})
	want := append(DefaultCategories(), "Bags")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("case/whitespace duplicates of defaults must not repeat\n got: %v\nwant: %v", got, want)
	}
}

func TestMergedCategoriesIgnoresBlankDBRows(t *testing.T) {
	got := MergedCategories([]string{"", "   "})
	want := DefaultCategories()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("blank DB rows must be ignored\n got: %v\nwant: %v", got, want)
	}
}
