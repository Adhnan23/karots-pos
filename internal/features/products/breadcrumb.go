package products

// Crumb is one step of the category path shown above the inventory report.
type Crumb struct {
	ID   int64
	Name string
}

// catRow is a category as the ancestor query returns it — flat and unordered.
type catRow struct {
	ID       int64  `db:"id"`
	Name     string `db:"name"`
	ParentID *int64 `db:"parent_id"`
}

// orderAncestors walks from leafID up to the root and returns the path
// root-first. Ordering in Go rather than SQL keeps it testable without a
// database, and lets a corrupted parent_id cycle terminate instead of hanging
// the report: every id is visited at most once.
func orderAncestors(rows []catRow, leafID int64) []Crumb {
	byID := make(map[int64]catRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	var rev []Crumb
	seen := make(map[int64]bool, len(rows))
	for id := leafID; ; {
		r, ok := byID[id]
		if !ok || seen[id] {
			break
		}
		seen[id] = true
		rev = append(rev, Crumb{ID: r.ID, Name: r.Name})
		if r.ParentID == nil {
			break
		}
		id = *r.ParentID
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}
