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

	fid, err := s.CreateField(ctx, Field{
		Key: "model_no", Label: "Model No", Type: "text",
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
}
