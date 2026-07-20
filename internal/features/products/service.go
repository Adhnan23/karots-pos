package products

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"math/big"
	"strings"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/stock"
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

func (s *Service) List(ctx context.Context, q ListQuery) ([]Product, int, error) {
	q.Normalize()
	rows, total, err := s.listOnce(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	// Typo rescue: only when an exact-word search found nothing does it fall
	// back to fuzzy matching. Running fuzzy first would pad searches that
	// already work with near-misses the user did not ask for.
	if total == 0 && !q.Fuzzy && len(searchTokens(q.Search)) > 0 {
		q.Fuzzy = true
		return s.listOnce(ctx, q)
	}
	return rows, total, nil
}

func (s *Service) listOnce(ctx context.Context, q ListQuery) ([]Product, int, error) {
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


// ListAll returns the entire active catalog unpaginated, for CSV/spreadsheet
// export (List clamps Limit to 100 and would export only the first page).
func (s *Service) ListAll(ctx context.Context) ([]Product, error) {
	rows, err := s.repo.ListAll(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to list products", err)
	}
	return rows, nil
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

// QuickInput is a till-side quick-add: the cashier hit an item that isn't in the
// catalog and must still sell it. Only name + price are required; the barcode is
// optional (scanned, generated, or left for the admin).
type QuickInput struct {
	Name    string `json:"name"    form:"name"`
	Price   string `json:"price"   form:"price"`
	Qty     string `json:"qty"     form:"qty"`
	Barcode string `json:"barcode" form:"barcode"`
	UnitID  int64  `json:"unit_id" form:"unit_id"`
}

// QuickCreate makes a minimal, sellable product on the fly and seeds its stock to
// the quantity being sold, so the imminent sale nets it back to zero ("count
// later"). It is flagged needs_review and stamped with the cashier (created_by) so
// the admin can finish it (real category, unit, cost) from the review queue. The
// whole thing — product row, opening batch, stock bump and audit movement — runs
// in one transaction. cost_price is 0 (a placeholder corrected during review).
func (s *Service) QuickCreate(ctx context.Context, in QuickInput, userID int64) (*Product, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, apperr.Validation("item name is required")
	}
	price, err := money.Parse(in.Price)
	if err != nil || price.IsNegative() {
		return nil, apperr.Validation("price must be a non-negative amount")
	}
	qty, err := money.Parse(in.Qty)
	if err != nil || qty.LessThanOrEqual(decimal.Zero) {
		qty = decimal.NewFromInt(1)
	}

	var newID int64
	err = appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		catID, err := ensureUncategorized(ctx, tx)
		if err != nil {
			return apperr.Internal("failed to resolve category", err)
		}
		unitID := in.UnitID
		if unitID <= 0 {
			unitID, err = defaultUnitID(ctx, tx)
			if err != nil {
				return apperr.Internal("failed to resolve unit", err)
			}
		}
		repo := NewRepository(tx)
		id, err := repo.Insert(ctx, writeRow{
			Name:        name,
			Barcode:     nullStr(in.Barcode),
			CategoryID:  catID,
			UnitID:      unitID,
			Cost:        decimal.Zero,
			Selling:     price,
			Wholesale:   decimal.Zero,
			Tax:         decimal.Zero,
			NeedsReview: true,
			CreatedBy:   &userID,
		})
		if err != nil {
			return mapWriteErr(err)
		}
		// Seed stock = qty so the upcoming sale nets it to 0. A product-insert
		// trigger already created the stock row at 0; bump it and open a costing
		// batch + audit movement, mirroring a manual stock adjustment.
		stk := stock.NewRepository(tx)
		if err := stk.Increment(ctx, id, qty); err != nil {
			return apperr.Internal("failed to seed stock", err)
		}
		if _, err := stk.InsertBatch(ctx, stock.NewBatch{
			ProductID: id, Quantity: qty, CostPrice: decimal.Zero, Source: "opening",
		}); err != nil {
			return apperr.Internal("failed to open stock batch", err)
		}
		note := "quick-add opening (count pending)"
		if err := stk.InsertMovement(ctx, stock.MovementInput{
			ProductID: id, Type: stock.MoveAdjust, Quantity: qty, UserID: userID, Note: &note,
		}); err != nil {
			return apperr.Internal("failed to record stock movement", err)
		}
		newID = id
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, newID)
}

// NeedsReview lists the products awaiting admin review (quick-added at the till).
func (s *Service) NeedsReview(ctx context.Context) ([]Product, error) {
	rows, err := s.repo.ListNeedsReview(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to list items needing review", err)
	}
	return rows, nil
}

// CountNeedsReview powers the admin-panel badge.
func (s *Service) CountNeedsReview(ctx context.Context) (int, error) {
	n, err := s.repo.CountNeedsReview(ctx)
	if err != nil {
		return 0, apperr.Internal("failed to count items needing review", err)
	}
	return n, nil
}

// SetCost updates a product's cost price (stock-take opening-stock valuation).
func (s *Service) SetCost(ctx context.Context, id int64, cost decimal.Decimal) error {
	if cost.IsNegative() {
		return apperr.Validation("cost must be a non-negative amount")
	}
	if err := s.repo.SetCost(ctx, id, cost); err != nil {
		return apperr.Internal("failed to update cost", err)
	}
	return nil
}

// SetPrices updates a product's selling and wholesale prices (bulk set from the
// stock-take screen / count-sheet import). Both must be non-negative.
func (s *Service) SetPrices(ctx context.Context, id int64, selling, wholesale decimal.Decimal) error {
	if selling.IsNegative() || wholesale.IsNegative() {
		return apperr.Validation("prices must be non-negative amounts")
	}
	if err := s.repo.SetPrices(ctx, id, selling, wholesale); err != nil {
		return apperr.Internal("failed to update prices", err)
	}
	return nil
}

// AssignBarcode sets a barcode on a product that currently has none (the small
// "add barcode" action on barcode-less rows). The code must be non-empty and not
// already used by another product; it never overwrites an existing barcode.
func (s *Service) AssignBarcode(ctx context.Context, id int64, code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return apperr.Validation("enter or generate a barcode")
	}
	exists, err := s.repo.BarcodeExists(ctx, code)
	if err != nil {
		return apperr.Internal("failed to check barcode", err)
	}
	if exists {
		return apperr.Validation("that barcode is already used by another product")
	}
	ok, err := s.repo.SetBarcodeIfEmpty(ctx, id, code)
	if err != nil {
		return apperr.Internal("failed to save barcode", err)
	}
	if !ok {
		return apperr.Validation("this item already has a barcode")
	}
	return nil
}

// MarkReviewed clears the review flag once the admin has finished an item.
func (s *Service) MarkReviewed(ctx context.Context, id int64) error {
	if err := s.repo.ClearReview(ctx, id); err != nil {
		return apperr.Internal("failed to mark reviewed", err)
	}
	return nil
}

// BackfillCost corrects the placeholder cost 0 on a product's past sale lines once
// a real cost is known, so historical COGS/profit are accurate. Returns how many
// lines were corrected.
func (s *Service) BackfillCost(ctx context.Context, productID int64, costStr string) (int64, error) {
	cost, err := money.Parse(costStr)
	if err != nil || cost.IsNegative() {
		return 0, apperr.Validation("cost must be a non-negative amount")
	}
	n, err := s.repo.BackfillZeroCost(ctx, productID, cost)
	if err != nil {
		return 0, apperr.Internal("failed to backfill cost", err)
	}
	return n, nil
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
		NameLocal:  nullStr(deref(in.NameLocal)),
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
		IsService:         in.IsService,
		PreferredSupplier: nilIfZero(in.PreferredSupplierID),
	}, nil
}

// nilIfZero maps a 0 (or nil) supplier id to nil. The product form's "— none —"
// option posts an empty value, which the form binder turns into a *int64 of 0;
// stored as-is that 0 violates the preferred_supplier_id foreign key. Treating 0
// as "no supplier" keeps the field genuinely optional (create products first,
// attach a supplier later).
func nilIfZero(p *int64) *int64 {
	if p == nil || *p == 0 {
		return nil
	}
	return p
}

// ImportRow is one resolved row of a bulk catalog import. Category/unit/supplier
// are pre-resolved to IDs by the caller (the web import handler) so the products
// service doesn't reach across features. OpeningQty seeds stock at OpeningCost.
type ImportRow struct {
	Name              string
	NameLocal         string
	Barcode           string
	CategoryID        int64
	UnitID            int64
	UserID            int64
	PreferredSupplier *int64
	Cost              decimal.Decimal
	Selling           decimal.Decimal
	Wholesale         decimal.Decimal
	Tax               decimal.Decimal
	Reorder           int
	WarrantyMonths    int
	TrackSerial       bool
	OpeningQty        decimal.Decimal
}

// ImportResult reports what a single row did, for the import summary.
type ImportResult struct {
	Action string // "created" | "updated"
	Note   string // optional caveat, e.g. opening stock skipped
}

// ImportOne upserts one catalog row in a single transaction. It matches an
// existing product by barcode (when given) and updates its master fields;
// otherwise it inserts a new product. Opening stock is seeded — at the real
// cost — only for a brand-new product or one currently holding zero on-hand, so
// re-running the same import never double-counts stock.
func (s *Service) ImportOne(ctx context.Context, in ImportRow) (ImportResult, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return ImportResult{}, apperr.Validation("name is required")
	}
	w := writeRow{
		Name:              name,
		NameLocal:         nullStr(strings.TrimSpace(in.NameLocal)),
		Barcode:           nullStr(strings.TrimSpace(in.Barcode)),
		CategoryID:        in.CategoryID,
		UnitID:            in.UnitID,
		Cost:              in.Cost,
		Selling:           in.Selling,
		Wholesale:         in.Wholesale,
		Tax:               in.Tax,
		Reorder:           in.Reorder,
		TrackSerial:       in.TrackSerial,
		WarrantyMonths:    in.WarrantyMonths,
		PreferredSupplier: nilIfZero(in.PreferredSupplier),
	}
	var res ImportResult
	err := appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		repo := NewRepository(tx)
		// Match an existing active product: prefer barcode, else fall back to name
		// so barcode-less products round-trip on re-import instead of duplicating.
		var existing *Product
		if w.Barcode != nil {
			if p, ferr := repo.FindByBarcode(ctx, *w.Barcode); ferr == nil {
				existing = p
			} else if !errors.Is(ferr, sql.ErrNoRows) {
				return ferr
			}
		}
		if existing == nil {
			if p, ferr := repo.FindByName(ctx, name); ferr == nil {
				existing = p
			} else if !errors.Is(ferr, sql.ErrNoRows) {
				return ferr
			}
		}
		if existing != nil {
			if uerr := repo.Update(ctx, existing.ID, w); uerr != nil {
				return mapWriteErr(uerr)
			}
			res.Action = "updated"
			if in.OpeningQty.IsPositive() {
				if existing.StockQty.IsZero() {
					if serr := seedOpeningStock(ctx, tx, existing.ID, in.OpeningQty, in.Cost, in.UserID); serr != nil {
						return serr
					}
				} else {
					res.Note = "opening stock skipped (already in stock)"
				}
			}
			return nil
		}
		id, ierr := repo.Insert(ctx, w)
		if ierr != nil {
			return mapWriteErr(ierr)
		}
		res.Action = "created"
		if in.OpeningQty.IsPositive() {
			if serr := seedOpeningStock(ctx, tx, id, in.OpeningQty, in.Cost, in.UserID); serr != nil {
				return serr
			}
		}
		return nil
	})
	if err != nil {
		return ImportResult{}, err
	}
	return res, nil
}

// seedOpeningStock mirrors the till quick-add opening seed (service.go QuickCreate)
// but with a real cost: bump the cached quantity, open a costing batch, and log a
// stock-adjust movement — all within the caller's transaction.
func seedOpeningStock(ctx context.Context, tx *sqlx.Tx, productID int64, qty, cost decimal.Decimal, userID int64) error {
	stk := stock.NewRepository(tx)
	if err := stk.Increment(ctx, productID, qty); err != nil {
		return apperr.Internal("failed to seed opening stock", err)
	}
	if _, err := stk.InsertBatch(ctx, stock.NewBatch{
		ProductID: productID, Quantity: qty, CostPrice: cost, Source: "opening",
	}); err != nil {
		return apperr.Internal("failed to open stock batch", err)
	}
	note := "CSV import opening stock"
	if err := stk.InsertMovement(ctx, stock.MovementInput{
		ProductID: productID, Type: stock.MoveAdjust, Quantity: qty, UserID: userID, Note: &note,
	}); err != nil {
		return apperr.Internal("failed to record stock movement", err)
	}
	return nil
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

// ensureUncategorized returns the id of the "Uncategorized" category, creating it
// once if it doesn't exist. Quick-added items land here until an admin recategorizes
// them during review.
func ensureUncategorized(ctx context.Context, tx *sqlx.Tx) (int64, error) {
	var id int64
	err := tx.GetContext(ctx, &id, `SELECT id FROM categories WHERE name = 'Uncategorized' LIMIT 1`)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	err = tx.GetContext(ctx, &id,
		`INSERT INTO categories (name, parent_id) VALUES ('Uncategorized', NULL) RETURNING id`)
	return id, err
}

// defaultUnitID picks a sensible default unit for quick-added items — the seeded
// "Piece" (pcs) when present, otherwise the lowest-id unit.
func defaultUnitID(ctx context.Context, tx *sqlx.Tx) (int64, error) {
	var id int64
	err := tx.GetContext(ctx, &id,
		`SELECT id FROM units ORDER BY (abbreviation = 'pcs') DESC, id ASC LIMIT 1`)
	return id, err
}
