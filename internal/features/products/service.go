package products

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"math/big"
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

// GenerateBarcode mints a valid EAN-13 that no product currently uses. It uses
// the GS1 "restricted distribution" prefix 2 (reserved for in-store codes), so a
// generated value can never collide with a real manufacturer barcode. The DB
// uniqueness check guards against reissuing an existing/deactivated product's
// code; the products_barcode_key constraint remains the source of truth on save.
func (s *Service) GenerateBarcode(ctx context.Context) (string, error) {
	for range 20 {
		code, err := randomEAN13("2")
		if err != nil {
			return "", apperr.Internal("failed to generate barcode", err)
		}
		exists, err := s.repo.BarcodeExists(ctx, code)
		if err != nil {
			return "", apperr.Internal("failed to check barcode", err)
		}
		if !exists {
			return code, nil
		}
	}
	return "", apperr.Internal("could not generate a unique barcode; please try again", nil)
}

// randomEAN13 builds a 13-digit EAN-13 from the given leading prefix, filling the
// remaining data digits randomly and appending the EAN-13 check digit.
func randomEAN13(prefix string) (string, error) {
	digits := []byte(prefix)
	for len(digits) < 12 {
		n, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		digits = append(digits, byte('0'+n.Int64()))
	}
	return string(digits) + string(ean13CheckDigit(digits)), nil
}

// ean13CheckDigit computes the EAN-13 modulo-10 check digit for 12 data digits:
// odd positions weight 1, even positions weight 3 (1-indexed from the left).
func ean13CheckDigit(d12 []byte) byte {
	sum := 0
	for i, c := range d12 {
		n := int(c - '0')
		if i%2 == 1 {
			n *= 3
		}
		sum += n
	}
	return byte('0' + (10-(sum%10))%10)
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
		TrackSerial:    in.TrackSerial,
		WarrantyMonths: in.WarrantyMonths,
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
