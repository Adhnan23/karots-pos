package clearance

import (
	"context"
	"time"

	"karots-pos/internal/plugin"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

type Store struct{ db *sqlx.DB }

func NewStore(db *sqlx.DB) *Store { return &Store{db: db} }

type Settings struct {
	StaleDays        int             `db:"stale_days"`
	DefaultPercent   decimal.Decimal `db:"default_percent"`
	MinMarginPercent decimal.Decimal `db:"min_margin_percent"`
	ExpiryDays       int             `db:"expiry_days"` // flag lots expiring within this window
}

func (s *Store) GetSettings(ctx context.Context) (Settings, error) {
	var out Settings
	err := s.db.GetContext(ctx, &out,
		`SELECT stale_days, default_percent, min_margin_percent, expiry_days FROM clearance_settings WHERE id = 1`)
	return out, err
}

func (s *Store) SaveSettings(ctx context.Context, in Settings) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE clearance_settings SET stale_days=$1, default_percent=$2, min_margin_percent=$3, expiry_days=$4 WHERE id = 1`,
		in.StaleDays, in.DefaultPercent, in.MinMarginPercent, in.ExpiryDays)
	return err
}

// suggestPercent returns the discount % to suggest: defaultPct, but never so
// much that the new price falls below cost*(1+minMargin/100). Returns 0 when the
// price is already at or under that floor (nothing safe to give).
func suggestPercent(sell, cost, defaultPct, minMarginPct decimal.Decimal) decimal.Decimal {
	hundred := decimal.NewFromInt(100)
	if !sell.IsPositive() {
		return decimal.Zero
	}
	floor := cost.Mul(hundred.Add(minMarginPct)).Div(hundred) // cost*(1+m/100)
	// max % that still keeps price >= floor: (1 - floor/sell) * 100
	maxPct := hundred.Sub(floor.Div(sell).Mul(hundred))
	if maxPct.IsNegative() {
		maxPct = decimal.Zero
	}
	if defaultPct.LessThan(maxPct) {
		return defaultPct
	}
	return maxPct
}

// newPrice applies a percent off a selling price, rounded to 2dp.
func newPrice(sell, pct decimal.Decimal) decimal.Decimal {
	hundred := decimal.NewFromInt(100)
	return sell.Mul(hundred.Sub(pct)).Div(hundred).Round(2)
}

type StaleItem struct {
	ProductID     int64            `db:"product_id"`
	Name          string           `db:"name"`
	Unit          string           `db:"unit_abbr"`
	OnHand        decimal.Decimal  `db:"stock_qty"`
	Cost          decimal.Decimal  `db:"cost_price"`
	Price         decimal.Decimal  `db:"selling_price"`
	DaysSinceSale *int             `db:"days_since_sale"`  // nil = never sold
	SoonestExpiry *time.Time       `db:"soonest_expiry"`   // soonest FUTURE lot expiry, nil = none
	Status        *string          `db:"status"`           // approved | dismissed | nil (candidate)
	MarkdownType  *string          `db:"discount_type"`
	MarkdownValue *decimal.Decimal `db:"discount_value"`
}

// StaleItems lists clearance candidates: products with stock that either haven't
// sold within stale_days (slow movers) OR have a live lot expiring within
// expiry_days (sell it before it must be written off). Already-approved markdowns
// are included; dismissed ones and services/inactive products are excluded.
// Already-expired lots are NOT clearance candidates — those belong on the Batches
// page's write-off, so the expiry join only considers future-dated lots.
func (s *Store) StaleItems(ctx context.Context) ([]StaleItem, error) {
	cfg, err := s.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	var rows []StaleItem
	err = s.db.SelectContext(ctx, &rows, `
		SELECT p.id AS product_id, p.name, u.abbreviation AS unit_abbr,
		       COALESCE(st.quantity, 0) AS stock_qty, p.cost_price, p.selling_price,
		       CASE WHEN ls.last_sold IS NULL THEN NULL
		            ELSE EXTRACT(DAY FROM now() - ls.last_sold)::int END AS days_since_sale,
		       be.soonest_expiry,
		       m.status, m.discount_type, m.discount_value
		FROM products p
		JOIN units u ON u.id = p.unit_id
		LEFT JOIN stock st ON st.product_id = p.id
		LEFT JOIN (
		    SELECT si.product_id, MAX(sa.created_at) AS last_sold
		    FROM sale_items si JOIN sales sa ON sa.id = si.sale_id
		    GROUP BY si.product_id
		) ls ON ls.product_id = p.id
		LEFT JOIN (
		    SELECT product_id, MIN(expiry_date) AS soonest_expiry
		    FROM stock_batches
		    WHERE qty_remaining > 0 AND expiry_date IS NOT NULL AND expiry_date > now()
		    GROUP BY product_id
		) be ON be.product_id = p.id
		LEFT JOIN clearance_markdowns m ON m.product_id = p.id
		WHERE p.is_active = true AND p.is_service = false
		  AND COALESCE(st.quantity, 0) > 0
		  AND (
		        (ls.last_sold IS NULL OR ls.last_sold < now() - ($1 || ' days')::interval)
		     OR (be.soonest_expiry IS NOT NULL AND be.soonest_expiry <= now() + ($2 || ' days')::interval)
		  )
		  AND (m.status IS DISTINCT FROM 'dismissed')
		ORDER BY be.soonest_expiry ASC NULLS LAST, ls.last_sold ASC NULLS FIRST, p.name`,
		cfg.StaleDays, cfg.ExpiryDays)
	return rows, err
}

func (s *Store) Approve(ctx context.Context, productID int64, dtype string, value decimal.Decimal, userID int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO clearance_markdowns (product_id, discount_type, discount_value, status, approved_by, updated_at)
		VALUES ($1,$2,$3,'approved',$4, now())
		ON CONFLICT (product_id) DO UPDATE
		SET discount_type=$2, discount_value=$3, status='approved', approved_by=$4, updated_at=now()`,
		productID, dtype, value, userID)
	return err
}

func (s *Store) Dismiss(ctx context.Context, productID, userID int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO clearance_markdowns (product_id, discount_type, discount_value, status, approved_by, updated_at)
		VALUES ($1,'percent',0,'dismissed',$2, now())
		ON CONFLICT (product_id) DO UPDATE
		SET status='dismissed', approved_by=$2, updated_at=now()`,
		productID, userID)
	return err
}

type markdown struct {
	Type  string
	Value decimal.Decimal
}

// approvedMarkdowns returns approved (type,value) for the given product ids.
func (s *Store) approvedMarkdowns(ctx context.Context, ids []int64) (map[int64]markdown, error) {
	out := map[int64]markdown{}
	if len(ids) == 0 {
		return out, nil
	}
	q, args, err := sqlx.In(
		`SELECT product_id, discount_type, discount_value FROM clearance_markdowns
		 WHERE status='approved' AND discount_value > 0 AND product_id IN (?)`, ids)
	if err != nil {
		return nil, err
	}
	q = s.db.Rebind(q)
	rows, err := s.db.QueryxContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var t string
		var v decimal.Decimal
		if err := rows.Scan(&id, &t, &v); err != nil {
			return nil, err
		}
		out[id] = markdown{Type: t, Value: v}
	}
	return out, rows.Err()
}

func (m markdown) label() string {
	if m.Type == "percent" {
		return "Clearance -" + m.Value.String() + "%"
	}
	return "Clearance -" + m.Value.String()
}

// BadgesFor pins "Clearance -N%" (or "-Rs N") on approved products' till cards.
func (s *Store) BadgesFor(ctx context.Context, ids []int64) (map[int64][]string, error) {
	m, err := s.approvedMarkdowns(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := map[int64][]string{}
	for id, md := range m {
		out[id] = []string{md.label()}
	}
	return out, nil
}

// SuggestionsFor returns the till line-discount suggestion for approved products.
func (s *Store) SuggestionsFor(ctx context.Context, ids []int64) (map[int64]plugin.SaleSuggestion, error) {
	m, err := s.approvedMarkdowns(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := map[int64]plugin.SaleSuggestion{}
	for id, md := range m {
		out[id] = plugin.SaleSuggestion{
			DiscountType:  md.Type,
			DiscountValue: md.Value.String(),
			Label:         md.label(),
			Prompt:        "Clearance item — apply the markdown?",
		}
	}
	return out, nil
}
