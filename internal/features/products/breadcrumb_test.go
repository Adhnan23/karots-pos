package products

import "testing"

func pid(v int64) *int64 { return &v }

func names(cs []Crumb) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func eq(a []string, b ...string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestOrderAncestorsReturnsRootFirst(t *testing.T) {
	rows := []catRow{
		{ID: 3, Name: "Batteries", ParentID: pid(2)},
		{ID: 1, Name: "Electronics", ParentID: nil},
		{ID: 2, Name: "Batteries & Power", ParentID: pid(1)},
	}
	got := names(orderAncestors(rows, 3))
	if !eq(got, "Electronics", "Batteries & Power", "Batteries") {
		t.Errorf("got %v", got)
	}
}

func TestOrderAncestorsHandlesARootLeaf(t *testing.T) {
	rows := []catRow{{ID: 1, Name: "Electronics", ParentID: nil}}
	if got := names(orderAncestors(rows, 1)); !eq(got, "Electronics") {
		t.Errorf("got %v", got)
	}
}

func TestOrderAncestorsReturnsNothingForAnUnknownLeaf(t *testing.T) {
	rows := []catRow{{ID: 1, Name: "Electronics", ParentID: nil}}
	if got := orderAncestors(rows, 99); len(got) != 0 {
		t.Errorf("got %d crumbs, want 0", len(got))
	}
}

// A corrupted parent_id loop must terminate rather than hang the report.
func TestOrderAncestorsSurvivesACycle(t *testing.T) {
	rows := []catRow{
		{ID: 1, Name: "A", ParentID: pid(2)},
		{ID: 2, Name: "B", ParentID: pid(1)},
	}
	got := orderAncestors(rows, 1)
	if len(got) > 2 {
		t.Errorf("cycle produced %d crumbs, want at most 2", len(got))
	}
}
