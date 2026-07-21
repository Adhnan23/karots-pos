package recipes

import (
	"context"
	"strings"

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

// CostsFor returns a product's non-stock cost lines.
func (s *Service) CostsFor(ctx context.Context, productID int64) ([]CostLine, error) {
	ls, err := s.repo.CostsFor(ctx, productID)
	if err != nil {
		return nil, apperr.Internal("failed to load recipe costs", err)
	}
	return ls, nil
}

// Summaries returns the per-unit cost split keyed by service product id.
func (s *Service) Summaries(ctx context.Context) (map[int64]Costs, error) {
	m, err := s.repo.Summaries(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to summarise recipes", err)
	}
	return m, nil
}

// Replace validates and stores a product's whole recipe: its stock components
// and its non-stock cost lines, in one transaction so a half-saved recipe can
// never be sold from.
func (s *Service) Replace(ctx context.Context, productID int64, cs []Component, ls []CostLine) error {
	seen := make(map[string]bool, len(ls))
	for i, l := range ls {
		label := strings.TrimSpace(l.Label)
		if label == "" {
			return apperr.Validation("each cost line needs a name")
		}
		if l.CostPerUnit.IsNegative() {
			return apperr.Validation("a cost line cannot be negative")
		}
		if seen[strings.ToLower(label)] {
			return apperr.Validation("two cost lines cannot share the name " + label)
		}
		seen[strings.ToLower(label)] = true
		ls[i].Label = label
	}
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
		if err := s.repo.Replace(ctx, tx, productID, cs); err != nil {
			return err
		}
		return s.repo.ReplaceCosts(ctx, tx, productID, ls)
	})
}

// Counts returns ingredient counts keyed by service product id.
func (s *Service) Counts(ctx context.Context) (map[int64]int, error) {
	m, err := s.repo.Counts(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to count recipes", err)
	}
	return m, nil
}
