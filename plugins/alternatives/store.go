package alternatives

import (
	"context"

	appdb "karots-pos/internal/db"

	"github.com/lib/pq"
)

// Group is one set of interchangeable products (e.g. "USB Flash Drive 32GB").
type Group struct {
	ID        int64  `db:"id"`
	Name      string `db:"name"`
	SortOrder int    `db:"sort_order"`
	IsActive  bool   `db:"is_active"`
}

// Tier is a quality separation within a group (Genuine / Normal / Cheap), with its
// own reorder level compared against the summed on-hand qty of its members.
type Tier struct {
	ID           int64  `db:"id"`
	GroupID      int64  `db:"group_id"`
	Name         string `db:"name"`
	ReorderLevel int    `db:"reorder_level"`
	SortOrder    int    `db:"sort_order"`
	IsActive     bool   `db:"is_active"`
}

type Store struct{ db appdb.Queryer }

func NewStore(db appdb.Queryer) *Store { return &Store{db: db} }

// --- groups ---

func (s *Store) Groups(ctx context.Context, includeDisabled bool) ([]Group, error) {
	var rows []Group
	err := s.db.SelectContext(ctx, &rows,
		`SELECT id, name, sort_order, is_active FROM alt_groups
		 WHERE ($1 OR is_active) ORDER BY sort_order, name`, includeDisabled)
	return rows, err
}

func (s *Store) Group(ctx context.Context, id int64) (*Group, error) {
	var g Group
	if err := s.db.GetContext(ctx, &g,
		`SELECT id, name, sort_order, is_active FROM alt_groups WHERE id=$1`, id); err != nil {
		return nil, err
	}
	return &g, nil
}

func (s *Store) CreateGroup(ctx context.Context, g Group) (int64, error) {
	var id int64
	err := s.db.GetContext(ctx, &id,
		`INSERT INTO alt_groups (name, sort_order, is_active) VALUES ($1,$2,true) RETURNING id`,
		g.Name, g.SortOrder)
	return id, err
}

func (s *Store) UpdateGroup(ctx context.Context, g Group) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE alt_groups SET name=$2, sort_order=$3 WHERE id=$1`, g.ID, g.Name, g.SortOrder)
	return err
}

func (s *Store) SetGroupActive(ctx context.Context, id int64, active bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alt_groups SET is_active=$2 WHERE id=$1`, id, active)
	return err
}

// GroupSummary is a group with its tier/product counts and total on-hand qty, for
// the groups list.
type GroupSummary struct {
	Group
	Tiers    int `db:"tiers"`
	Products int `db:"products"`
	Qty      int `db:"qty"`
}

func (s *Store) GroupSummaries(ctx context.Context, includeDisabled bool) ([]GroupSummary, error) {
	var rows []GroupSummary
	err := s.db.SelectContext(ctx, &rows, `
		SELECT g.id, g.name, g.sort_order, g.is_active,
		       COUNT(DISTINCT t.id) FILTER (WHERE t.is_active) AS tiers,
		       COUNT(DISTINCT m.product_id)                    AS products,
		       COALESCE(SUM(COALESCE(st.quantity,0)),0)::int   AS qty
		FROM alt_groups g
		LEFT JOIN alt_tiers t   ON t.group_id = g.id
		LEFT JOIN alt_members m ON m.tier_id  = t.id
		LEFT JOIN products p    ON p.id = m.product_id AND p.is_active
		LEFT JOIN stock st      ON st.product_id = m.product_id AND p.is_active
		WHERE ($1 OR g.is_active)
		GROUP BY g.id
		ORDER BY g.sort_order, g.name`, includeDisabled)
	return rows, err
}

// --- tiers ---

func (s *Store) Tiers(ctx context.Context, groupID int64) ([]Tier, error) {
	var rows []Tier
	err := s.db.SelectContext(ctx, &rows,
		`SELECT id, group_id, name, reorder_level, sort_order, is_active
		 FROM alt_tiers WHERE group_id=$1 ORDER BY sort_order, id`, groupID)
	return rows, err
}

func (s *Store) Tier(ctx context.Context, id int64) (*Tier, error) {
	var t Tier
	if err := s.db.GetContext(ctx, &t,
		`SELECT id, group_id, name, reorder_level, sort_order, is_active
		 FROM alt_tiers WHERE id=$1`, id); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) CreateTier(ctx context.Context, t Tier) (int64, error) {
	var id int64
	err := s.db.GetContext(ctx, &id,
		`INSERT INTO alt_tiers (group_id, name, reorder_level, sort_order, is_active)
		 VALUES ($1,$2,$3,$4,true) RETURNING id`,
		t.GroupID, t.Name, t.ReorderLevel, t.SortOrder)
	return id, err
}

func (s *Store) UpdateTier(ctx context.Context, t Tier) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE alt_tiers SET name=$2, reorder_level=$3, sort_order=$4 WHERE id=$1`,
		t.ID, t.Name, t.ReorderLevel, t.SortOrder)
	return err
}

func (s *Store) SetTierActive(ctx context.Context, id int64, active bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alt_tiers SET is_active=$2 WHERE id=$1`, id, active)
	return err
}

// --- members ---

func (s *Store) MembersOfTier(ctx context.Context, tierID int64) ([]int64, error) {
	var ids []int64
	err := s.db.SelectContext(ctx, &ids,
		`SELECT product_id FROM alt_members WHERE tier_id=$1 ORDER BY product_id`, tierID)
	return ids, err
}

// MemberRow is a tier member product with its on-hand qty, for the group detail page.
type MemberRow struct {
	ProductID int64  `db:"product_id"`
	Name      string `db:"name"`
	Qty       int    `db:"qty"`
}

func (s *Store) TierMembers(ctx context.Context, tierID int64) ([]MemberRow, error) {
	var rows []MemberRow
	err := s.db.SelectContext(ctx, &rows, `
		SELECT m.product_id, p.name, COALESCE(st.quantity,0)::int AS qty
		FROM alt_members m
		JOIN products p    ON p.id = m.product_id AND p.is_active
		LEFT JOIN stock st ON st.product_id = m.product_id
		WHERE m.tier_id = $1
		ORDER BY p.name`, tierID)
	return rows, err
}

// MemberTier returns the product's current tier id (found=false if it isn't a member).
func (s *Store) MemberTier(ctx context.Context, productID int64) (int64, bool, error) {
	var tid int64
	if err := s.db.GetContext(ctx, &tid,
		`SELECT tier_id FROM alt_members WHERE product_id=$1`, productID); err != nil {
		return 0, false, nil // not a member (incl. sql.ErrNoRows)
	}
	return tid, true, nil
}

// AddMember inserts, or MOVES the product to a new tier on conflict — enforcing the
// exactly-one rule (product_id is the PK).
func (s *Store) AddMember(ctx context.Context, productID, tierID int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO alt_members (product_id, tier_id) VALUES ($1,$2)
		 ON CONFLICT (product_id) DO UPDATE SET tier_id = EXCLUDED.tier_id`,
		productID, tierID)
	return err
}

func (s *Store) RemoveMember(ctx context.Context, productID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM alt_members WHERE product_id=$1`, productID)
	return err
}

func (s *Store) AllMemberIDs(ctx context.Context) ([]int64, error) {
	var ids []int64
	err := s.db.SelectContext(ctx, &ids, `SELECT product_id FROM alt_members`)
	return ids, err
}

// --- search + badges + reorder ---

// MatchProductIDs returns the active members of every group the query touches — by
// group name, tier name, OR a member product's name. Yields both group-name search
// ("32gb pendrive") and always-on siblings (search "Kingston" → the whole group).
// Inactive products and disabled groups/tiers are excluded.
func (s *Store) MatchProductIDs(ctx context.Context, query string) ([]int64, error) {
	var ids []int64
	err := s.db.SelectContext(ctx, &ids, `
		WITH hit AS (
			SELECT DISTINCT g.id AS group_id
			FROM alt_groups g
			JOIN alt_tiers t   ON t.group_id = g.id AND t.is_active
			JOIN alt_members m ON m.tier_id  = t.id
			JOIN products p    ON p.id = m.product_id AND p.is_active
			WHERE g.is_active AND (
				g.name ILIKE '%' || $1 || '%'
				OR t.name ILIKE '%' || $1 || '%'
				OR p.name ILIKE '%' || $1 || '%'
			)
		)
		SELECT DISTINCT m.product_id
		FROM alt_members m
		JOIN alt_tiers t ON t.id = m.tier_id AND t.is_active
		JOIN hit h       ON h.group_id = t.group_id
		JOIN products p  ON p.id = m.product_id AND p.is_active`, query)
	return ids, err
}

// BadgesFor maps each given product id that is an active group member to its tier
// name (e.g. {12: ["Genuine"]}), for the till card pin. One query for the page.
func (s *Store) BadgesFor(ctx context.Context, productIDs []int64) (map[int64][]string, error) {
	if len(productIDs) == 0 {
		return nil, nil
	}
	type row struct {
		ProductID int64  `db:"product_id"`
		Tier      string `db:"tier"`
	}
	var rows []row
	if err := s.db.SelectContext(ctx, &rows, `
		SELECT m.product_id, t.name AS tier
		FROM alt_members m
		JOIN alt_tiers t  ON t.id = m.tier_id AND t.is_active
		JOIN alt_groups g ON g.id = t.group_id AND g.is_active
		WHERE m.product_id = ANY($1)`, pq.Array(productIDs)); err != nil {
		return nil, err
	}
	out := make(map[int64][]string, len(rows))
	for _, r := range rows {
		out[r.ProductID] = append(out[r.ProductID], r.Tier)
	}
	return out, nil
}

// TierRollup is a tier with its summed member qty and low flag.
type TierRollup struct {
	Tier     Tier
	TotalQty int
	Low      bool
}

// GroupRollup is a group with its tier rollups and total qty.
type GroupRollup struct {
	Group    Group
	TotalQty int
	Tiers    []TierRollup
}

// Reorder returns every active group with its active tiers, each carrying the summed
// on-hand qty of its active member products and a low flag (reorder_level>0 and
// total<=level).
func (s *Store) Reorder(ctx context.Context) ([]GroupRollup, error) {
	groups, err := s.Groups(ctx, false)
	if err != nil {
		return nil, err
	}
	type tq struct {
		TierID int64 `db:"tier_id"`
		Qty    int   `db:"qty"`
	}
	var sums []tq
	if err := s.db.SelectContext(ctx, &sums, `
		SELECT m.tier_id, COALESCE(SUM(COALESCE(st.quantity,0)),0)::int AS qty
		FROM alt_members m
		JOIN products p    ON p.id = m.product_id AND p.is_active
		LEFT JOIN stock st ON st.product_id = m.product_id
		GROUP BY m.tier_id`); err != nil {
		return nil, err
	}
	qtyByTier := map[int64]int{}
	for _, r := range sums {
		qtyByTier[r.TierID] = r.Qty
	}

	out := make([]GroupRollup, 0, len(groups))
	for _, g := range groups {
		tiers, err := s.Tiers(ctx, g.ID)
		if err != nil {
			return nil, err
		}
		gr := GroupRollup{Group: g}
		for _, t := range tiers {
			if !t.IsActive {
				continue
			}
			q := qtyByTier[t.ID]
			gr.Tiers = append(gr.Tiers, TierRollup{
				Tier:     t,
				TotalQty: q,
				Low:      t.ReorderLevel > 0 && q <= t.ReorderLevel,
			})
			gr.TotalQty += q
		}
		out = append(out, gr)
	}
	return out, nil
}
