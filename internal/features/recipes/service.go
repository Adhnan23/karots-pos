package recipes

import (
	"context"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
)

type Service struct {
	db   *sqlx.DB
	repo *Repository
}

func NewService(db *sqlx.DB) *Service { return &Service{db: db, repo: NewRepository(db)} }

func (s *Service) For(ctx context.Context, productID int64) ([]Component, error) {
	cs, err := s.repo.For(ctx, productID)
	if err != nil {
		return nil, apperr.Internal("failed to load recipe", err)
	}
	return cs, nil
}

// Replace validates and stores a product's whole recipe.
func (s *Service) Replace(ctx context.Context, productID int64, cs []Component) error {
	for _, c := range cs {
		if c.ComponentProductID == productID {
			return apperr.Validation("a product cannot consume itself")
		}
		qtySet := c.QtyPerUnit.Valid && c.QtyPerUnit.Decimal.IsPositive()
		yieldSet := c.YieldUnits.Valid && c.YieldUnits.Decimal.IsPositive()
		if qtySet == yieldSet {
			return apperr.Validation("each ingredient needs either a quantity per unit or a yield, not both")
		}
	}
	return appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		return s.repo.Replace(ctx, tx, productID, cs)
	})
}
