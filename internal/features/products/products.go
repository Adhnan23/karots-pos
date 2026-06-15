// Package products is the master catalog: CRUD plus barcode/search lookups used
// by both the admin panel and the cashier terminal.
package products

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"karots-pos/internal/db"

	"github.com/shopspring/decimal"
)

type Product struct {
	ID             int64           `db:"id"              json:"id"`
	Name           string          `db:"name"            json:"name"`
	NameSi         *string         `db:"name_si"         json:"name_si,omitempty"`
	Barcode        *string         `db:"barcode"         json:"barcode,omitempty"`
	CategoryID     int64           `db:"category_id"     json:"category_id"`
	UnitID         int64           `db:"unit_id"         json:"unit_id"`
	CostPrice      decimal.Decimal `db:"cost_price"      json:"cost_price"`
	SellingPrice   decimal.Decimal `db:"selling_price"   json:"selling_price"`
	WholesalePrice decimal.Decimal `db:"wholesale_price" json:"wholesale_price"`
	TaxRate        decimal.Decimal `db:"tax_rate"        json:"tax_rate"`
	ReorderLevel   int             `db:"reorder_level"   json:"reorder_level"`
	HasExpiry      bool            `db:"has_expiry"      json:"has_expiry"`
	TrackSerial    bool            `db:"track_serial"    json:"track_serial"`
	WarrantyMonths int             `db:"warranty_months" json:"warranty_months"`
	IsActive       bool            `db:"is_active"       json:"is_active"`
	NeedsReview    bool            `db:"needs_review"    json:"needs_review"`
	CreatedBy      *int64          `db:"created_by"      json:"created_by,omitempty"`
	CreatedAt      time.Time       `db:"created_at"      json:"created_at"`
	// Joined, read-only:
	CategoryName     string          `db:"category_name"      json:"category_name"`
	UnitAbbr         string          `db:"unit_abbr"          json:"unit_abbr"`
	UnitAllowDecimal bool            `db:"unit_allow_decimal" json:"unit_allow_decimal"`
	StockQty         decimal.Decimal `db:"stock_qty"          json:"stock_qty"`
	CreatedByName    *string         `db:"created_by_name"    json:"created_by_name,omitempty"`
}

// IsLowStock reports whether on-hand quantity is at or below the reorder level.
func (p Product) IsLowStock() bool {
	return p.StockQty.LessThanOrEqual(decimal.NewFromInt(int64(p.ReorderLevel)))
}

type CreateInput struct {
	Name           string  `json:"name"            form:"name"            validate:"required,min=1,max=150"`
	NameSi         *string `json:"name_si"         form:"name_si"`
	Barcode        *string `json:"barcode"         form:"barcode"         validate:"omitempty,max=50"`
	CategoryID     int64   `json:"category_id"     form:"category_id"     validate:"required,gt=0"`
	UnitID         int64   `json:"unit_id"         form:"unit_id"         validate:"required,gt=0"`
	CostPrice      string  `json:"cost_price"      form:"cost_price"`
	SellingPrice   string  `json:"selling_price"   form:"selling_price"`
	WholesalePrice string  `json:"wholesale_price" form:"wholesale_price"`
	TaxRate        string  `json:"tax_rate"        form:"tax_rate"`
	ReorderLevel   int     `json:"reorder_level"   form:"reorder_level"   validate:"gte=0"`
	TrackSerial    bool    `json:"track_serial"    form:"track_serial"`
	WarrantyMonths int     `json:"warranty_months" form:"warranty_months" validate:"gte=0,lte=600"`
}

type UpdateInput = CreateInput

type ListQuery struct {
	Page       int    `query:"page"        form:"page"`
	Limit      int    `query:"limit"       form:"limit"`
	CategoryID *int64 `query:"category_id" form:"category_id"`
	Search     string `query:"search"      form:"search"`
	LowStock   bool   `query:"low_stock"   form:"low_stock"`
}

// Normalize applies sane pagination defaults/bounds.
func (q *ListQuery) Normalize() {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.Limit < 1 || q.Limit > 100 {
		q.Limit = 50
	}
}

func (q ListQuery) offset() int { return (q.Page - 1) * q.Limit }

// --- repository ---

const selectProduct = `
	SELECT p.*, c.name AS category_name, u.abbreviation AS unit_abbr,
	       u.allow_decimal AS unit_allow_decimal,
	       COALESCE(s.quantity, 0) AS stock_qty
	FROM products p
	JOIN categories c ON c.id = p.category_id
	JOIN units u      ON u.id = p.unit_id
	LEFT JOIN stock s ON s.product_id = p.id`

type Repository struct{ db db.Queryer }

func NewRepository(q db.Queryer) *Repository { return &Repository{db: q} }

// subcatsCTE expands a selected category to itself + all descendants so that
// filtering by a parent category also returns products in its sub-categories.
const subcatsCTE = `
	WITH RECURSIVE subcats AS (
		SELECT id FROM categories WHERE $2::bigint IS NOT NULL AND id = $2
		UNION ALL
		SELECT c.id FROM categories c JOIN subcats sc ON c.parent_id = sc.id
	)`

func (r *Repository) List(ctx context.Context, q ListQuery) ([]Product, error) {
	var rows []Product
	err := r.db.SelectContext(ctx, &rows, subcatsCTE+selectProduct+`
		WHERE p.is_active = true
		  AND ($1::text   IS NULL OR p.name ILIKE '%' || $1 || '%' OR p.barcode = $1)
		  AND ($2::bigint IS NULL OR p.category_id IN (SELECT id FROM subcats))
		  AND ($3 = false OR COALESCE(s.quantity,0) <= p.reorder_level)
		ORDER BY p.name
		LIMIT $4 OFFSET $5`,
		nullStr(q.Search), q.CategoryID, q.LowStock, q.Limit, q.offset())
	return rows, err
}

func (r *Repository) Count(ctx context.Context, q ListQuery) (int, error) {
	var n int
	err := r.db.GetContext(ctx, &n, subcatsCTE+`
		SELECT COUNT(*) FROM products p
		LEFT JOIN stock s ON s.product_id = p.id
		WHERE p.is_active = true
		  AND ($1::text   IS NULL OR p.name ILIKE '%' || $1 || '%' OR p.barcode = $1)
		  AND ($2::bigint IS NULL OR p.category_id IN (SELECT id FROM subcats))
		  AND ($3 = false OR COALESCE(s.quantity,0) <= p.reorder_level)`,
		nullStr(q.Search), q.CategoryID, q.LowStock)
	return n, err
}

func (r *Repository) FindByID(ctx context.Context, id int64) (*Product, error) {
	var p Product
	err := r.db.GetContext(ctx, &p, selectProduct+` WHERE p.id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *Repository) FindByBarcode(ctx context.Context, barcode string) (*Product, error) {
	var p Product
	err := r.db.GetContext(ctx, &p,
		selectProduct+` WHERE p.barcode = $1 AND p.is_active = true`, barcode)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// BarcodeExists reports whether any product (active or not) already carries this
// barcode, so a generated code never shadows an existing or deactivated product.
func (r *Repository) BarcodeExists(ctx context.Context, code string) (bool, error) {
	var exists bool
	err := r.db.GetContext(ctx, &exists,
		`SELECT EXISTS(SELECT 1 FROM products WHERE barcode = $1)`, code)
	return exists, err
}

type writeRow struct {
	Name                          string
	NameSi, Barcode               *string
	CategoryID, UnitID            int64
	Cost, Selling, Wholesale, Tax decimal.Decimal
	Reorder                       int
	TrackSerial                   bool
	WarrantyMonths                int
	NeedsReview                   bool   // set by the till quick-add; false for normal creates
	CreatedBy                     *int64 // the user who quick-added it (nil otherwise)
}

func (r *Repository) Insert(ctx context.Context, w writeRow) (int64, error) {
	var id int64
	err := r.db.GetContext(ctx, &id, `
		INSERT INTO products
			(name, name_si, barcode, category_id, unit_id,
			 cost_price, selling_price, wholesale_price, tax_rate, reorder_level,
			 track_serial, warranty_months, needs_review, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING id`,
		w.Name, w.NameSi, w.Barcode, w.CategoryID, w.UnitID,
		w.Cost, w.Selling, w.Wholesale, w.Tax, w.Reorder,
		w.TrackSerial, w.WarrantyMonths, w.NeedsReview, w.CreatedBy)
	return id, err
}

func (r *Repository) Update(ctx context.Context, id int64, w writeRow) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE products SET
			name=$1, name_si=$2, barcode=$3, category_id=$4, unit_id=$5,
			cost_price=$6, selling_price=$7, wholesale_price=$8, tax_rate=$9, reorder_level=$10,
			track_serial=$11, warranty_months=$12
		WHERE id=$13 AND is_active = true`,
		w.Name, w.NameSi, w.Barcode, w.CategoryID, w.UnitID,
		w.Cost, w.Selling, w.Wholesale, w.Tax, w.Reorder,
		w.TrackSerial, w.WarrantyMonths, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) SoftDelete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE products SET is_active = false WHERE id = $1`, id)
	return err
}

// SetCost updates only a product's cost price (used by the stock-take screen when
// the admin enters the cost of opening stock).
func (r *Repository) SetCost(ctx context.Context, id int64, cost decimal.Decimal) error {
	_, err := r.db.ExecContext(ctx, `UPDATE products SET cost_price = $1 WHERE id = $2`, cost, id)
	return err
}

// CountNeedsReview is the number of active products still flagged for review
// (quick-added at the till). Powers the admin-panel badge.
func (r *Repository) CountNeedsReview(ctx context.Context) (int, error) {
	var n int
	err := r.db.GetContext(ctx, &n,
		`SELECT COUNT(*) FROM products WHERE needs_review = true AND is_active = true`)
	return n, err
}

// ListNeedsReview returns the products awaiting review, newest first, with the
// name of the user who quick-added each one.
func (r *Repository) ListNeedsReview(ctx context.Context) ([]Product, error) {
	var rows []Product
	err := r.db.SelectContext(ctx, &rows, `
		SELECT p.*, c.name AS category_name, u.abbreviation AS unit_abbr,
		       u.allow_decimal AS unit_allow_decimal,
		       COALESCE(s.quantity, 0) AS stock_qty,
		       cb.name AS created_by_name
		FROM products p
		JOIN categories c ON c.id = p.category_id
		JOIN units u      ON u.id = p.unit_id
		LEFT JOIN stock s ON s.product_id = p.id
		LEFT JOIN users cb ON cb.id = p.created_by
		WHERE p.is_active = true AND p.needs_review = true
		ORDER BY p.created_at DESC`)
	return rows, err
}

// ClearReview marks a product as reviewed (admin has finished its setup).
func (r *Repository) ClearReview(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE products SET needs_review = false WHERE id = $1`, id)
	return err
}

// BackfillZeroCost sets the cost on this product's sale lines that were recorded
// with the placeholder cost 0 (quick-add). This corrects COGS/profit on those
// past sales once the admin enters the real cost during review. Returns the
// number of sale lines corrected.
func (r *Repository) BackfillZeroCost(ctx context.Context, productID int64, cost decimal.Decimal) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`UPDATE sale_items SET cost_price = $1 WHERE product_id = $2 AND cost_price = 0`,
		cost, productID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func nullStr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
