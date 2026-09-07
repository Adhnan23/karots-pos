package aicatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
)

// --- data the Optimize AI call reasons over ---

type CatRow struct {
	ID       int64  `db:"id"`
	Name     string `db:"name"`
	ParentID *int64 `db:"parent_id"`
	Count    int    `db:"cnt"`
}

type ProdRow struct {
	ID         int64  `db:"id"`
	Name       string `db:"name"`
	CategoryID int64  `db:"category_id"`
}

// OptimizeCategories lists every category with its parent and how many active
// products sit directly in it.
func (s *Store) OptimizeCategories(ctx context.Context) ([]CatRow, error) {
	var rows []CatRow
	err := s.db.SelectContext(ctx, &rows, `
		SELECT c.id, c.name, c.parent_id,
		       (SELECT count(*) FROM products p WHERE p.category_id = c.id AND p.is_active) AS cnt
		FROM categories c
		ORDER BY c.parent_id NULLS FIRST, c.name`)
	return rows, err
}

// CategoryPaths returns every category as a full "Parent > Child" path so the
// identify step can show the AI which categories already exist and have it
// reuse a fitting one instead of inventing an inconsistent new label. Capped so
// a pathological catalog can't blow the prompt.
func (s *Store) CategoryPaths(ctx context.Context) ([]string, error) {
	rows, err := s.OptimizeCategories(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]CatRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	paths := make([]string, 0, len(rows))
	for _, r := range rows {
		parts := []string{r.Name}
		cur := r
		for i := 0; i < 20 && cur.ParentID != nil; i++ {
			p, ok := byID[*cur.ParentID]
			if !ok {
				break
			}
			parts = append([]string{p.Name}, parts...)
			cur = p
		}
		paths = append(paths, strings.Join(parts, " > "))
		if len(paths) >= 200 { // ponytail: dozens typical; cap guards a huge tree
			break
		}
	}
	return paths, nil
}

// OptimizeProducts lists a page of active products with their current category.
// Paging (limit+offset over a stable id order) lets a large catalog be optimised
// in chunks — each chunk small enough to paste into a chatbot. Products are never
// deleted by a plan, so the id order stays stable across chunks.
func (s *Store) OptimizeProducts(ctx context.Context, limit, offset int) ([]ProdRow, error) {
	var rows []ProdRow
	err := s.db.SelectContext(ctx, &rows, `
		SELECT id, name, category_id FROM products
		WHERE is_active AND is_service = false
		ORDER BY id LIMIT $1 OFFSET $2`, limit, offset)
	return rows, err
}

// CountOptimizeProducts is the total the chunked Optimize pages through, so the
// UI can show "chunk k of K" and know when the last chunk is done.
func (s *Store) CountOptimizeProducts(ctx context.Context) (int, error) {
	var n int
	err := s.db.GetContext(ctx, &n,
		`SELECT count(*) FROM products WHERE is_active AND is_service = false`)
	return n, err
}

// --- the plan the AI returns ---

type OptimizePlan struct {
	Renames []struct {
		ID     int64  `json:"id"`
		To     string `json:"to"`
		Reason string `json:"reason"`
	} `json:"renames"`
	Reparents []struct {
		ID       int64  `json:"id"`
		ParentID int64  `json:"parent_id"` // 0 = make top-level
		Reason   string `json:"reason"`
	} `json:"reparents"`
	Merges []struct {
		From   int64  `json:"from"`
		Into   int64  `json:"into"`
		Reason string `json:"reason"`
	} `json:"merges"`
	Moves []struct {
		ProductID  int64  `json:"product_id"`
		CategoryID int64  `json:"category_id"`
		Reason     string `json:"reason"`
	} `json:"moves"`
}

func (p OptimizePlan) empty() bool {
	return len(p.Renames)+len(p.Reparents)+len(p.Merges)+len(p.Moves) == 0
}

// parseOptimizePlan tolerantly parses the model's reply (strips code fences).
func parseOptimizePlan(raw []byte) (OptimizePlan, error) {
	var out OptimizePlan
	s := strings.TrimSpace(string(raw))
	if i := strings.IndexByte(s, '{'); i >= 0 {
		if j := strings.LastIndexByte(s, '}'); j >= i {
			s = s[i : j+1]
		}
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return OptimizePlan{}, err
	}
	return out, nil
}

// --- apply, with a captured undo list, in one transaction ---

type OptimizeRun struct {
	ID      int64  `db:"id"`
	Summary string `db:"summary"`
}

// ApplyPlan validates the plan against real ids and applies it in a single
// transaction, recording the exact undo ops so it can be reverted. Invalid or
// unsafe ops (unknown ids, cycles, self-merge) are skipped, not fatal.
//
// runID threads a chunked Optimize session into ONE run: pass 0 for the first
// chunk (a new run is created) and the returned id for later chunks (their undo
// ops are appended to the same run). A single Revert then rolls back the whole
// session. Returns the human summary and the run id (0 when nothing was applied
// and no run existed yet).
func (s *Store) ApplyPlan(ctx context.Context, plan OptimizePlan, userID, runID int64) (string, int64, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return "", 0, err
	}
	defer tx.Rollback()

	catExists := map[int64]bool{}
	{
		var ids []int64
		if err := tx.SelectContext(ctx, &ids, `SELECT id FROM categories`); err != nil {
			return "", 0, err
		}
		for _, id := range ids {
			catExists[id] = true
		}
	}
	prodExists := map[int64]bool{}
	{
		var ids []int64
		if err := tx.SelectContext(ctx, &ids, `SELECT id FROM products`); err != nil {
			return "", 0, err
		}
		for _, id := range ids {
			prodExists[id] = true
		}
	}

	var undo []map[string]any
	var nRen, nRep, nMerge, nMove int

	catRow := func(id int64) (string, *int64, error) {
		var r struct {
			Name     string `db:"name"`
			ParentID *int64 `db:"parent_id"`
		}
		err := tx.GetContext(ctx, &r, `SELECT name, parent_id FROM categories WHERE id=$1`, id)
		return r.Name, r.ParentID, err
	}

	// Renames
	for _, op := range plan.Renames {
		if !catExists[op.ID] || strings.TrimSpace(op.To) == "" {
			continue
		}
		old, _, err := catRow(op.ID)
		if err != nil || old == op.To {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE categories SET name=$2 WHERE id=$1`, op.ID, op.To); err != nil {
			return "", 0, err
		}
		undo = append(undo, map[string]any{"kind": "rename", "id": op.ID, "name": old})
		nRen++
	}

	// Reparents (guard against cycles: new parent must not be the node or its descendant)
	for _, op := range plan.Reparents {
		if !catExists[op.ID] {
			continue
		}
		var newParent *int64
		if op.ParentID != 0 {
			if !catExists[op.ParentID] || op.ParentID == op.ID {
				continue
			}
			if desc, err := isDescendant(ctx, tx, op.ID, op.ParentID); err != nil {
				return "", 0, err
			} else if desc {
				continue
			}
			p := op.ParentID
			newParent = &p
		}
		_, oldParent, err := catRow(op.ID)
		if err != nil {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE categories SET parent_id=$2 WHERE id=$1`, op.ID, newParent); err != nil {
			return "", 0, err
		}
		undo = append(undo, map[string]any{"kind": "reparent", "id": op.ID, "parent_id": oldParent})
		nRep++
	}

	// Merges (from -> into): move products & child cats, then delete `from`.
	for _, op := range plan.Merges {
		if !catExists[op.From] || !catExists[op.Into] || op.From == op.Into {
			continue
		}
		name, parent, err := catRow(op.From)
		if err != nil {
			continue
		}
		var prodIDs, childIDs []int64
		if err := tx.SelectContext(ctx, &prodIDs, `SELECT id FROM products WHERE category_id=$1`, op.From); err != nil {
			return "", 0, err
		}
		if err := tx.SelectContext(ctx, &childIDs, `SELECT id FROM categories WHERE parent_id=$1`, op.From); err != nil {
			return "", 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE products SET category_id=$2 WHERE category_id=$1`, op.From, op.Into); err != nil {
			return "", 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE categories SET parent_id=$2 WHERE parent_id=$1`, op.From, op.Into); err != nil {
			return "", 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM categories WHERE id=$1`, op.From); err != nil {
			return "", 0, err
		}
		catExists[op.From] = false
		undo = append(undo, map[string]any{
			"kind": "unmerge", "id": op.From, "name": name, "parent_id": parent,
			"products": prodIDs, "children": childIDs,
		})
		nMerge++
	}

	// Product moves
	for _, op := range plan.Moves {
		if !prodExists[op.ProductID] || !catExists[op.CategoryID] {
			continue
		}
		var oldCat int64
		if err := tx.GetContext(ctx, &oldCat, `SELECT category_id FROM products WHERE id=$1`, op.ProductID); err != nil {
			continue
		}
		if oldCat == op.CategoryID {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE products SET category_id=$2 WHERE id=$1`, op.ProductID, op.CategoryID); err != nil {
			return "", 0, err
		}
		undo = append(undo, map[string]any{"kind": "move_product", "id": op.ProductID, "category_id": oldCat})
		nMove++
	}

	if len(undo) == 0 {
		// Nothing changed this chunk — keep the session's run id (0 if none yet).
		return "No changes applied.", runID, tx.Commit()
	}
	summary := fmt.Sprintf("%d rename, %d re-parent, %d merge, %d product move", nRen, nRep, nMerge, nMove)

	if runID != 0 {
		// Append this chunk's undo ops to the existing session run so one Revert
		// rolls the whole session back. Reverse-order replay stays correct: ops
		// appended last (applied last) are undone first.
		var run struct {
			Undo    []byte `db:"undo"`
			Summary string `db:"summary"`
		}
		if err := tx.GetContext(ctx, &run,
			`SELECT undo, summary FROM aicatalog_optimize_runs WHERE id=$1 FOR UPDATE`, runID); err != nil {
			return "", 0, err
		}
		var prev []map[string]any
		_ = json.Unmarshal(run.Undo, &prev)
		merged, _ := json.Marshal(append(prev, undo...))
		combined := run.Summary + "; " + summary
		if _, err := tx.ExecContext(ctx,
			`UPDATE aicatalog_optimize_runs SET summary=$2, undo=$3 WHERE id=$1`,
			runID, combined, merged); err != nil {
			return "", 0, err
		}
		return combined, runID, tx.Commit()
	}

	undoJSON, _ := json.Marshal(undo)
	var newID int64
	if err := tx.GetContext(ctx, &newID,
		`INSERT INTO aicatalog_optimize_runs (applied_by, summary, undo) VALUES ($1,$2,$3) RETURNING id`,
		userID, summary, undoJSON); err != nil {
		return "", 0, err
	}
	return summary, newID, tx.Commit()
}

// isDescendant reports whether `node` is `ancestor` or sits below it, walking up
// from node's candidate parent. Used to stop a reparent from forming a cycle.
func isDescendant(ctx context.Context, tx *sqlx.Tx, ancestor, node int64) (bool, error) {
	cur := &node
	for i := 0; i < 100 && cur != nil; i++ {
		if *cur == ancestor {
			return true, nil
		}
		var next *int64
		if err := tx.GetContext(ctx, &next, `SELECT parent_id FROM categories WHERE id=$1`, *cur); err != nil {
			return false, err
		}
		cur = next
	}
	return false, nil
}

// LatestRun returns the most recent un-reverted run, or nil if none.
func (s *Store) LatestRun(ctx context.Context) (*OptimizeRun, error) {
	var r OptimizeRun
	err := s.db.GetContext(ctx, &r,
		`SELECT id, summary FROM aicatalog_optimize_runs WHERE reverted_at IS NULL ORDER BY id DESC LIMIT 1`)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// Revert replays the undo ops of the latest un-reverted run, restoring the prior
// category tree and product placements, then marks the run reverted.
func (s *Store) Revert(ctx context.Context) (string, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var run struct {
		ID   int64  `db:"id"`
		Sum  string `db:"summary"`
		Undo []byte `db:"undo"`
	}
	err = tx.GetContext(ctx, &run,
		`SELECT id, summary, undo FROM aicatalog_optimize_runs WHERE reverted_at IS NULL ORDER BY id DESC LIMIT 1`)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return "", fmt.Errorf("nothing to revert")
		}
		return "", err
	}
	var ops []map[string]any
	if err := json.Unmarshal(run.Undo, &ops); err != nil {
		return "", err
	}
	// Replay in reverse of how they were applied.
	for i := len(ops) - 1; i >= 0; i-- {
		if err := revertOp(ctx, tx, ops[i]); err != nil {
			return "", err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE aicatalog_optimize_runs SET reverted_at=now() WHERE id=$1`, run.ID); err != nil {
		return "", err
	}
	return run.Sum, tx.Commit()
}

func revertOp(ctx context.Context, tx *sqlx.Tx, op map[string]any) error {
	switch op["kind"] {
	case "rename":
		_, err := tx.ExecContext(ctx, `UPDATE categories SET name=$2 WHERE id=$1`, toInt(op["id"]), op["name"])
		return err
	case "reparent":
		_, err := tx.ExecContext(ctx, `UPDATE categories SET parent_id=$2 WHERE id=$1`, toInt(op["id"]), toNullInt(op["parent_id"]))
		return err
	case "move_product":
		_, err := tx.ExecContext(ctx, `UPDATE products SET category_id=$2 WHERE id=$1`, toInt(op["id"]), toInt(op["category_id"]))
		return err
	case "unmerge":
		id := toInt(op["id"])
		// Recreate the deleted category with its original id.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO categories (id, name, parent_id) VALUES ($1,$2,$3) ON CONFLICT (id) DO NOTHING`,
			id, op["name"], toNullInt(op["parent_id"])); err != nil {
			return err
		}
		for _, pid := range toIntSlice(op["products"]) {
			if _, err := tx.ExecContext(ctx, `UPDATE products SET category_id=$2 WHERE id=$1`, pid, id); err != nil {
				return err
			}
		}
		for _, cid := range toIntSlice(op["children"]) {
			if _, err := tx.ExecContext(ctx, `UPDATE categories SET parent_id=$2 WHERE id=$1`, cid, id); err != nil {
				return err
			}
		}
		// Keep the id sequence ahead of any reused id.
		_, _ = tx.ExecContext(ctx, `SELECT setval(pg_get_serial_sequence('categories','id'), (SELECT max(id) FROM categories))`)
		return nil
	}
	return nil
}

// catLabel renders a category id for the preview (0 = top level).
func catLabel(m map[int64]string, id int64) string {
	if id == 0 {
		return "— top level —"
	}
	if n, ok := m[id]; ok {
		return n
	}
	return fmt.Sprintf("#%d", id)
}

func nameOr(m map[int64]string, id int64) string {
	if n, ok := m[id]; ok {
		return n
	}
	return fmt.Sprintf("#%d", id)
}

// JSON numbers decode as float64; these coerce them back to ints for SQL.
func toInt(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}

func toNullInt(v any) any {
	if v == nil {
		return nil
	}
	return toInt(v)
}

func toIntSlice(v any) []int64 {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]int64, 0, len(arr))
	for _, e := range arr {
		out = append(out, toInt(e))
	}
	return out
}
