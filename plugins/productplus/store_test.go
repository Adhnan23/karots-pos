package productplus

import (
	"context"
	"os"
	"testing"

	appdb "karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
)

func testDB(t *testing.T) *sqlx.DB {
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

// Field CRUD, value upsert around the default, and substring Match — all inside a
// rolled-back tx so the dev DB is left untouched.
func TestStoreFieldValueMatch(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck

	s := &Store{db: tx}

	// A test-only key so it never clashes with fields created in the live dev DB
	// (the whole test rolls back, but the key's UNIQUE index sees committed rows).
	fid, err := s.CreateField(ctx, Field{
		Key: "zz_test_model_no", Label: "Model No", Type: "text",
		DefaultValue: "", Required: true, Searchable: true, SortOrder: 1, IsActive: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Use an existing product id from the seeded DB.
	var pid int64
	if err := tx.GetContext(ctx, &pid, `SELECT id FROM products ORDER BY id LIMIT 1`); err != nil {
		t.Skip("no products to attach a value to")
	}

	if err := s.SetValue(ctx, fid, pid, "XJ-900"); err != nil {
		t.Fatal(err)
	}
	vals, err := s.Values(ctx, pid)
	if err != nil || vals[fid] != "XJ-900" {
		t.Fatalf("Values = %v, want field %d = XJ-900", vals, fid)
	}

	ids, err := s.MatchProductIDs(ctx, "xj-9")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != pid {
		t.Fatalf("MatchProductIDs = %v, want [%d]", ids, pid)
	}

	// Deleting the value row returns to "absence = default".
	if err := s.DeleteValue(ctx, fid, pid); err != nil {
		t.Fatal(err)
	}
	vals, _ = s.Values(ctx, pid)
	if _, ok := vals[fid]; ok {
		t.Fatalf("value not deleted: %v", vals)
	}

	// A searchable bool is found by its LABEL when Yes (value "1"), not by "1".
	bid, err := s.CreateField(ctx, Field{
		Key: "zz_test_waterproof", Label: "ZZ Waterproof", Type: "bool",
		Searchable: true, SortOrder: 2, IsActive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetValue(ctx, bid, pid, "1"); err != nil {
		t.Fatal(err)
	}
	// pid now has ONLY the bool value in this tx (its text value was deleted above).
	ids, err = s.MatchProductIDs(ctx, "zz waterproof")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != pid {
		t.Fatalf("bool label search = %v, want [%d]", ids, pid)
	}
	// The bool's raw "1" must NOT match it — bool is found only by label. (Other
	// live products may match "1" via their own text values; we only assert pid.)
	got, _ := s.MatchProductIDs(ctx, "1")
	for _, id := range got {
		if id == pid {
			t.Fatalf("bool value '1' matched its own product %d", pid)
		}
	}
}
