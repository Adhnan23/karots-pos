package plugin

import (
	"context"
	"testing"
)

func TestSaleSuggestionProviderRegistration(t *testing.T) {
	before := len(ProductSaleSuggestionProviders())
	r := &Registry{}
	r.AddProductSaleSuggestionProvider(ProductSaleSuggestionProvider{
		Batch: func(_ context.Context, ids []int64) (map[int64]SaleSuggestion, error) {
			return map[int64]SaleSuggestion{ids[0]: {DiscountType: "percent", DiscountValue: "20", Label: "x", Prompt: "y"}}, nil
		},
	})
	if got := len(ProductSaleSuggestionProviders()); got != before+1 {
		t.Fatalf("provider not registered: got %d want %d", got, before+1)
	}
}
