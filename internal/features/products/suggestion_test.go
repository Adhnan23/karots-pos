package products

import (
	"context"
	"testing"
)

func TestApplySuggestionSetsField(t *testing.T) {
	orig := SaleSuggestionProvider
	t.Cleanup(func() { SaleSuggestionProvider = orig })
	SaleSuggestionProvider = func(_ context.Context, ids []int64) map[int64]SaleSuggestion {
		return map[int64]SaleSuggestion{7: {DiscountType: "percent", DiscountValue: "20", Label: "Clearance -20%", Prompt: "Apply?"}}
	}
	rows := []Product{{ID: 7}, {ID: 9}}
	applySuggestions(context.Background(), rows)
	if rows[0].Suggestion == nil || rows[0].Suggestion.DiscountValue != "20" {
		t.Fatalf("id 7 should have suggestion, got %+v", rows[0].Suggestion)
	}
	if rows[1].Suggestion != nil {
		t.Fatalf("id 9 should have no suggestion")
	}
}
