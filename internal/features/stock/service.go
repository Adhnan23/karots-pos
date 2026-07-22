package stock

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/money"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

type Service struct {
	db   *sqlx.DB
	repo *Repository
}

func NewService(db *sqlx.DB) *Service {
	return &Service{db: db, repo: NewRepository(db)}
}

type AdjustInput struct {
	ProductID   int64  `json:"product_id"  form:"product_id"  validate:"required,gt=0"`
	NewQuantity string `json:"new_quantity" form:"new_quantity" validate:"required"`
	Note        string `json:"note"        form:"note"`
	// SellingPrice optionally prices the lot this adjustment opens (stock intake
	// entering a restock at a new price). Blank leaves the lot following the
	// product, which is what every caller did before per-lot pricing. Ignored
	// when the adjustment removes stock — there is no new lot to price.
	SellingPrice string `json:"selling_price" form:"selling_price"`
	// BatchID names the lot to take from when the adjustment REDUCES stock.
	// Without it the shortfall comes off the oldest lot, which is a guess: the
	// units that actually went missing may have been from the newest delivery,
	// and once lots carry different prices that guess leaves the books offering a
	// price for stock the shop no longer has. Zero keeps the old FEFO behaviour.
	BatchID int64 `json:"batch_id" form:"batch_id"`
}

// Adjust sets a product's stock to an absolute quantity, recording the signed
// delta as an 'adjust' movement. The read-modify-write runs in one transaction.
func (s *Service) Adjust(ctx context.Context, in AdjustInput, userID int64) error {
	target, err := money.Parse(in.NewQuantity)
	if err != nil || target.IsNegative() {
		return apperr.Validation("new quantity must be a non-negative number")
	}
	return appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		repo := NewRepository(tx)
		current, err := repo.GetQuantity(ctx, in.ProductID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return apperr.NotFound("product")
			}
			return apperr.Internal("failed to read stock", err)
		}
		delta := target.Sub(current)
		if delta.IsZero() {
			return nil
		}
		if err := repo.SetQuantity(ctx, in.ProductID, target); err != nil {
			return apperr.Internal("failed to update stock", err)
		}
		// Keep batches consistent: a positive delta opens an adjustment batch,
		// a negative delta is depleted FEFO.
		if delta.IsPositive() {
			cost := decimal.Zero
			if p, err := repo.productCost(ctx, in.ProductID); err == nil {
				cost = p
			}
			// A blank/unparseable price is simply no price: the lot follows the
			// product, exactly as before.
			sell := decimal.Zero
			if s := strings.TrimSpace(in.SellingPrice); s != "" {
				if v, perr := money.Parse(s); perr == nil && v.IsPositive() {
					sell = v
				}
			}
			if _, err := repo.InsertBatch(ctx, NewBatch{
				ProductID: in.ProductID, Quantity: delta, CostPrice: cost,
				SellingPrice: sell, Source: "adjust",
			}); err != nil {
				return apperr.Internal("failed to open adjustment batch", err)
			}
		} else {
			if _, err := repo.depleteChosen(ctx, in.ProductID, in.BatchID, delta.Abs()); err != nil {
				return err
			}
		}
		note := nilIfEmpty(in.Note)
		if err := repo.InsertMovement(ctx, MovementInput{
			ProductID: in.ProductID,
			Type:      MoveAdjust,
			Quantity:  delta,
			UserID:    userID,
			Note:      note,
		}); err != nil {
			return apperr.Internal("failed to record movement", err)
		}
		return nil
	})
}

// Quantity returns a product's current on-hand quantity (used by the stock-take
// screen to detect which rows actually changed).
func (s *Service) Quantity(ctx context.Context, productID int64) (decimal.Decimal, error) {
	return s.repo.GetQuantity(ctx, productID)
}

type DamageInput struct {
	ProductID int64  `json:"product_id" form:"product_id" validate:"required,gt=0"`
	Quantity  string `json:"quantity"   form:"quantity"   validate:"required"`
	Note      string `json:"note"       form:"note"`
	// BatchID names the lot being written off; zero falls back to FEFO.
	BatchID int64 `json:"batch_id" form:"batch_id"`
}

// ConsumeInput records stock leaving for a non-sale reason.
type ConsumeInput struct {
	ProductID int64  `json:"product_id" form:"product_id" validate:"required,gt=0"`
	Quantity  string `json:"quantity"   form:"quantity"   validate:"required"`
	Reason    string `json:"reason"     form:"reason"`
	Note      string `json:"note"       form:"note"`
	// BatchID names the lot physically being written off / taken. Zero falls back
	// to FEFO. Which lot it is decides the cost booked against the loss as well as
	// what the shop is left holding, so guessing shows up in both the P&L and the
	// price the till offers.
	BatchID int64 `json:"batch_id" form:"batch_id"`
}

// consumeReasons maps a UI reason to its movement type. Anything not listed is
// rejected rather than defaulting, so a typo can never silently book stock to
// the wrong P&L line.
var consumeReasons = map[string]string{
	"damage":  MoveDamage,
	"own_use": MoveOwnUse,
	"staff":   MoveStaff,
}

// Damage writes off stock (spoilage, breakage). It decrements under the same
// guard as a sale so quantity can never go negative, and audits the loss.
func (s *Service) Damage(ctx context.Context, in DamageInput, userID int64) error {
	return s.Consume(ctx, ConsumeInput{
		ProductID: in.ProductID,
		Quantity:  in.Quantity,
		Reason:    "damage",
		Note:      in.Note,
		BatchID:   in.BatchID,
	}, userID)
}

// Consume removes stock for a deliberate, non-sale reason and books its FEFO
// cost against the movement, so the P&L can report it. Damage, shop own-use and
// staff consumption all take this one path: they differ only in which line the
// cost lands on, never in how stock and cost are computed.
func (s *Service) Consume(ctx context.Context, in ConsumeInput, userID int64) error {
	mtype, ok := consumeReasons[in.Reason]
	if !ok {
		return apperr.Validation("unknown reason for removing stock")
	}
	qty, err := money.Parse(in.Quantity)
	if err != nil || !qty.IsPositive() {
		return apperr.Validation("quantity must be greater than zero")
	}
	return appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		repo := NewRepository(tx)
		ok, err := repo.DecrementGuarded(ctx, in.ProductID, qty)
		if err != nil {
			return apperr.Internal("failed to update stock", err)
		}
		if !ok {
			return apperr.Conflict("not enough stock")
		}
		cost, err := repo.depleteChosen(ctx, in.ProductID, in.BatchID, qty)
		if err != nil {
			return err
		}
		return repo.InsertMovement(ctx, MovementInput{
			ProductID: in.ProductID,
			Type:      mtype,
			Quantity:  qty.Neg(),
			UserID:    userID,
			Note:      nilIfEmpty(in.Note),
			Cost:      cost.Mul(qty), // total worth consumed
		})
	})
}

// Batches lists the live lots for a product (admin drill-down).
func (s *Service) Batches(ctx context.Context, productID int64) ([]Batch, error) {
	rows, err := s.repo.ListBatches(ctx, productID)
	if err != nil {
		return nil, apperr.Internal("failed to load batches", err)
	}
	return rows, nil
}

// MultiPriceProducts backs the till's "which price?" prompt: only products whose
// live lots disagree on price, with their options.
func (s *Service) MultiPriceProducts(ctx context.Context) (map[int64][]PriceOption, error) {
	rows, err := s.repo.MultiPriceProducts(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to load batch prices", err)
	}
	return rows, nil
}

// SetBatchPrice re-prices one lot from the admin batch list; zero puts it back on
// the product's current price.
func (s *Service) SetBatchPrice(ctx context.Context, batchID int64, price decimal.Decimal) error {
	if price.IsNegative() {
		return apperr.Validation("price cannot be negative")
	}
	if err := s.repo.SetBatchSellingPrice(ctx, batchID, price); err != nil {
		return apperr.Internal("failed to update batch price", err)
	}
	return nil
}

// AllBatches lists every batch that still has stock (the batch report).
func (s *Service) AllBatches(ctx context.Context) ([]Batch, error) {
	rows, err := s.repo.AllLiveBatches(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to load batches", err)
	}
	return rows, nil
}

// Expiring returns live batches expiring on/before now+days (days<=0 → only
// already-expired). Backs the expiry report + dashboard alert.
func (s *Service) Expiring(ctx context.Context, days int) ([]Batch, error) {
	cutoff := time.Now().AddDate(0, 0, days)
	rows, err := s.repo.ExpiringBefore(ctx, cutoff)
	if err != nil {
		return nil, apperr.Internal("failed to load expiring stock", err)
	}
	return rows, nil
}

func (s *Service) Movements(ctx context.Context, productID *int64, mtype string, limit int) ([]Movement, error) {
	rows, err := s.repo.ListMovements(ctx, productID, mtype, limit)
	if err != nil {
		return nil, apperr.Internal("failed to load movements", err)
	}
	return rows, nil
}

// FindMovements returns one page of the filtered audit trail plus the total
// number of matching movements. Pass Limit 0 for every match (CSV export).
func (s *Service) FindMovements(ctx context.Context, f MovementFilter) ([]Movement, int, error) {
	total, err := s.repo.CountMovements(ctx, f)
	if err != nil {
		return nil, 0, apperr.Internal("failed to count movements", err)
	}
	rows, err := s.repo.FindMovements(ctx, f)
	if err != nil {
		return nil, 0, apperr.Internal("failed to load movements", err)
	}
	return rows, total, nil
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
