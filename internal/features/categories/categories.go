// Package categories manages the product category tree (self-referencing).
package categories

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/db"
)

type Category struct {
	ID         int64     `db:"id"         json:"id"`
	Name       string    `db:"name"       json:"name"`
	ParentID   *int64    `db:"parent_id"  json:"parent_id,omitempty"`
	ParentName *string   `db:"parent_name" json:"parent_name,omitempty"`
	CreatedAt  time.Time `db:"created_at" json:"created_at"`
}

type CreateInput struct {
	Name     string `json:"name"      form:"name"      validate:"required,min=1,max=80"`
	ParentID *int64 `json:"parent_id" form:"parent_id"`
}

type UpdateInput struct {
	Name     string `json:"name"      form:"name"      validate:"required,min=1,max=80"`
	ParentID *int64 `json:"parent_id" form:"parent_id"`
}

// --- repository ---

type Repository struct{ db db.Queryer }

func NewRepository(q db.Queryer) *Repository { return &Repository{db: q} }

func (r *Repository) List(ctx context.Context) ([]Category, error) {
	var rows []Category
	err := r.db.SelectContext(ctx, &rows, `
		SELECT c.id, c.name, c.parent_id, p.name AS parent_name, c.created_at
		FROM categories c
		LEFT JOIN categories p ON p.id = c.parent_id
		ORDER BY c.name`)
	return rows, err
}

func (r *Repository) FindByID(ctx context.Context, id int64) (*Category, error) {
	var c Category
	err := r.db.GetContext(ctx, &c, `
		SELECT c.id, c.name, c.parent_id, p.name AS parent_name, c.created_at
		FROM categories c
		LEFT JOIN categories p ON p.id = c.parent_id
		WHERE c.id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// FindByNameParent looks up a category by case-insensitive name within a parent
// (parentID nil = top level). Returns sql.ErrNoRows when absent.
func (r *Repository) FindByNameParent(ctx context.Context, name string, parentID *int64) (*Category, error) {
	var c Category
	var err error
	if parentID == nil {
		err = r.db.GetContext(ctx, &c,
			`SELECT id, name, parent_id, NULL::varchar AS parent_name, created_at
			 FROM categories WHERE lower(name) = lower($1) AND parent_id IS NULL`, name)
	} else {
		err = r.db.GetContext(ctx, &c,
			`SELECT id, name, parent_id, NULL::varchar AS parent_name, created_at
			 FROM categories WHERE lower(name) = lower($1) AND parent_id = $2`, name, *parentID)
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *Repository) Create(ctx context.Context, name string, parentID *int64) (*Category, error) {
	var c Category
	err := r.db.GetContext(ctx, &c,
		`INSERT INTO categories (name, parent_id) VALUES ($1, $2)
		 RETURNING id, name, parent_id, NULL::varchar AS parent_name, created_at`,
		name, parentID)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *Repository) Update(ctx context.Context, id int64, name string, parentID *int64) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE categories SET name = $1, parent_id = $2 WHERE id = $3`, name, parentID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM categories WHERE id = $1`, id)
	return err
}

// --- service ---

type Service struct{ repo *Repository }

func NewService(q db.Queryer) *Service { return &Service{repo: NewRepository(q)} }

func (s *Service) List(ctx context.Context) ([]Category, error) {
	rows, err := s.repo.List(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to list categories", err)
	}
	return rows, nil
}

// TreeNode is a category plus its position in the hierarchy, for indented and
// collapsible display. Path holds the ancestor IDs (root first, excluding self)
// so a collapsible UI can decide visibility without a DOM map; HasChildren drives
// the expand/collapse toggle.
type TreeNode struct {
	Category
	Depth       int
	HasChildren bool
	Path        []int64
}

// Tree returns all categories ordered depth-first (parents before their
// children), each tagged with its depth — used to render indented pickers.
func (s *Service) Tree(ctx context.Context) ([]TreeNode, error) {
	rows, err := s.repo.List(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to list categories", err)
	}
	children := map[int64][]Category{}
	var roots []Category
	for _, c := range rows {
		if c.ParentID == nil {
			roots = append(roots, c)
		} else {
			children[*c.ParentID] = append(children[*c.ParentID], c)
		}
	}
	var out []TreeNode
	var walk func(c Category, depth int, path []int64)
	walk = func(c Category, depth int, path []int64) {
		out = append(out, TreeNode{
			Category:    c,
			Depth:       depth,
			HasChildren: len(children[c.ID]) > 0,
			Path:        path,
		})
		// Copy so sibling branches don't share/overwrite the same backing array.
		childPath := append(append([]int64{}, path...), c.ID)
		for _, ch := range children[c.ID] {
			walk(ch, depth+1, childPath)
		}
	}
	for _, r := range roots {
		walk(r, 0, nil)
	}
	// Orphans (parent was deleted/SET NULL but still has a stale parent_id) — append.
	seen := map[int64]bool{}
	for _, n := range out {
		seen[n.ID] = true
	}
	for _, c := range rows {
		if !seen[c.ID] {
			out = append(out, TreeNode{Category: c, Depth: 0})
		}
	}
	return out, nil
}

// FindOrCreateByPath resolves a "Parent > Child > Grandchild" path to a leaf
// category ID, creating any missing levels. An empty path returns (0, nil) so the
// caller can fall back to its own default. Used by the bulk product importer.
func (s *Service) FindOrCreateByPath(ctx context.Context, path string) (int64, error) {
	var parentID *int64
	var leaf int64
	any := false
	for _, raw := range strings.Split(path, ">") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		any = true
		existing, err := s.repo.FindByNameParent(ctx, name, parentID)
		if err == nil {
			leaf = existing.ID
		} else if errors.Is(err, sql.ErrNoRows) {
			created, cerr := s.repo.Create(ctx, name, parentID)
			if cerr != nil {
				return 0, apperr.Internal("failed to create category "+name, cerr)
			}
			leaf = created.ID
		} else {
			return 0, apperr.Internal("failed to resolve category "+name, err)
		}
		id := leaf
		parentID = &id
	}
	if !any {
		return 0, nil
	}
	return leaf, nil
}

func (s *Service) Get(ctx context.Context, id int64) (*Category, error) {
	c, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("category")
		}
		return nil, apperr.Internal("failed to load category", err)
	}
	return c, nil
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*Category, error) {
	c, err := s.repo.Create(ctx, in.Name, in.ParentID)
	if err != nil {
		return nil, apperr.Internal("failed to create category", err)
	}
	return c, nil
}

func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) error {
	err := s.repo.Update(ctx, id, in.Name, in.ParentID)
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFound("category")
	}
	if err != nil {
		return apperr.Internal("failed to update category", err)
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return apperr.Conflict("category is in use and cannot be deleted")
	}
	return nil
}
