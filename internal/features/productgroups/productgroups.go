package productgroups

import (
	"context"
	"strings"

	"karots-pos/internal/db"

	"github.com/shopspring/decimal"
)

// Group is one node in the cashier menu tree. HasChildren and ItemCount are
// derived (populated by Children/Tree/Get) to drive the admin view and to let
// the till decide whether a card drills into subgroups or shows products.
type Group struct {
	ID          int64   `db:"id"          json:"id"`
	Name        string  `db:"name"        json:"name"`
	Emoji       *string `db:"emoji"       json:"emoji"`
	ParentID    *int64  `db:"parent_id"   json:"parent_id"`
	SortOrder   int     `db:"sort_order"  json:"sort_order"`
	IsActive    bool    `db:"is_active"   json:"is_active"`
	HasChildren bool    `db:"has_children" json:"has_children"`
	ItemCount   int     `db:"item_count"  json:"item_count"`
}

// GroupProduct is a product linked into a group, with the per-link emoji and the
// same fields a till product card + addToCart need (so the card behaves exactly
// like a search result). JSON tags mirror products.Product.
type GroupProduct struct {
	ProductID        int64           `db:"product_id"         json:"id"`
	Name             string          `db:"name"               json:"name"`
	Barcode          *string         `db:"barcode"            json:"barcode,omitempty"`
	SellingPrice     decimal.Decimal `db:"selling_price"      json:"selling_price"`
	WholesalePrice   decimal.Decimal `db:"wholesale_price"    json:"wholesale_price"`
	TaxRate          decimal.Decimal `db:"tax_rate"           json:"tax_rate"`
	TrackSerial      bool            `db:"track_serial"       json:"track_serial"`
	WarrantyMonths   int             `db:"warranty_months"    json:"warranty_months"`
	UnitAbbr         string          `db:"unit_abbr"          json:"unit_abbr"`
	UnitAllowDecimal bool            `db:"unit_allow_decimal" json:"unit_allow_decimal"`
	StockQty         decimal.Decimal `db:"stock_qty"          json:"stock_qty"`
	Emoji            *string         `db:"emoji"              json:"emoji"`
	SortOrder        int             `db:"sort_order"         json:"sort_order"`
}

type CreateInput struct {
	Name     string
	Emoji    *string
	ParentID *int64
}

type UpdateInput struct {
	Name  string
	Emoji *string
}

type Repository struct{ db db.Queryer }

func NewRepository(q db.Queryer) *Repository { return &Repository{db: q} }

// groupSelect lists groups with derived child/item counts.
const groupSelect = `
	SELECT g.id, g.name, g.emoji, g.parent_id, g.sort_order, g.is_active,
	       EXISTS (SELECT 1 FROM product_groups c WHERE c.parent_id = g.id AND c.is_active) AS has_children,
	       (SELECT COUNT(*) FROM product_group_items i WHERE i.group_id = g.id)              AS item_count
	FROM product_groups g`

// Children returns active groups at one level: top-level when parentID is nil,
// otherwise the direct children of that group, stably ordered.
func (r *Repository) Children(ctx context.Context, parentID *int64) ([]Group, error) {
	var rows []Group
	err := r.db.SelectContext(ctx, &rows, groupSelect+`
		WHERE g.is_active
		  AND (($1::bigint IS NULL AND g.parent_id IS NULL) OR g.parent_id = $1)
		ORDER BY g.sort_order, g.name, g.id`, parentID)
	return rows, err
}

// Tree returns every active group (any level) for the admin page, stably ordered.
func (r *Repository) Tree(ctx context.Context) ([]Group, error) {
	var rows []Group
	err := r.db.SelectContext(ctx, &rows, groupSelect+`
		WHERE g.is_active
		ORDER BY g.sort_order, g.name, g.id`)
	return rows, err
}

func (r *Repository) Get(ctx context.Context, id int64) (*Group, error) {
	var g Group
	if err := r.db.GetContext(ctx, &g, groupSelect+` WHERE g.id = $1`, id); err != nil {
		return nil, err
	}
	return &g, nil
}

// Products returns the active products linked into a group, stably ordered.
func (r *Repository) Products(ctx context.Context, groupID int64) ([]GroupProduct, error) {
	var rows []GroupProduct
	err := r.db.SelectContext(ctx, &rows, `
		SELECT p.id AS product_id, p.name, p.barcode, p.selling_price, p.wholesale_price,
		       p.tax_rate, p.track_serial, p.warranty_months,
		       u.abbreviation AS unit_abbr, u.allow_decimal AS unit_allow_decimal,
		       COALESCE(s.quantity, 0) AS stock_qty,
		       i.emoji, i.sort_order
		FROM product_group_items i
		JOIN products p   ON p.id = i.product_id
		JOIN units u      ON u.id = p.unit_id
		LEFT JOIN stock s ON s.product_id = p.id
		WHERE i.group_id = $1 AND p.is_active
		ORDER BY i.sort_order, p.name, p.id`, groupID)
	return rows, err
}

func (r *Repository) Create(ctx context.Context, in CreateInput) (int64, error) {
	var id int64
	err := r.db.GetContext(ctx, &id, `
		INSERT INTO product_groups (name, emoji, parent_id, sort_order)
		VALUES ($1, $2, $3, COALESCE(
			(SELECT MAX(sort_order)+1 FROM product_groups
			 WHERE parent_id IS NOT DISTINCT FROM $3), 0))
		RETURNING id`, strings.TrimSpace(in.Name), in.Emoji, in.ParentID)
	return id, err
}

func (r *Repository) Update(ctx context.Context, id int64, in UpdateInput) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE product_groups SET name = $1, emoji = $2 WHERE id = $3`,
		strings.TrimSpace(in.Name), in.Emoji, id)
	return err
}

func (r *Repository) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM product_groups WHERE id = $1`, id)
	return err
}

func (r *Repository) LinkProduct(ctx context.Context, groupID, productID int64, emoji *string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO product_group_items (group_id, product_id, emoji, sort_order)
		VALUES ($1, $2, $3, COALESCE(
			(SELECT MAX(sort_order)+1 FROM product_group_items WHERE group_id = $1), 0))
		ON CONFLICT (group_id, product_id) DO UPDATE SET emoji = EXCLUDED.emoji`,
		groupID, productID, emoji)
	return err
}

func (r *Repository) UnlinkProduct(ctx context.Context, groupID, productID int64) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM product_group_items WHERE group_id = $1 AND product_id = $2`, groupID, productID)
	return err
}

func (r *Repository) SetItemEmoji(ctx context.Context, groupID, productID int64, emoji *string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE product_group_items SET emoji = $3 WHERE group_id = $1 AND product_id = $2`,
		groupID, productID, emoji)
	return err
}

// swapOrder moves group id one step up/down among its siblings by swapping
// sort_order with the adjacent sibling in the requested direction. A no-op when
// the group is already at the end in that direction.
func (r *Repository) swapOrder(ctx context.Context, id int64, dir string) error {
	op, ord := "<", "DESC"
	if dir == "down" {
		op, ord = ">", "ASC"
	}
	_, err := r.db.ExecContext(ctx, `
		WITH me AS (SELECT id, parent_id, sort_order FROM product_groups WHERE id = $1),
		neighbor AS (
			SELECT g.id, g.sort_order FROM product_groups g, me
			WHERE g.parent_id IS NOT DISTINCT FROM me.parent_id
			  AND g.sort_order `+op+` me.sort_order
			ORDER BY g.sort_order `+ord+` LIMIT 1),
		swap AS (
			UPDATE product_groups SET sort_order = (SELECT sort_order FROM me)
			WHERE id = (SELECT id FROM neighbor) RETURNING 1)
		UPDATE product_groups SET sort_order = (SELECT sort_order FROM neighbor)
		WHERE id = $1 AND EXISTS (SELECT 1 FROM neighbor)`, id)
	return err
}
