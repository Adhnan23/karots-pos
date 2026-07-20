package products

import "testing"

// The Inventory Valuation report reported 14% of real stock value because it
// asked List for 10000 rows and Normalize turned that into 50 — fewer than the
// cap it exceeded. Over-asking must clamp DOWN TO the maximum, never below it.
func TestNormalizeClampsToMaxNotBelowIt(t *testing.T) {
	cases := []struct {
		name      string
		in        int
		wantLimit int
	}{
		{"zero falls back to the default page", 0, 50},
		{"negative falls back to the default page", -7, 50},
		{"an ordinary limit is respected", 25, 25},
		{"exactly the maximum is respected", MaxListLimit, MaxListLimit},
		{"one over the maximum clamps to the maximum", MaxListLimit + 1, MaxListLimit},
		{"a bulk-read limit clamps to the maximum", 10000, MaxListLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := ListQuery{Limit: tc.in}
			q.Normalize()
			if q.Limit != tc.wantLimit {
				t.Fatalf("Limit %d normalized to %d, want %d", tc.in, q.Limit, tc.wantLimit)
			}
			if q.Limit > MaxListLimit {
				t.Fatalf("Limit %d exceeds MaxListLimit %d", q.Limit, MaxListLimit)
			}
		})
	}
}

// An over-large request must never come back smaller than a modest one — the
// property that made the original truncation silent.
func TestNormalizeIsMonotonic(t *testing.T) {
	modest := ListQuery{Limit: 25}
	modest.Normalize()
	huge := ListQuery{Limit: 10000}
	huge.Normalize()
	if huge.Limit < modest.Limit {
		t.Fatalf("asking for 10000 returned %d, fewer than asking for 25 (%d)",
			huge.Limit, modest.Limit)
	}
}

func TestNormalizePageFloorsAtOne(t *testing.T) {
	for _, in := range []int{-1, 0, 1} {
		q := ListQuery{Page: in}
		q.Normalize()
		if q.Page < 1 {
			t.Fatalf("Page %d normalized to %d, want >= 1", in, q.Page)
		}
	}
	q := ListQuery{Page: 3, Limit: 100}
	q.Normalize()
	if got := q.offset(); got != 200 {
		t.Fatalf("page 3 of 100 has offset %d, want 200", got)
	}
}
