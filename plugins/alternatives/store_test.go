package alternatives

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

// Group→tier→member CRUD and the exactly-one move, in a rolled-back tx.
func TestStoreGroupTierMember(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	s := &Store{db: tx}

	gid, err := s.CreateGroup(ctx, Group{Name: "zz_usb_32gb", SortOrder: 1})
	if err != nil {
		t.Fatal(err)
	}
	t1, err := s.CreateTier(ctx, Tier{GroupID: gid, Name: "zz_genuine", ReorderLevel: 5, SortOrder: 1})
	if err != nil {
		t.Fatal(err)
	}
	t2, err := s.CreateTier(ctx, Tier{GroupID: gid, Name: "zz_cheap", SortOrder: 2})
	if err != nil {
		t.Fatal(err)
	}

	var pid int64
	if err := tx.GetContext(ctx, &pid, `SELECT id FROM products ORDER BY id LIMIT 1`); err != nil {
		t.Skip("no products")
	}

	if err := s.AddMember(ctx, pid, t1); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.MembersOfTier(ctx, t1); len(got) != 1 || got[0] != pid {
		t.Fatalf("MembersOfTier(t1)=%v want [%d]", got, pid)
	}

	// Move to t2 (exactly-one): t1 empty, t2 has it.
	if err := s.AddMember(ctx, pid, t2); err != nil {
		t.Fatal(err)
	}
	if g1, _ := s.MembersOfTier(ctx, t1); len(g1) != 0 {
		t.Fatalf("after move t1 should be empty, got %v", g1)
	}
	tid, ok, _ := s.MemberTier(ctx, pid)
	if !ok || tid != t2 {
		t.Fatalf("MemberTier=%d,%v want %d,true", tid, ok, t2)
	}

	if err := s.RemoveMember(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.MemberTier(ctx, pid); ok {
		t.Fatal("member not removed")
	}
}

// Searching group / tier / member name returns the whole group.
func TestStoreMatchProductIDs(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, _ := conn.BeginTxx(ctx, nil)
	defer tx.Rollback() //nolint:errcheck
	s := &Store{db: tx}

	gid, _ := s.CreateGroup(ctx, Group{Name: "zz_usb_32gb"})
	tid, _ := s.CreateTier(ctx, Tier{GroupID: gid, Name: "zz_genuine"})

	var pids []int64
	if err := tx.SelectContext(ctx, &pids,
		`SELECT id FROM products WHERE is_active ORDER BY id LIMIT 2`); err != nil || len(pids) < 2 {
		t.Skip("need 2 active products")
	}
	for _, p := range pids {
		if err := s.AddMember(ctx, p, tid); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.MatchProductIDs(ctx, "zz_usb_32")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("group-name search got %v, want 2 members", got)
	}
}

// Badge is the tier name; a huge reorder level flags the tier low.
func TestStoreReorderAndBadges(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, _ := conn.BeginTxx(ctx, nil)
	defer tx.Rollback() //nolint:errcheck
	s := &Store{db: tx}

	gid, _ := s.CreateGroup(ctx, Group{Name: "zz_grp"})
	tid, _ := s.CreateTier(ctx, Tier{GroupID: gid, Name: "zz_genuine", ReorderLevel: 1000000})

	var pid int64
	if err := tx.GetContext(ctx, &pid, `SELECT id FROM products WHERE is_active ORDER BY id LIMIT 1`); err != nil {
		t.Skip("no products")
	}
	if err := s.AddMember(ctx, pid, tid); err != nil {
		t.Fatal(err)
	}

	b, err := s.BadgesFor(ctx, []int64{pid})
	if err != nil {
		t.Fatal(err)
	}
	if len(b[pid]) != 1 || b[pid][0] != "zz_genuine" {
		t.Fatalf("BadgesFor=%v want {%d:[zz_genuine]}", b, pid)
	}

	// Huge reorder level ⇒ tier total can't exceed it ⇒ NOT covered (needs reorder).
	cov, err := s.CoverageFor(ctx, []int64{pid})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := cov[pid]
	if !ok {
		t.Fatalf("coverage missing for %d", pid)
	}
	if c.Covered {
		t.Fatalf("should NOT be covered (reorder_level huge), got %+v", c)
	}
	if c.Group != "zz_grp" || c.Tier != "zz_genuine" {
		t.Fatalf("coverage note fields wrong: %+v", c)
	}

	// Drop the reorder level to 0-tracked-low then to a low number the tier clears:
	// with reorder_level 0 the tier is "don't track" ⇒ not covered.
	if err := s.UpdateTier(ctx, Tier{ID: tid, GroupID: gid, Name: "zz_genuine", ReorderLevel: 0}); err != nil {
		t.Fatal(err)
	}
	cov, _ = s.CoverageFor(ctx, []int64{pid})
	if cov[pid].Covered {
		t.Fatal("reorder_level 0 means don't track ⇒ not covered")
	}
}

// Summaries + AllMemberIDs sanity.
func TestStoreSummaries(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, _ := conn.BeginTxx(ctx, nil)
	defer tx.Rollback() //nolint:errcheck
	s := &Store{db: tx}

	gid, _ := s.CreateGroup(ctx, Group{Name: "zz_sum"})
	tid, _ := s.CreateTier(ctx, Tier{GroupID: gid, Name: "zz_t"})
	var pid int64
	if err := tx.GetContext(ctx, &pid, `SELECT id FROM products WHERE is_active ORDER BY id LIMIT 1`); err != nil {
		t.Skip("no products")
	}
	if err := s.AddMember(ctx, pid, tid); err != nil {
		t.Fatal(err)
	}

	sums, err := s.GroupSummaries(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	var ok bool
	for _, g := range sums {
		if g.ID == gid {
			ok = true
			if g.Tiers != 1 || g.Products != 1 {
				t.Fatalf("summary tiers=%d products=%d want 1/1", g.Tiers, g.Products)
			}
		}
	}
	if !ok {
		t.Fatal("group missing from summaries")
	}

	ids, err := s.AllMemberIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, id := range ids {
		if id == pid {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("AllMemberIDs missing %d", pid)
	}
}
