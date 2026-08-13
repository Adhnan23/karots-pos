// Package activity is a read-only, cross-cutting "who did what" view. It unions
// the core accountability trails — the audit log, stock movements, cash-drawer
// movements and locker ledger — into one normalized row so the admin can filter
// every action by WHO, source, date and free text in a single place. The
// specialized pages (stock movements, cashflow, locker ledger, cash sessions)
// keep their domain columns; this is the flat lens across all of them.
//
// Plugins can't be unioned in SQL (core stays plugin-free), so they contribute
// their own rows via the plugin.ActivityContributor hook and the web layer
// merges them with the core rows here — the same shape PLIncome uses for the P&L.
package activity

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// Core source identifiers (also used as the Source filter values).
const (
	SourceAudit  = "audit"
	SourceStock  = "stock"
	SourceCash   = "cash"
	SourceLocker = "locker"
)

// Row is one normalized activity entry from any source. Amount is a signed money
// delta where the source has one (cash, locker), else zero.
type Row struct {
	When     time.Time       `db:"created_at" json:"when"`
	UserID   *int64          `db:"user_id"    json:"user_id,omitempty"`
	UserName string          `db:"user_name"  json:"user_name"`
	Source   string          `db:"source"     json:"source"`
	Action   string          `db:"action"     json:"action"`
	Detail   string          `db:"detail"     json:"detail"`
	Amount   decimal.Decimal `db:"amount"     json:"amount"`
}

// Filter narrows the view. An empty/"all" Source means every source; a zero
// UserID pointer means every user. Contributors receive the same filter so a
// plugin only returns rows in range.
type Filter struct {
	From, To *time.Time
	UserID   *int64
	Source   string // "", "all", or a specific source id
	Query    string // free-text ILIKE over action / detail / user name
	Limit    int
}

// UserRef is one entry in the "who" filter dropdown.
type UserRef struct {
	ID   int64  `db:"id"   json:"id"`
	Name string `db:"name" json:"name"`
}

type Service struct{ db *sqlx.DB }

func NewService(db *sqlx.DB) *Service { return &Service{db: db} }

// coreUnion normalizes each core trail to the same columns. Amounts/details are
// built in SQL so one round trip returns display-ready rows. user_name comes
// inline for audit, joined from users for the id-based sources.
const coreUnion = `
(SELECT created_at, user_id, user_name, 'audit'::text AS source, action::text AS action,
        (entity || COALESCE(' #'||entity_id,'') || COALESCE(' — '||detail,''))::text AS detail,
        0::numeric AS amount
   FROM audit_log)
UNION ALL
(SELECT m.created_at, m.user_id, COALESCE(u.name,'')::text, 'stock'::text, m.type::text,
        (COALESCE(p.name,'product #'||m.product_id::text) || ' × ' || m.quantity::text
         || COALESCE(' — '||m.note,''))::text,
        0::numeric
   FROM stock_movements m
   LEFT JOIN users u ON u.id = m.user_id
   LEFT JOIN products p ON p.id = m.product_id)
UNION ALL
(SELECT c.created_at, c.user_id, COALESCE(u.name,'')::text, 'cash'::text, c.type::text,
        COALESCE(c.reason,'')::text, c.amount
   FROM cash_movements c
   LEFT JOIN users u ON u.id = c.user_id)
UNION ALL
(SELECT l.created_at, l.created_by, COALESCE(u.name,'')::text, 'locker'::text,
        COALESCE(l.ref_kind, l.kind)::text,
        (COALESCE(lk.name,'locker #'||l.locker_id::text)
         || COALESCE(' — '||l.counterparty,'') || COALESCE(' '||l.note,''))::text,
        l.balance_delta
   FROM locker_ledger l
   LEFT JOIN users u ON u.id = l.created_by
   LEFT JOIN lockers lk ON lk.id = l.locker_id)`

// List returns the core-source rows matching f, newest first. The web layer
// merges these with plugin contributors, then sorts and paginates the whole set.
// ponytail: in-memory merge/paginate downstream, bounded here by the date filter
// + Limit cap; move to a materialized union if a busy shop's volume ever hurts.
func (s *Service) List(ctx context.Context, f Filter) ([]Row, error) {
	if f.Limit <= 0 || f.Limit > 20000 {
		f.Limit = 20000
	}
	var source, query *string
	if f.Source != "" && f.Source != "all" {
		source = &f.Source
	}
	if f.Query != "" {
		query = &f.Query
	}
	var rows []Row
	err := s.db.SelectContext(ctx, &rows, `
		SELECT created_at, user_id, user_name, source, action, detail, amount
		FROM (`+coreUnion+`) t
		WHERE ($1::timestamptz IS NULL OR created_at >= $1)
		  AND ($2::timestamptz IS NULL OR created_at <  $2)
		  AND ($3::bigint IS NULL OR user_id = $3)
		  AND ($4::text IS NULL OR source = $4)
		  AND ($5::text IS NULL OR action ILIKE '%'||$5||'%'
		                        OR detail ILIKE '%'||$5||'%'
		                        OR user_name ILIKE '%'||$5||'%')
		  -- Hide the hidden system/support account's actions from the trail.
		  AND (user_id IS NULL OR user_id NOT IN (SELECT id FROM users WHERE is_system))
		ORDER BY created_at DESC
		LIMIT $6`,
		f.From, f.To, f.UserID, source, query, f.Limit)
	return rows, err
}

// Users lists the shop's staff accounts for the "who" dropdown. The system
// account (the developer's hidden debug backdoor) is excluded — its actions are
// hidden from the trail, so it must not be a filter option either.
func (s *Service) Users(ctx context.Context) ([]UserRef, error) {
	var rows []UserRef
	err := s.db.SelectContext(ctx, &rows, `SELECT id, name FROM users WHERE NOT is_system ORDER BY name`)
	return rows, err
}
