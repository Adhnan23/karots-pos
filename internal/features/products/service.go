package products

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/internal/money"

	"github.com/jmoiron/sqlx"
)

type Service struct {
	db   *sqlx.DB
	repo *Repository
}

func NewService(db *sqlx.DB) *Service {
	return &Service{db: db, repo: NewRepository(db)}
}

func (s *Service) List(ctx context.Context, q ListQuery) ([]Product, int, error) {
	q.Normalize()
	rows, err := s.repo.List(ctx, q)
	if err != nil {
		return nil, 0, apperr.Internal("failed to list products", err)
	}
	total, err := s.repo.Count(ctx, q)
	if err != nil {
		return nil, 0, apperr.Internal("failed to count products", err)
	}
	return rows, total, nil
}

func (s *Service) Get(ctx context.Context, id int64) (*Product, error) {
	p, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("product")
		}
		return nil, apperr.Internal("failed to load product", err)
	}
	return p, nil
}

// GetByBarcode powers the cashier scanner and price-check lookups.
func (s *Service) GetByBarcode(ctx context.Context, barcode string) (*Product, error) {
	p, err := s.repo.FindByBarcode(ctx, strings.TrimSpace(barcode))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("product")
		}
		return nil, apperr.Internal("failed to load product", err)
	}
	return p, nil
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*Product, error) {
	w, err := toWriteRow(in)
	if err != nil {
		return nil, err
	}
	id, err := s.repo.Insert(ctx, w)
	if err != nil {
		return nil, mapWriteErr(err)
	}
	return s.Get(ctx, id)
}

func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) (*Product, error) {
	w, err := toWriteRow(in)
	if err != nil {
		return nil, err
	}
	if err := s.repo.Update(ctx, id, w); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("product")
		}
		return nil, mapWriteErr(err)
	}
	return s.Get(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	if _, err := s.repo.FindByID(ctx, id); err != nil {
		return apperr.NotFound("product")
	}
	if err := s.repo.SoftDelete(ctx, id); err != nil {
		return apperr.Internal("failed to delete product", err)
	}
	return nil
}

func toWriteRow(in CreateInput) (writeRow, error) {
	cost, err := money.Parse(in.CostPrice)
	if err != nil {
		return writeRow{}, apperr.Validation("cost price is not a valid amount")
	}
	sell, err := money.Parse(in.SellingPrice)
	if err != nil {
		return writeRow{}, apperr.Validation("selling price is not a valid amount")
	}
	whole, err := money.Parse(in.WholesalePrice)
	if err != nil {
		return writeRow{}, apperr.Validation("wholesale price is not a valid amount")
	}
	tax, err := money.Parse(in.TaxRate)
	if err != nil {
		return writeRow{}, apperr.Validation("tax rate is not a valid number")
	}
	return writeRow{
		Name:       strings.TrimSpace(in.Name),
		NameSi:     nullStr(deref(in.NameSi)),
		Barcode:    nullStr(deref(in.Barcode)),
		CategoryID: in.CategoryID,
		UnitID:     in.UnitID,
		Cost:       cost,
		Selling:    sell,
		Wholesale:  whole,
		Tax:        tax,
		Reorder:    in.ReorderLevel,
	}, nil
}

func mapWriteErr(err error) error {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "products_barcode_key") || strings.Contains(msg, "barcode"):
		return apperr.Conflict("a product with that barcode already exists")
	case strings.Contains(msg, "foreign key") && strings.Contains(msg, "category"):
		return apperr.Validation("selected category does not exist")
	case strings.Contains(msg, "foreign key") && strings.Contains(msg, "unit"):
		return apperr.Validation("selected unit does not exist")
	default:
		return apperr.Internal("failed to save product", err)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
