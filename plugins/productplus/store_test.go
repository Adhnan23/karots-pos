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

// TestMoveField proves the ▲▼ reorder flips the relative order of adjacent fields.
// Runs inside a rolled-back tx; the three test fields sort at the end (high
// sort_order) so live dev fields don't interfere with their relative order.
func TestMoveField(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	s := &Store{db: tx}

	keys := []string{"zz_move_a", "zz_move_b", "zz_move_c"}
	ids := make(map[string]int64)
	for i, k := range keys {
		id, err := s.CreateField(ctx, Field{Key: k, Label: k, Type: "text", SortOrder: 9000 + i, IsActive: true})
		if err != nil {
			t.Fatal(err)
		}
		ids[k] = id
	}

	// relOrder returns the keys of our three test fields in current display order.
	relOrder := func() []string {
		fields, err := s.Fields(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, f := range fields {
			if _, ours := ids[f.Key]; ours {
				out = append(out, f.Key)
			}
		}
		return out
	}

	if got := relOrder(); len(got) != 3 || got[0] != "zz_move_a" || got[1] != "zz_move_b" || got[2] != "zz_move_c" {
		t.Fatalf("initial order = %v, want [a b c]", got)
	}
	// Move B up → [b a c].
	if err := s.MoveField(ctx, ids["zz_move_b"], true); err != nil {
		t.Fatal(err)
	}
	if got := relOrder(); got[0] != "zz_move_b" || got[1] != "zz_move_a" || got[2] != "zz_move_c" {
		t.Fatalf("after move-up B, order = %v, want [b a c]", got)
	}
	// Move B down twice → [a c b]; a second-from-edge then edge no-op stays put.
	_ = s.MoveField(ctx, ids["zz_move_b"], false)
	_ = s.MoveField(ctx, ids["zz_move_b"], false)
	if got := relOrder(); got[0] != "zz_move_a" || got[1] != "zz_move_c" || got[2] != "zz_move_b" {
		t.Fatalf("after moving B to the end, order = %v, want [a c b]", got)
	}
}
