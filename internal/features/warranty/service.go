package warranty

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/stock"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// Until is the warranty expiry date for a unit sold (or replaced) at soldAt with
// a cover of months. It works on the date only so the boundary is timezone-safe.
func Until(soldAt time.Time, months int) time.Time {
	d := soldAt.UTC()
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, months, 0)
}

type Service struct {
	db   *sqlx.DB
	repo *Repository
}

func NewService(db *sqlx.DB) *Service { return &Service{db: db, repo: NewRepository(db)} }

// Detail bundles a unit with its claim history for the lookup view.
type Detail struct {
	Unit   Unit    `json:"unit"`
	Claims []Claim `json:"claims"`
}

// Lookup finds a unit by serial number along with its claim history.
func (s *Service) Lookup(ctx context.Context, serial string) (*Detail, error) {
	serial = strings.TrimSpace(serial)
	if serial == "" {
		return nil, apperr.Validation("enter a serial number to search")
	}
	unit, err := s.repo.FindUnitBySerial(ctx, serial)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("warranty serial")
		}
		return nil, apperr.Internal("failed to look up serial", err)
	}
	claims, err := s.repo.ClaimsForUnit(ctx, unit.ID)
	if err != nil {
		return nil, apperr.Internal("failed to load claim history", err)
	}
	return &Detail{Unit: *unit, Claims: claims}, nil
}

// List returns warranty units for the overview table.
func (s *Service) List(ctx context.Context, status, search string) ([]Unit, error) {
	rows, err := s.repo.ListUnits(ctx, status, search, 100)
	if err != nil {
		return nil, apperr.Internal("failed to list warranty units", err)
	}
	return rows, nil
}

// UnitsForSale returns the serials recorded on a sale (for the printed receipt).
func (s *Service) UnitsForSale(ctx context.Context, saleID int64) ([]Unit, error) {
	return s.repo.UnitsForSale(ctx, saleID)
}

// RecordReplacement replaces a faulty unit: it issues a NEW unit that CONTINUES
// the original warranty (same expiry date and cover — it does not restart), logs
// the claim, marks the old unit replaced, and ships the new unit out of stock
// (FEFO) — a cost, never revenue. All in one transaction.
func (s *Service) RecordReplacement(ctx context.Context, unitID int64, newSerial, reason string, userID int64) (*Unit, error) {
	newSerial = strings.TrimSpace(newSerial)
	if newSerial == "" {
		return nil, apperr.Validation("a new serial number is required")
	}
	reason = strings.TrimSpace(reason)

	var result *Unit
	err := appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		repo := NewRepository(tx)
		stk := stock.NewRepository(tx)

		old, err := repo.FindUnitByID(ctx, unitID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return apperr.NotFound("warranty unit")
			}
			return apperr.Internal("failed to load unit", err)
		}
		if old.Status == "replaced" {
			return apperr.Conflict("this unit has already been replaced")
		}

		// Reject a duplicate serial up front with a clear message.
		exists, err := repo.SerialExists(ctx, newSerial)
		if err != nil {
			return apperr.Internal("failed to check serial", err)
		}
		if exists {
			return apperr.Validation("that serial number is already on record")
		}

		now := time.Now()

		// 1. The new unit — the warranty CONTINUES from the original unit (same
		// expiry date and cover), not a fresh term. Same customer.
		newID, err := repo.InsertUnit(ctx, NewUnit{
			ProductID:      old.ProductID,
			SerialNo:       newSerial,
			CustomerID:     old.CustomerID,
			SoldAt:         now,
			WarrantyMonths: old.WarrantyMonths,
			WarrantyUntil:  old.WarrantyUntil,
			Source:         "replacement",
		})
		if err != nil {
			return apperr.Internal("failed to create replacement unit", err)
		}

		// 2. The claim record, pointing at the new unit.
		var reasonPtr *string
		if reason != "" {
			reasonPtr = &reason
		}
		claimID, err := repo.InsertClaim(ctx, unitID, reasonPtr, "replaced", &newID, userID)
		if err != nil {
			return apperr.Internal("failed to record claim", err)
		}

		// 3. Retire the old unit.
		if err := repo.MarkUnitReplaced(ctx, unitID, newID); err != nil {
			return apperr.Internal("failed to retire old unit", err)
		}

		// 4. Ship the new unit out of stock (a warranty cost, not a sale).
		one := decimal.NewFromInt(1)
		ok, err := stk.DecrementGuarded(ctx, old.ProductID, one)
		if err != nil {
			return apperr.Internal("failed to update stock", err)
		}
		if !ok {
			return apperr.Conflict("no stock available to issue a replacement")
		}
		cost, err := stk.DepleteFEFO(ctx, old.ProductID, one)
		if err != nil {
			return apperr.Internal("failed to deplete batch", err)
		}
		refType := "warranty"
		note := "warranty replacement: " + newSerial
		if err := stk.InsertMovement(ctx, stock.MovementInput{
			ProductID:     old.ProductID,
			Type:          stock.MoveWarranty,
			Quantity:      one.Neg(),
			ReferenceID:   &claimID,
			ReferenceType: &refType,
			UserID:        userID,
			Note:          &note,
			Cost:          cost, // worth of the unit handed out (qty 1)
		}); err != nil {
			return apperr.Internal("failed to record stock movement", err)
		}

		nu, err := repo.FindUnitByID(ctx, newID)
		if err != nil {
			return apperr.Internal("failed to load replacement unit", err)
		}
		result = nu
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
