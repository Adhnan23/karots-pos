// Package suppliers manages wholesale suppliers and their outstanding payables.
package suppliers

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/config"
	"karots-pos/internal/db"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

type Supplier struct {
	ID                 int64           `db:"id"                  json:"id"`
	Name               string          `db:"name"                json:"name"`
	ContactPerson      *string         `db:"contact_person"      json:"contact_person,omitempty"`
	Phone              *string         `db:"phone"               json:"phone,omitempty"`
	Address            *string         `db:"address"             json:"address,omitempty"`
	CreditDays         int             `db:"credit_days"         json:"credit_days"`
	OutstandingBalance decimal.Decimal `db:"outstanding_balance" json:"outstanding_balance"`
	OpeningBalance     decimal.Decimal `db:"opening_balance"     json:"opening_balance"`
	OpeningUnlinked    decimal.Decimal `db:"opening_unlinked"    json:"opening_unlinked"`
	IsActive           bool            `db:"is_active"           json:"is_active"`
	CreatedAt          time.Time       `db:"created_at"          json:"created_at"`
}

// LinkedBalance is the transactional (document-backed) part of what we owe this
// supplier: the outstanding payable minus the still-unpaid old (opening) debt.
// This is read-only in the UI; only the opening part is editable.
func (s Supplier) LinkedBalance() decimal.Decimal {
	return s.OutstandingBalance.Sub(s.OpeningUnlinked)
}

type CreateInput struct {
	Name          string  `json:"name"           form:"name"           validate:"required,min=2,max=150"`
	ContactPerson *string `json:"contact_person" form:"contact_person"`
	Phone         *string `json:"phone"          form:"phone"          validate:"omitempty,max=15"`
	Address       *string `json:"address"        form:"address"`
	CreditDays    int     `json:"credit_days"    form:"credit_days"    validate:"gte=0"`
	// OpeningBalance is the amount we already owed this supplier at onboarding.
	// Applied once, at creation; Update never touches it.
	OpeningBalance string `json:"opening_balance" form:"opening_balance"`
}

type UpdateInput = CreateInput

type Repository struct{ q db.Queryer }

func NewRepository(q db.Queryer) *Repository { return &Repository{q: q} }

func (r *Repository) List(ctx context.Context, search string) ([]Supplier, error) {
	var rows []Supplier
	var s *string
	if strings.TrimSpace(search) != "" {
		s = &search
	}
	err := r.q.SelectContext(ctx, &rows, `
		SELECT * FROM suppliers
		WHERE is_active = true
		  AND ($1::text IS NULL OR name ILIKE '%' || $1 || '%'
		       OR contact_person ILIKE '%' || $1 || '%' OR phone ILIKE '%' || $1 || '%')
		ORDER BY name`, s)
	return rows, err
}

// OwingRow is a supplier we owe money, plus the date of their oldest unpaid
// purchase (an aging proxy).
type OwingRow struct {
	Supplier
	OldestUnpaid *time.Time `db:"oldest_unpaid" json:"oldest_unpaid,omitempty"`
}

// Owing lists active suppliers with an outstanding payable, biggest first.
func (r *Repository) Owing(ctx context.Context) ([]OwingRow, error) {
	var rows []OwingRow
	err := r.q.SelectContext(ctx, &rows, `
		SELECT s.*,
		       (SELECT MIN(pu.created_at) FROM purchases pu
		         WHERE pu.supplier_id = s.id AND pu.status <> 'draft' AND pu.total > pu.paid_amount) AS oldest_unpaid
		FROM suppliers s
		WHERE s.is_active = true AND s.outstanding_balance > 0
		ORDER BY s.outstanding_balance DESC`)
	return rows, err
}

func (r *Repository) FindByID(ctx context.Context, id int64) (*Supplier, error) {
	var s Supplier
	err := r.q.GetContext(ctx, &s, `SELECT * FROM suppliers WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// FindByName looks up an active supplier by case-insensitive name. Returns
// sql.ErrNoRows when absent.
func (r *Repository) FindByName(ctx context.Context, name string) (*Supplier, error) {
	var s Supplier
	err := r.q.GetContext(ctx, &s,
		`SELECT * FROM suppliers WHERE lower(name) = lower($1) AND is_active = true ORDER BY id LIMIT 1`, name)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *Repository) Create(ctx context.Context, in CreateInput, opening decimal.Decimal) (*Supplier, error) {
	var s Supplier
	err := r.q.GetContext(ctx, &s, `
		INSERT INTO suppliers (name, contact_person, phone, address, credit_days, opening_balance, outstanding_balance, opening_unlinked)
		VALUES ($1,$2,$3,$4,$5,$6,$6,$6) RETURNING *`,
		in.Name, in.ContactPerson, in.Phone, in.Address, in.CreditDays, opening)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *Repository) Update(ctx context.Context, id int64, in UpdateInput) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE suppliers SET name=$1, contact_person=$2, phone=$3, address=$4, credit_days=$5
		WHERE id=$6`, in.Name, in.ContactPerson, in.Phone, in.Address, in.CreditDays, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ListAll returns active + disabled suppliers (disabled last), for the admin list
// with "Show disabled" on so they can be reactivated.
func (r *Repository) ListAll(ctx context.Context, search string) ([]Supplier, error) {
	var rows []Supplier
	var s *string
	if strings.TrimSpace(search) != "" {
		s = &search
	}
	err := r.q.SelectContext(ctx, &rows, `
		SELECT * FROM suppliers
		WHERE ($1::text IS NULL OR name ILIKE '%' || $1 || '%'
		       OR contact_person ILIKE '%' || $1 || '%' OR phone ILIKE '%' || $1 || '%')
		ORDER BY is_active DESC, name`, s)
	return rows, err
}

func (r *Repository) Deactivate(ctx context.Context, id int64) error {
	_, err := r.q.ExecContext(ctx, `UPDATE suppliers SET is_active=false WHERE id=$1`, id)
	return err
}

func (r *Repository) Reactivate(ctx context.Context, id int64) error {
	_, err := r.q.ExecContext(ctx, `UPDATE suppliers SET is_active=true WHERE id=$1`, id)
	return err
}

// AddBalance changes a supplier's payable (used inside the purchase tx; pass a
// negative delta when paying the supplier). A payment settles the transactional
// (linked) part first; any overflow erodes the still-unpaid opening debt, so
// opening_unlinked is clamped to never exceed the new outstanding balance.
func (r *Repository) AddBalance(ctx context.Context, id int64, delta decimal.Decimal) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE suppliers
		SET outstanding_balance = outstanding_balance + $1,
		    opening_unlinked = LEAST(opening_unlinked, GREATEST(outstanding_balance + $1, 0))
		WHERE id=$2`, delta, id)
	return err
}

// PayOpening reduces the old (opening) debt directly: outstanding_balance and
// opening_unlinked each drop by amt (capped at the old debt still owed), leaving
// the gross opening_balance and the linked part untouched. Callers guard against
// overpaying; the clamp here is a safety net. Reducing both columns by the SAME
// capped amount preserves linked — clamping outstanding alone would wipe it out.
func (r *Repository) PayOpening(ctx context.Context, id int64, amt decimal.Decimal) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE suppliers SET
			outstanding_balance = outstanding_balance - GREATEST(LEAST($1, opening_unlinked), 0),
			opening_unlinked    = opening_unlinked    - GREATEST(LEAST($1, opening_unlinked), 0)
		WHERE id=$2`, amt, id)
	return err
}

// AdjustOpening sets the still-unpaid opening (old pre-system debt) to newOpening
// and shifts both the outstanding payable and the gross opening figure by the same
// delta, so the transactional (linked) part is left untouched. newOpening must be
// non-negative.
func (r *Repository) AdjustOpening(ctx context.Context, id int64, newOpening decimal.Decimal) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE suppliers SET
			outstanding_balance = outstanding_balance + ($1 - opening_unlinked),
			opening_balance     = opening_balance     + ($1 - opening_unlinked),
			opening_unlinked    = $1
		WHERE id=$2`, newOpening, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

type Service struct {
	db   *sqlx.DB
	repo *Repository
}

func NewService(db *sqlx.DB) *Service { return &Service{db: db, repo: NewRepository(db)} }

func (s *Service) List(ctx context.Context, search string) ([]Supplier, error) {
	rows, err := s.repo.List(ctx, search)
	if err != nil {
		return nil, apperr.Internal("failed to list suppliers", err)
	}
	return rows, nil
}

// ListAll includes disabled suppliers (for the admin list with "Show disabled").
func (s *Service) ListAll(ctx context.Context, search string) ([]Supplier, error) {
	rows, err := s.repo.ListAll(ctx, search)
	if err != nil {
		return nil, apperr.Internal("failed to list suppliers", err)
	}
	return rows, nil
}

// Reactivate re-enables a disabled supplier.
func (s *Service) Reactivate(ctx context.Context, id int64) error {
	if err := s.repo.Reactivate(ctx, id); err != nil {
		return apperr.Internal("failed to reactivate supplier", err)
	}
	return nil
}

// Owing lists suppliers with an outstanding payable (for the dues report).
func (s *Service) Owing(ctx context.Context) ([]OwingRow, error) {
	rows, err := s.repo.Owing(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to list supplier dues", err)
	}
	return rows, nil
}

func (s *Service) Get(ctx context.Context, id int64) (*Supplier, error) {
	sup, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("supplier")
		}
		return nil, apperr.Internal("failed to load supplier", err)
	}
	return sup, nil
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*Supplier, error) {
	in.Name = strings.TrimSpace(in.Name)
	// No two active suppliers should share a name (case-insensitive). Without this
	// a repeated "Add supplier" silently spawned a duplicate. The importer's own
	// upsert (ImportOne / FindOrCreateByName) is a separate, intentional path.
	if existing, ferr := s.repo.FindByName(ctx, in.Name); ferr == nil && existing != nil {
		return nil, apperr.Conflict("a supplier named \"" + existing.Name + "\" already exists")
	} else if ferr != nil && !errors.Is(ferr, sql.ErrNoRows) {
		return nil, apperr.Internal("failed to check for an existing supplier", ferr)
	}
	opening, err := parseOpening(in.OpeningBalance)
	if err != nil {
		return nil, err
	}
	sup, err := s.repo.Create(ctx, in, opening)
	if err != nil {
		return nil, apperr.Internal("failed to create supplier", err)
	}
	return sup, nil
}

// parseOpening parses an optional opening-balance string (blank → 0), rejecting
// negatives. We can't owe a supplier a negative amount at onboarding.
func parseOpening(s string) (decimal.Decimal, error) {
	if strings.TrimSpace(s) == "" {
		return decimal.Zero, nil
	}
	v, err := money.Parse(s)
	if err != nil || v.IsNegative() {
		return decimal.Zero, apperr.Validation("opening balance must be a non-negative amount")
	}
	return v, nil
}

// FindOrCreateByName resolves a supplier by case-insensitive name, creating a
// bare supplier (name only) if none exists. An empty name returns (nil, nil) so
// the caller can leave the link unset. Used by the bulk product importer.
func (s *Service) FindOrCreateByName(ctx context.Context, name string) (*int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	if sup, err := s.repo.FindByName(ctx, name); err == nil {
		return &sup.ID, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.Internal("failed to resolve supplier", err)
	}
	sup, err := s.repo.Create(ctx, CreateInput{Name: name}, decimal.Zero)
	if err != nil {
		return nil, apperr.Internal("failed to create supplier "+name, err)
	}
	return &sup.ID, nil
}

// ImportResult reports what one import row did, for the summary.
type ImportResult struct {
	Action string // "created" | "updated"
	Note   string
}

// ImportOne upserts one supplier in a transaction, matching an existing active
// supplier by case-insensitive name. The opening balance (already parsed into
// in.OpeningBalance) is applied on create only, so re-imports never re-add it.
func (s *Service) ImportOne(ctx context.Context, in CreateInput, opening decimal.Decimal) (ImportResult, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return ImportResult{}, apperr.Validation("name is required")
	}
	var res ImportResult
	err := db.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		r := NewRepository(tx)
		if existing, ferr := r.FindByName(ctx, in.Name); ferr == nil {
			if uerr := r.Update(ctx, existing.ID, in); uerr != nil {
				return uerr
			}
			res.Action = "updated"
			if opening.IsPositive() {
				res.Note = "opening balance skipped (existing supplier)"
			}
			return nil
		} else if !errors.Is(ferr, sql.ErrNoRows) {
			return ferr
		}
		if _, cerr := r.Create(ctx, in, opening); cerr != nil {
			return cerr
		}
		res.Action = "created"
		return nil
	})
	if err != nil {
		return ImportResult{}, apperr.Internal("failed to import supplier", err)
	}
	return res, nil
}

func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) error {
	err := s.repo.Update(ctx, id, in)
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFound("supplier")
	}
	if err != nil {
		return apperr.Internal("failed to update supplier", err)
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	if err := s.repo.Deactivate(ctx, id); err != nil {
		return apperr.Internal("failed to remove supplier", err)
	}
	return nil
}

// AdjustOpening corrects the editable opening (old pre-system debt) for a
// supplier. It returns the supplier as it was before the change (for the audit
// trail) and the refreshed supplier. The transactional (linked) part is never
// touched — only the opening and, by the same delta, the outstanding payable.
func (s *Service) AdjustOpening(ctx context.Context, id int64, newOpeningStr string) (before, after *Supplier, err error) {
	// The opening (old pre-system balance) may be negative here: a supplier we were
	// in credit with before go-live (an advance/overpayment they still owe us).
	newOpening, perr := money.Parse(newOpeningStr)
	if perr != nil {
		return nil, nil, apperr.Validation("opening balance must be a valid amount")
	}
	before, err = s.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if aerr := s.repo.AdjustOpening(ctx, id, newOpening); aerr != nil {
		if errors.Is(aerr, sql.ErrNoRows) {
			return nil, nil, apperr.NotFound("supplier")
		}
		return nil, nil, apperr.Internal("failed to adjust opening balance", aerr)
	}
	after, err = s.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return before, after, nil
}

// LedgerEntry is one line of a supplier's payable ledger.
type LedgerEntry struct {
	Date    time.Time
	Kind    string
	Ref     string
	Debit   decimal.Decimal
	Credit  decimal.Decimal
	Balance decimal.Decimal
}

// Statement is a supplier's full payable ledger with a forward running balance.
type Statement struct {
	Supplier    Supplier
	Entries     []LedgerEntry
	TotalDebit  decimal.Decimal
	TotalCredit decimal.Decimal
}

// Statement builds a supplier's payable ledger: the opening balance, purchase
// debits, and payment/return credits, time-ordered with a running balance. Like
// the customer statement it queries the transaction tables directly (no package
// import) and leans on the authoritative outstanding_balance — payments and
// returns are itemised from when their logging began.
func (s *Service) Statement(ctx context.Context, id int64) (*Statement, error) {
	sup, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	type evRow struct {
		CreatedAt time.Time       `db:"created_at"`
		Ref       string          `db:"ref"`
		Amount    decimal.Decimal `db:"amount"`
	}

	var purchases []evRow
	if err := s.db.SelectContext(ctx, &purchases, `
		SELECT created_at,
		       COALESCE(NULLIF(invoice_no, ''), 'PO #' || id::text) AS ref,
		       total AS amount
		FROM purchases
		WHERE supplier_id = $1 AND status <> 'draft'
		ORDER BY created_at`, id); err != nil {
		return nil, apperr.Internal("failed to load purchases", err)
	}
	var returns []evRow
	if err := s.db.SelectContext(ctx, &returns, `
		SELECT created_at, COALESCE(reference, '') AS ref, total AS amount
		FROM purchase_returns
		WHERE supplier_id = $1
		ORDER BY created_at`, id); err != nil {
		return nil, apperr.Internal("failed to load purchase returns", err)
	}
	type payRow struct {
		CreatedAt time.Time       `db:"created_at"`
		Method    string          `db:"method"`
		Reference *string         `db:"reference"`
		Amount    decimal.Decimal `db:"amount"`
	}
	var pays []payRow
	if err := s.db.SelectContext(ctx, &pays, `
		SELECT created_at, method, reference, amount
		FROM supplier_payments WHERE supplier_id = $1 ORDER BY created_at`, id); err != nil {
		return nil, apperr.Internal("failed to load supplier payments", err)
	}

	entries := make([]LedgerEntry, 0, len(purchases)+len(returns)+len(pays)+1)
	// The opening balance is the carried-forward figure from before this system: a
	// debit when we owed them, a credit when they owed us (a pre-system advance).
	if sup.OpeningBalance.IsPositive() {
		entries = append(entries, LedgerEntry{Date: sup.CreatedAt, Kind: "Opening balance", Debit: sup.OpeningBalance})
	} else if sup.OpeningBalance.IsNegative() {
		entries = append(entries, LedgerEntry{Date: sup.CreatedAt, Kind: "Opening balance", Credit: sup.OpeningBalance.Neg()})
	}
	for _, r := range purchases {
		entries = append(entries, LedgerEntry{Date: r.CreatedAt, Kind: "Purchase", Ref: r.Ref, Debit: r.Amount})
	}
	for _, r := range returns {
		entries = append(entries, LedgerEntry{Date: r.CreatedAt, Kind: "Return", Ref: r.Ref, Credit: r.Amount})
	}
	for _, r := range pays {
		ref := r.Method
		if r.Reference != nil && strings.TrimSpace(*r.Reference) != "" {
			ref += " · " + *r.Reference
		}
		entries = append(entries, LedgerEntry{Date: r.CreatedAt, Kind: "Payment", Ref: ref, Credit: r.Amount})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Date.Before(entries[j].Date) })

	bal, totalDebit, totalCredit := decimal.Zero, decimal.Zero, decimal.Zero
	for i := range entries {
		bal = bal.Add(entries[i].Debit).Sub(entries[i].Credit)
		entries[i].Balance = bal
		totalDebit = totalDebit.Add(entries[i].Debit)
		totalCredit = totalCredit.Add(entries[i].Credit)
	}
	return &Statement{Supplier: *sup, Entries: entries, TotalDebit: totalDebit, TotalCredit: totalCredit}, nil
}

type APIHandler struct{ svc *Service }

func NewAPIHandler(svc *Service) *APIHandler { return &APIHandler{svc: svc} }

func (h *APIHandler) List(c echo.Context) error {
	rows, err := h.svc.List(c.Request().Context(), c.QueryParam("search"))
	if err != nil {
		return err
	}
	return response.OK(c, rows)
}

func (h *APIHandler) Create(c echo.Context) error {
	var in CreateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	sup, err := h.svc.Create(c.Request().Context(), in)
	if err != nil {
		return err
	}
	return response.Created(c, sup)
}

func (h *APIHandler) Update(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	var in UpdateInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	if err := h.svc.Update(c.Request().Context(), id, in); err != nil {
		return err
	}
	return response.NoContent(c)
}

func (h *APIHandler) Delete(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apperr.BadRequest("invalid id")
	}
	if err := h.svc.Delete(c.Request().Context(), id); err != nil {
		return err
	}
	return response.NoContent(c)
}

func RegisterAPI(e *echo.Echo, db *sqlx.DB, cfg *config.Config) {
	api := NewAPIHandler(NewService(db))
	g := e.Group("/api/suppliers", middleware.JWTAuth(cfg.JWTSecret), middleware.RequireRole("admin", "manager"))
	g.GET("", api.List)
	g.POST("", api.Create)
	g.PUT("/:id", api.Update)
	g.DELETE("/:id", api.Delete)
}
