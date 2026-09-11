package purchases

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"
)

// TestRefreshProductPricingAcceptsFractionalPrices pins the intermittent
// counter-receive failure: a fractional cost/selling price (e.g. 3.4, from a
// pro-rata discount spread over candy-sized items) used to make Postgres infer
// the $1/$2 parameters as integer — because of the bare `$1 > 0` against the
// literal 0 — and reject the decimal with "invalid input syntax for type
// integer" (22P02). Whole numbers parsed as int and passed, so it only failed
// some of the time. The parse error fires during bind regardless of whether the
// id matches a row, so a non-existent id reproduces it without seeding FKs.
func TestRefreshProductPricingAcceptsFractionalPrices(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	must(t, err)
	defer tx.Rollback() //nolint:errcheck

	repo := NewRepository(tx)
	dec := func(s string) decimal.Decimal { d, _ := decimal.NewFromString(s); return d }

	if err := repo.RefreshProductPricing(ctx, -1, dec("3.4"), dec("5.5")); err != nil {
		t.Fatalf("fractional prices rejected: %v", err)
	}
}
