package warranty

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/datetime"
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

// ReceiptDetail is a receipt's non-serial warranted lines, for the by-receipt
// claim view.
type ReceiptDetail struct {
	Sale  ReceiptSale   `json:"sale"`
	Lines []ReceiptLine `json:"lines"`
}

// underWarranty compares a pure warranty-until date against today in the shop's
// local zone (both as ISO date strings, so it is timezone-safe).
func underWarranty(until time.Time) bool {
	return until.UTC().Format("2006-01-02") >= datetime.Date(time.Now())
}

// LookupByReceipt finds a sale by receipt number and returns its non-serial
// warranted lines with derived cover/remaining info. Works on any past sale —
// nothing needed to be recorded at sale time.
func (s *Service) LookupByReceipt(ctx context.Context, receiptNo string) (*ReceiptDetail, error) {
	receiptNo = strings.TrimSpace(receiptNo)
	if receiptNo == "" {
		return nil, apperr.Validation("enter a receipt number to search")
	}
	sale, err := s.repo.FindSaleByReceipt(ctx, receiptNo)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("receipt")
		}
		return nil, apperr.Internal("failed to look up receipt", err)
	}
	lines, err := s.repo.NonSerialWarrantyLines(ctx, sale.SaleID)
	if err != nil {
		return nil, apperr.Internal("failed to load warranty lines", err)
	}
	for i := range lines {
		l := &lines[i]
		l.WarrantyUntil = Until(l.SoldAt, l.WarrantyMonths)
		l.UnderWarranty = underWarranty(l.WarrantyUntil)
		l.Remaining = l.Qty.Sub(decimal.NewFromInt(int64(l.Replaced)))
	}
	return &ReceiptDetail{Sale: *sale, Lines: lines}, nil
}

// RecordReceiptReplacement hands out a free warranty replacement for a NON-serial
// line identified by its sale + product (found via the receipt). It mirrors the
// serial path in one step: creates an on-demand unit for the faulty item, logs a
// claim, and ships one from stock as a warranty cost (FEFO) — feeding Losses &
// Recovery and supplier recovery exactly like a serial claim. The warranty
// CONTINUES (same expiry), and a line can't be over-claimed past the qty sold.
func (s *Service) RecordReceiptReplacement(ctx context.Context, saleID, productID int64, reason string, userID int64) (int64, error) {
	reason = strings.TrimSpace(reason)
	var claimID int64
	err := appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		repo := NewRepository(tx)
		stk := stock.NewRepository(tx)

		line, err := repo.NonSerialLine(ctx, saleID, productID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return apperr.Validation("no non-serial warranty item found for that receipt line")
			}
			return apperr.Internal("failed to load line", err)
		}
		until := Until(line.SoldAt, line.WarrantyMonths)
		if !underWarranty(until) {
			return apperr.Validation("this item is out of warranty")
		}
		if line.Qty.Sub(decimal.NewFromInt(int64(line.Replaced))).LessThan(decimal.NewFromInt(1)) {
			return apperr.Conflict("all units on this line have already been replaced")
		}

		// 1. On-demand unit for the faulty item (serial NULL), warranty continues.
		oldID, err := repo.InsertUnit(ctx, NewUnit{
			ProductID: productID, SerialNo: "", SaleID: &saleID, CustomerID: line.CustomerID,
			SoldAt: line.SoldAt, WarrantyMonths: line.WarrantyMonths, WarrantyUntil: until, Source: "sale",
		})
		if err != nil {
			return apperr.Internal("failed to create warranty unit", err)
		}

		// 2. The claim (no replacement serial — an identical item is handed over).
		var reasonPtr *string
		if reason != "" {
			reasonPtr = &reason
		}
		claimID, err = repo.InsertClaim(ctx, oldID, reasonPtr, "replaced", nil, userID)
		if err != nil {
			return apperr.Internal("failed to record claim", err)
		}
		if err := repo.MarkUnitReplacedNoUnit(ctx, oldID); err != nil {
			return apperr.Internal("failed to retire unit", err)
		}

		// 3. Ship one from stock as a warranty cost (not a sale).
		one := decimal.NewFromInt(1)
		ok, err := stk.DecrementGuarded(ctx, productID, one)
		if err != nil {
			return apperr.Internal("failed to update stock", err)
		}
		if !ok {
			return apperr.Conflict("no stock available to issue a replacement")
		}
		cost, err := stk.DepleteFEFO(ctx, productID, one)
		if err != nil {
			return apperr.Internal("failed to deplete batch", err)
		}
		refType := "warranty"
		note := "warranty replacement (by receipt)"
		if err := stk.InsertMovement(ctx, stock.MovementInput{
			ProductID:     productID,
			Type:          stock.MoveWarranty,
			Quantity:      one.Neg(),
			ReferenceID:   &claimID,
			ReferenceType: &refType,
			UserID:        userID,
			Note:          &note,
			Cost:          cost,
		}); err != nil {
			return apperr.Internal("failed to record stock movement", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return claimID, nil
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

// ListClaims returns recent warranty claims for the receipts/warranty tab.
func (s *Service) ListClaims(ctx context.Context, f ClaimFilter) ([]Claim, error) {
	rows, err := s.repo.ListClaims(ctx, f)
	if err != nil {
		return nil, apperr.Internal("failed to list warranty claims", err)
	}
	return rows, nil
}

// GetClaim loads one claim (for view / reprint).
func (s *Service) GetClaim(ctx context.Context, id int64) (*Claim, error) {
	cl, err := s.repo.GetClaim(ctx, id)
	if err != nil {
		return nil, apperr.NotFound("warranty claim")
	}
	return cl, nil
}

// GetUnit loads one warranty unit (for reprinting a replacement slip).
func (s *Service) GetUnit(ctx context.Context, id int64) (*Unit, error) {
	u, err := s.repo.FindUnitByID(ctx, id)
	if err != nil {
		return nil, apperr.NotFound("warranty unit")
	}
	return u, nil
}

// RecordReplacement replaces a faulty unit: it issues a NEW unit that CONTINUES
// the original warranty (same expiry date and cover — it does not restart), logs
// the claim, marks the old unit replaced, and ships the new unit out of stock
// (FEFO) — a cost, never revenue. All in one transaction.
func (s *Service) RecordReplacement(ctx context.Context, unitID int64, newSerial, reason string, userID int64) (*Unit, int64, error) {
	newSerial = strings.TrimSpace(newSerial)
	if newSerial == "" {
		return nil, 0, apperr.Validation("a new serial number is required")
	}
	reason = strings.TrimSpace(reason)

	var result *Unit
	var resultClaimID int64
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
		resultClaimID = claimID

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
		return nil, 0, err
	}
	return result, resultClaimID, nil
}
