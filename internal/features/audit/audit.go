// Package audit records a tamper-evident-ish trail of who did what. Recording is
// best-effort: a failed audit write is logged but never blocks the underlying
// operation, so accountability can't break a sale or an edit.
package audit

import (
	"context"
	"log"
	"time"

	appdb "karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
)

// Common action verbs and entity names (free-form, these are just conventions).
const (
	ActionCreate   = "create"
	ActionUpdate   = "update"
	ActionDelete   = "delete"
	ActionReturn   = "return"
	ActionPayment  = "payment"
	ActionWithdraw = "withdraw"
	ActionClose    = "close"
	ActionSettings = "settings"
	ActionBackup   = "backup"
	ActionRestore  = "restore"
)

type Entry struct {
	ID        int64     `db:"id"         json:"id"`
	UserID    *int64    `db:"user_id"    json:"user_id,omitempty"`
	UserName  string    `db:"user_name"  json:"user_name"`
	Action    string    `db:"action"     json:"action"`
	Entity    string    `db:"entity"     json:"entity"`
	EntityID  *string   `db:"entity_id"  json:"entity_id,omitempty"`
	Detail    *string   `db:"detail"     json:"detail,omitempty"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

type Repository struct{ q appdb.Queryer }

func NewRepository(q appdb.Queryer) *Repository { return &Repository{q: q} }

// Insert writes one entry. user_name is resolved from the users table so callers
// only need to pass the id.
func (r *Repository) Insert(ctx context.Context, userID int64, action, entity, entityID, detail string) error {
	var uid *int64
	if userID > 0 {
		uid = &userID
	}
	var eid, det *string
	if entityID != "" {
		eid = &entityID
	}
	if detail != "" {
		det = &detail
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO audit_log (user_id, user_name, action, entity, entity_id, detail)
		VALUES ($1, COALESCE((SELECT name FROM users WHERE id = $1), ''), $2, $3, $4, $5)`,
		uid, action, entity, eid, det)
	return err
}

// ListFilter narrows the audit view.
type ListFilter struct {
	From, To *time.Time
	Entity   string
	Limit    int
}

func (r *Repository) List(ctx context.Context, f ListFilter) ([]Entry, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 200
	}
	var entity *string
	if f.Entity != "" {
		entity = &f.Entity
	}
	var rows []Entry
	err := r.q.SelectContext(ctx, &rows, `
		SELECT * FROM audit_log
		WHERE ($1::timestamptz IS NULL OR created_at >= $1)
		  AND ($2::timestamptz IS NULL OR created_at <  $2)
		  AND ($3::text IS NULL OR entity = $3)
		ORDER BY created_at DESC
		LIMIT $4`, f.From, f.To, entity, f.Limit)
	return rows, err
}

type Service struct {
	db   *sqlx.DB
	repo *Repository
}

func NewService(db *sqlx.DB) *Service { return &Service{db: db, repo: NewRepository(db)} }

// Record writes an audit entry, swallowing (logging) any error so it can be
// called inline after a mutation without an error-handling burden at the call site.
func (s *Service) Record(ctx context.Context, userID int64, action, entity, entityID, detail string) {
	if err := s.repo.Insert(ctx, userID, action, entity, entityID, detail); err != nil {
		log.Printf("audit: failed to record %s %s: %v", action, entity, err)
	}
}

func (s *Service) List(ctx context.Context, f ListFilter) ([]Entry, error) {
	return s.repo.List(ctx, f)
}
