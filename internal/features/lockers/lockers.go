// Package lockers manages core cash lockers — named money-holding locations
// (safe, bank account, owner's pocket, ...) that sit outside the cashier
// drawers. A locker's balance is always the running SUM of its append-only
// ledger, so it can never drift from its history.
//
// Slice 1 (this file) covers locker CRUD, opening balances and balance/ledger
// reads. The atomic money-move helper (cashflow.Move) that wires lockers into
// expenses, supplier pay, refunds, transfers, etc. lands in later slices and
// will reuse AddEntry over a transaction.
package lockers

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/db"
	"karots-pos/internal/money"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// Kinds a locker may be.
const (
	KindSafe   = "safe"
	KindBank   = "bank"
	KindPocket = "pocket"
	KindOther  = "other"
)

func validKind(k string) bool {
	switch k {
	case KindSafe, KindBank, KindPocket, KindOther:
		return true
	}
	return false
}

// Locker is a money-holding location. Balance is computed (SUM of ledger
// deltas), never stored.
type Locker struct {
	ID            int64           `db:"id"             json:"id"`
	Name          string          `db:"name"           json:"name"`
	Kind          string          `db:"kind"           json:"kind"`
	AllowNegative bool            `db:"allow_negative" json:"allow_negative"`
	IsActive      bool            `db:"is_active"      json:"is_active"`
	CreatedAt     time.Time       `db:"created_at"     json:"created_at"`
	Balance       decimal.Decimal `db:"balance"        json:"balance"`
}

// LedgerEntry is one append-only money movement against a locker.
type LedgerEntry struct {
	ID                 int64           `db:"id"                   json:"id"`
	LockerID           int64           `db:"locker_id"            json:"locker_id"`
	BalanceDelta       decimal.Decimal `db:"balance_delta"        json:"balance_delta"`
	Kind               string          `db:"kind"                 json:"kind"`
	Counterparty       *string         `db:"counterparty"         json:"counterparty,omitempty"`
	CounterTillSession *int64          `db:"counter_till_session" json:"counter_till_session,omitempty"`
	CounterLockerID    *int64          `db:"counter_locker_id"    json:"counter_locker_id,omitempty"`
	RefKind            *string         `db:"ref_kind"             json:"ref_kind,omitempty"`
	RefID              *int64          `db:"ref_id"               json:"ref_id,omitempty"`
	Note               string          `db:"note"                 json:"note"`
	CreatedBy          *int64          `db:"created_by"           json:"created_by,omitempty"`
	CreatedAt          time.Time       `db:"created_at"           json:"created_at"`
}

// LedgerInput records one ledger row. It is deliberately generic so later slices
// (cashflow.Move) can write any movement kind through the same path.
type LedgerInput struct {
	LockerID           int64
	BalanceDelta       decimal.Decimal
	Kind               string
	Counterparty       *string
	CounterTillSession *int64
	CounterLockerID    *int64
	RefKind            *string
	RefID              *int64
	Note               string
	CreatedBy          *int64
}

// ------------------------------------------------------------------ Repository

// Repository runs locker queries against either the pool or a transaction.
type Repository struct{ q db.Queryer }

func NewRepository(q db.Queryer) *Repository { return &Repository{q: q} }

// List returns lockers with live balances. When activeOnly, inactive lockers are
// hidden (they remain for history and balance integrity).
func (r *Repository) List(ctx context.Context, activeOnly bool) ([]Locker, error) {
	var rows []Locker
	err := r.q.SelectContext(ctx, &rows, `
		SELECT l.*, COALESCE(le.bal, 0) AS balance
		FROM lockers l
		LEFT JOIN (
			SELECT locker_id, SUM(balance_delta) AS bal
			FROM locker_ledger GROUP BY locker_id
		) le ON le.locker_id = l.id
		WHERE ($1::bool = false OR l.is_active = true)
		ORDER BY l.is_active DESC, l.name`, activeOnly)
	return rows, err
}

// Get loads one locker with its live balance.
func (r *Repository) Get(ctx context.Context, id int64) (*Locker, error) {
	var l Locker
	err := r.q.GetContext(ctx, &l, `
		SELECT l.*, COALESCE((
			SELECT SUM(balance_delta) FROM locker_ledger WHERE locker_id = l.id
		), 0) AS balance
		FROM lockers l WHERE l.id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// GetForUpdate loads a locker with its live balance and locks the locker row,
// for use inside a transaction that debits it (cashflow.Move). The lock
// serialises concurrent debits so two can't both pass the overdraw guard.
func (r *Repository) GetForUpdate(ctx context.Context, id int64) (*Locker, error) {
	// Lock the row first, then read the balance (a subquery in a locked SELECT
	// can't carry FOR UPDATE through the aggregate).
	if _, err := r.q.ExecContext(ctx, `SELECT 1 FROM lockers WHERE id = $1 FOR UPDATE`, id); err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

// Create inserts a locker row (no opening balance — the service writes that as a
// ledger entry in the same transaction).
func (r *Repository) Create(ctx context.Context, name, kind string, allowNeg bool) (*Locker, error) {
	var l Locker
	err := r.q.GetContext(ctx, &l, `
		INSERT INTO lockers (name, kind, allow_negative)
		VALUES ($1, $2, $3) RETURNING *, 0::numeric AS balance`,
		name, kind, allowNeg)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// Update edits a locker's editable fields (not its balance).
func (r *Repository) Update(ctx context.Context, id int64, name, kind string, allowNeg, isActive bool) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE lockers SET name=$1, kind=$2, allow_negative=$3, is_active=$4 WHERE id=$5`,
		name, kind, allowNeg, isActive, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SetActive flips a locker's active flag (archive / restore).
func (r *Repository) SetActive(ctx context.Context, id int64, active bool) error {
	res, err := r.q.ExecContext(ctx, `UPDATE lockers SET is_active=$1 WHERE id=$2`, active, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// AddEntry appends one ledger row. Generic on purpose: every locker money
// movement (opening balance now; transfers/payments/intakes later) goes through
// here, so it works equally over the pool or a transaction.
func (r *Repository) AddEntry(ctx context.Context, in LedgerInput) (int64, error) {
	var id int64
	err := r.q.GetContext(ctx, &id, `
		INSERT INTO locker_ledger
			(locker_id, balance_delta, kind, counterparty,
			 counter_till_session, counter_locker_id, ref_kind, ref_id, note, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
		in.LockerID, in.BalanceDelta, in.Kind, in.Counterparty,
		in.CounterTillSession, in.CounterLockerID, in.RefKind, in.RefID, in.Note, in.CreatedBy)
	return id, err
}

// LedgerFilter narrows a locker's ledger by date range (To is exclusive — the
// web layer passes the day after the chosen end date).
type LedgerFilter struct {
	From  *time.Time
	To    *time.Time
	Limit int
}

// Ledger returns a locker's entries in the given range, newest first.
func (r *Repository) Ledger(ctx context.Context, lockerID int64, f LedgerFilter) ([]LedgerEntry, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 500
	}
	var rows []LedgerEntry
	err := r.q.SelectContext(ctx, &rows, `
		SELECT * FROM locker_ledger
		WHERE locker_id = $1
		  AND ($2::timestamptz IS NULL OR created_at >= $2)
		  AND ($3::timestamptz IS NULL OR created_at <  $3)
		ORDER BY created_at DESC, id DESC LIMIT $4`,
		lockerID, f.From, f.To, limit)
	return rows, err
}

// --------------------------------------------------------------------- Service

// CreateInput is the admin create-locker form.
type CreateInput struct {
	Name           string `form:"name"            json:"name"`
	Kind           string `form:"kind"            json:"kind"`
	AllowNegative  string `form:"allow_negative"  json:"allow_negative"`
	OpeningBalance string `form:"opening_balance" json:"opening_balance"`
}

// UpdateInput is the admin edit-locker form.
type UpdateInput struct {
	Name          string `form:"name"           json:"name"`
	Kind          string `form:"kind"           json:"kind"`
	AllowNegative string `form:"allow_negative" json:"allow_negative"`
	IsActive      string `form:"is_active"      json:"is_active"`
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "1", "yes":
		return true
	}
	return false
}

// Service wraps the repository with validation and transactional creation.
type Service struct {
	db   *sqlx.DB
	repo *Repository
}

func NewService(db *sqlx.DB) *Service { return &Service{db: db, repo: NewRepository(db)} }

func (s *Service) List(ctx context.Context, activeOnly bool) ([]Locker, error) {
	rows, err := s.repo.List(ctx, activeOnly)
	if err != nil {
		return nil, apperr.Internal("failed to list lockers", err)
	}
	return rows, nil
}

func (s *Service) Get(ctx context.Context, id int64) (*Locker, error) {
	l, err := s.repo.Get(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("locker")
		}
		return nil, apperr.Internal("failed to load locker", err)
	}
	return l, nil
}

// Create makes a locker and, when an opening balance is given, records it as an
// open_balance ledger entry in the same transaction.
func (s *Service) Create(ctx context.Context, in CreateInput, userID int64) (*Locker, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, apperr.Validation("locker name is required")
	}
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		kind = KindOther
	}
	if !validKind(kind) {
		return nil, apperr.Validation("invalid locker kind")
	}
	allowNeg := truthy(in.AllowNegative)

	opening := decimal.Zero
	if strings.TrimSpace(in.OpeningBalance) != "" {
		v, err := money.Parse(in.OpeningBalance)
		if err != nil {
			return nil, apperr.Validation("opening balance must be a number")
		}
		opening = v
	}
	if opening.IsNegative() && !allowNeg {
		return nil, apperr.Validation("opening balance can't be negative unless the locker allows it")
	}

	var created *Locker
	err := db.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		repo := NewRepository(tx)
		l, err := repo.Create(ctx, name, kind, allowNeg)
		if err != nil {
			return err
		}
		if !opening.IsZero() {
			var by *int64
			if userID > 0 {
				by = &userID
			}
			if _, err := repo.AddEntry(ctx, LedgerInput{
				LockerID: l.ID, BalanceDelta: opening, Kind: "open_balance",
				Note: "Opening balance", CreatedBy: by,
			}); err != nil {
				return err
			}
			l.Balance = opening
		}
		created = l
		return nil
	})
	if err != nil {
		return nil, apperr.Internal("failed to create locker", err)
	}
	return created, nil
}

func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) error {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return apperr.Validation("locker name is required")
	}
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		kind = KindOther
	}
	if !validKind(kind) {
		return apperr.Validation("invalid locker kind")
	}
	err := s.repo.Update(ctx, id, name, kind, truthy(in.AllowNegative), truthy(in.IsActive))
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFound("locker")
	}
	if err != nil {
		return apperr.Internal("failed to update locker", err)
	}
	return nil
}

// SetActive archives or restores a locker.
func (s *Service) SetActive(ctx context.Context, id int64, active bool) error {
	err := s.repo.SetActive(ctx, id, active)
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFound("locker")
	}
	if err != nil {
		return apperr.Internal("failed to update locker", err)
	}
	return nil
}

func (s *Service) Ledger(ctx context.Context, lockerID int64, f LedgerFilter) ([]LedgerEntry, error) {
	rows, err := s.repo.Ledger(ctx, lockerID, f)
	if err != nil {
		return nil, apperr.Internal("failed to load locker ledger", err)
	}
	return rows, nil
}
