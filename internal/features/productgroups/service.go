package productgroups

import (
	"context"

	"karots-pos/internal/apperr"

	"github.com/jmoiron/sqlx"
)

type Service struct {
	db   *sqlx.DB
	repo *Repository
}

func NewService(db *sqlx.DB) *Service {
	return &Service{db: db, repo: NewRepository(db)}
}

func (s *Service) Children(ctx context.Context, parentID *int64) ([]Group, error) {
	rows, err := s.repo.Children(ctx, parentID)
	if err != nil {
		return nil, apperr.Internal("failed to list groups", err)
	}
	return rows, nil
}

func (s *Service) Tree(ctx context.Context) ([]Group, error) {
	rows, err := s.repo.Tree(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to load group tree", err)
	}
	return rows, nil
}

func (s *Service) Get(ctx context.Context, id int64) (*Group, error) {
	g, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, apperr.NotFound("group")
	}
	return g, nil
}

func (s *Service) Products(ctx context.Context, groupID int64) ([]GroupProduct, error) {
	rows, err := s.repo.Products(ctx, groupID)
	if err != nil {
		return nil, apperr.Internal("failed to load group products", err)
	}
	return rows, nil
}

// Breadcrumb returns the path from the root down to id (inclusive) for the till's
// Back/breadcrumb. Walks parent links; capped to avoid cycles.
func (s *Service) Breadcrumb(ctx context.Context, id int64) ([]Group, error) {
	var path []Group
	cur := &id
	for i := 0; cur != nil && i < 50; i++ {
		g, err := s.repo.Get(ctx, *cur)
		if err != nil {
			return nil, apperr.NotFound("group")
		}
		path = append([]Group{*g}, path...)
		cur = g.ParentID
	}
	return path, nil
}

func (s *Service) Create(ctx context.Context, in CreateInput) (int64, error) {
	if in.Name == "" {
		return 0, apperr.Validation("group name is required")
	}
	id, err := s.repo.Create(ctx, in)
	if err != nil {
		return 0, apperr.Internal("failed to create group", err)
	}
	return id, nil
}

func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) error {
	if in.Name == "" {
		return apperr.Validation("group name is required")
	}
	if err := s.repo.Update(ctx, id, in); err != nil {
		return apperr.Internal("failed to update group", err)
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return apperr.Internal("failed to delete group", err)
	}
	return nil
}

func (s *Service) LinkProduct(ctx context.Context, groupID, productID int64, emoji *string) error {
	if err := s.repo.LinkProduct(ctx, groupID, productID, emoji); err != nil {
		return apperr.Internal("failed to link product", err)
	}
	return nil
}

func (s *Service) UnlinkProduct(ctx context.Context, groupID, productID int64) error {
	if err := s.repo.UnlinkProduct(ctx, groupID, productID); err != nil {
		return apperr.Internal("failed to unlink product", err)
	}
	return nil
}

func (s *Service) SetItemEmoji(ctx context.Context, groupID, productID int64, emoji *string) error {
	if err := s.repo.SetItemEmoji(ctx, groupID, productID, emoji); err != nil {
		return apperr.Internal("failed to set emoji", err)
	}
	return nil
}

func (s *Service) Move(ctx context.Context, id int64, dir string) error {
	if dir != "up" && dir != "down" {
		return apperr.Validation("direction must be up or down")
	}
	if err := s.repo.swapOrder(ctx, id, dir); err != nil {
		return apperr.Internal("failed to reorder group", err)
	}
	return nil
}
