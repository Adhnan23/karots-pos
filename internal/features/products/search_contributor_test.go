package products

import (
	"context"
	"os"
	"testing"

	appdb "karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
)

func ppTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	conn, err := appdb.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

// A registered SearchContributor makes products match by id even when their name
// does not match the query; with no contributor the search is unchanged.
func TestSearchContributorIncludesIDs(t *testing.T) {
	conn := ppTestDB(t)
	defer conn.Close()
	ctx := context.Background()
	repo := NewRepository(conn)

	// A term that matches no product name in the seeded dev DB.
	const term = "zzqqnomatch"
	base, err := repo.List(ctx, ListQuery{Search: term, Page: 1, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 0 {
		t.Skipf("dev DB unexpectedly has a product matching %q; skipping", term)
	}

	// Pick any real product id to "match" via the contributor.
	all, err := repo.List(ctx, ListQuery{Page: 1, Limit: 1})
	if err != nil || len(all) == 0 {
		t.Skip("no products in dev DB to test with")
	}
	target := all[0].ID

	SearchContributor = func(context.Context, string) ([]int64, error) { return []int64{target}, nil }
	defer func() { SearchContributor = nil }()

	got, err := repo.List(ctx, ListQuery{Search: term, Page: 1, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range got {
		if p.ID == target {
			found = true
		}
	}
	if !found {
		t.Fatalf("contributor id %d not included in search results", target)
	}
}
