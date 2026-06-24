// Package cashregister manages daily cash-drawer sessions. Exactly one session
// per cashier can be open at a time (enforced by a partial unique index).
//
// The drawer is counted by denomination (how many of each note/coin) at open and
// close. Every cash event during a session — opening float, mid-shift
// withdrawals and pay-ins, credit collected, and the close — is written to the
// cash_movements ledger so the whole day is auditable and rolls into finance.
//
// Expected cash at any moment = opening float + cash sales collected + pay-ins
// and credit collected − withdrawals/refunds. The close compares this to the
// counted drawer and records the over/short difference.
package cashregister

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/config"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/sales"
	"karots-pos/internal/middleware"
	"karots-pos/internal/money"
	"karots-pos/internal/response"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

// Movement types (mirror the cash_movement_type enum).
const (
	MoveOpening       = "opening"
	MoveSale          = "sale"
	MoveCreditPayment = "credit_payment"
	MoveWithdrawal    = "withdrawal"
	MovePayIn         = "pay_in"
	MoveRefund        = "refund"
	MoveClosing       = "closing"
)

type Session struct {
	ID               int64            `db:"id"                json:"id"`
	UserID           int64            `db:"user_id"           json:"user_id"`
	OpeningCash      decimal.Decimal  `db:"opening_cash"      json:"opening_cash"`
	ClosingCash      *decimal.Decimal `db:"closing_cash"      json:"closing_cash,omitempty"`
	ExpectedCash     *decimal.Decimal `db:"expected_cash"     json:"expected_cash,omitempty"`
	Difference       *decimal.Decimal `db:"difference"        json:"difference,omitempty"`
	OpeningBreakdown []byte           `db:"opening_breakdown" json:"opening_breakdown,omitempty"`
	ClosingBreakdown []byte           `db:"closing_breakdown" json:"closing_breakdown,omitempty"`
	OpenedAt         time.Time        `db:"opened_at"         json:"opened_at"`
	ClosedAt         *time.Time       `db:"closed_at"         json:"closed_at,omitempty"`
}

type Movement struct {
	ID        int64           `db:"id"         json:"id"`
	SessionID int64           `db:"session_id" json:"session_id"`
	UserID    int64           `db:"user_id"    json:"user_id"`
	Type      string          `db:"type"       json:"type"`
	Amount    decimal.Decimal `db:"amount"     json:"amount"`
	Reason    *string         `db:"reason"     json:"reason,omitempty"`
	Breakdown []byte          `db:"breakdown"  json:"-"`
	CreatedAt time.Time       `db:"created_at" json:"created_at"`
}

// SessionRow is a session joined with the cashier name, for admin listings.
type SessionRow struct {
	Session
	UserName string `db:"user_name" json:"user_name"`
}

type OpenInput struct {
	OpeningCash string          `json:"opening_cash" form:"opening_cash"`
	Breakdown   json.RawMessage `json:"breakdown"`
}

type CloseInput struct {
	ClosingCash string          `json:"closing_cash" form:"closing_cash"`
	Breakdown   json.RawMessage `json:"breakdown"`
}

// MovementInput is a mid-shift withdrawal or pay-in.
type MovementInput struct {
	Amount string `json:"amount" form:"amount" validate:"required"`
	Reason string `json:"reason" form:"reason"`
}

// CloseResult is returned after closing so the UI can show the reconciliation.
type CloseResult struct {
	Session      Session         `json:"session"`
	CashSales    decimal.Decimal `json:"cash_sales"`
	ExpectedCash decimal.Decimal `json:"expected_cash"`
	Difference   decimal.Decimal `json:"difference"`
}

// Summary is the live state of the open drawer, for the cashier terminal.
type Summary struct {
	Session       *Session         `json:"session"`
	CashSales     decimal.Decimal  `json:"cash_sales"`
	PayIns        decimal.Decimal  `json:"pay_ins"`     // pay-ins + credit collected
	Withdrawals   decimal.Decimal  `json:"withdrawals"` // positive magnitude
	Expected      decimal.Decimal  `json:"expected"`
	Movements     []Movement       `json:"movements"`
	LastClosing   *decimal.Decimal `json:"last_closing,omitempty"`
	LastBreakdown []byte           `json:"last_breakdown,omitempty"`
}

type Repository struct{ q appdb.Queryer }

func NewRepository(q appdb.Queryer) *Repository { return &Repository{q: q} }

func (r *Repository) FindOpen(ctx context.Context, userID int64) (*Session, error) {
	var s Session
	err := r.q.GetContext(ctx, &s,
		`SELECT * FROM cash_register WHERE user_id = $1 AND closed_at IS NULL`, userID)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// FindOpenForUpdate is FindOpen with a row lock, for use inside a transaction
// that moves cash to/from this drawer (cashflow.Move). The lock serialises
// concurrent withdrawals so two can't both pass the overdraw guard.
func (r *Repository) FindOpenForUpdate(ctx context.Context, userID int64) (*Session, error) {
	var s Session
	err := r.q.GetContext(ctx, &s,
		`SELECT * FROM cash_register WHERE user_id = $1 AND closed_at IS NULL FOR UPDATE`, userID)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// LastClosed returns the cashier's most recently closed session (for the
// "continue with last amount" option when reopening).
func (r *Repository) LastClosed(ctx context.Context, userID int64) (*Session, error) {
	var s Session
	err := r.q.GetContext(ctx, &s,
		`SELECT * FROM cash_register WHERE user_id = $1 AND closed_at IS NOT NULL
		 ORDER BY closed_at DESC LIMIT 1`, userID)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *Repository) Open(ctx context.Context, userID int64, opening decimal.Decimal, breakdown []byte) (*Session, error) {
	var s Session
	err := r.q.GetContext(ctx, &s,
		`INSERT INTO cash_register (user_id, opening_cash, opening_breakdown)
		 VALUES ($1, $2, $3) RETURNING *`,
		userID, opening, nullJSON(breakdown))
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *Repository) Close(ctx context.Context, id int64, closing, expected, diff decimal.Decimal, breakdown []byte) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE cash_register
		SET closing_cash = $1, expected_cash = $2, difference = $3, closing_breakdown = $4, closed_at = NOW()
		WHERE id = $5`, closing, expected, diff, nullJSON(breakdown), id)
	return err
}

func (r *Repository) AddMovement(ctx context.Context, sessionID, userID int64, mtype string, amount decimal.Decimal, reason string, breakdown []byte) error {
	var rp *string
	if strings.TrimSpace(reason) != "" {
		rp = &reason
	}
	_, err := r.q.ExecContext(ctx,
		`INSERT INTO cash_movements (session_id, user_id, type, amount, reason, breakdown)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		sessionID, userID, mtype, amount, rp, nullJSON(breakdown))
	return err
}

func (r *Repository) ListMovements(ctx context.Context, sessionID int64) ([]Movement, error) {
	var rows []Movement
	err := r.q.SelectContext(ctx, &rows,
		`SELECT * FROM cash_movements WHERE session_id = $1 ORDER BY created_at`, sessionID)
	return rows, err
}

// AdjustmentTotal sums the in/out movements that change expected drawer cash
// (everything except the opening/closing/sale audit rows).
func (r *Repository) AdjustmentTotal(ctx context.Context, sessionID int64) (decimal.Decimal, error) {
	var total decimal.Decimal
	err := r.q.GetContext(ctx, &total,
		`SELECT COALESCE(SUM(amount),0) FROM cash_movements
		 WHERE session_id = $1 AND type IN ('withdrawal','pay_in','credit_payment','refund')`, sessionID)
	return total, err
}

func (r *Repository) RecentSessions(ctx context.Context, limit int) ([]SessionRow, error) {
	var rows []SessionRow
	err := r.q.SelectContext(ctx, &rows, `
		SELECT cr.*, u.name AS user_name
		FROM cash_register cr JOIN users u ON u.id = cr.user_id
		ORDER BY cr.opened_at DESC LIMIT $1`, limit)
	return rows, err
}

// OpenSessions lists every currently-open till (closed_at IS NULL) with its
// cashier name — for pickers that move cash to/from a specific drawer.
func (r *Repository) OpenSessions(ctx context.Context) ([]SessionRow, error) {
	var rows []SessionRow
	err := r.q.SelectContext(ctx, &rows, `
		SELECT cr.*, u.name AS user_name
		FROM cash_register cr JOIN users u ON u.id = cr.user_id
		WHERE cr.closed_at IS NULL
		ORDER BY cr.opened_at`)
	return rows, err
}

// SessionsInRange lists sessions opened within [from,to) for the period report.
func (r *Repository) SessionsInRange(ctx context.Context, from, to time.Time) ([]SessionRow, error) {
	var rows []SessionRow
	err := r.q.SelectContext(ctx, &rows, `
		SELECT cr.*, u.name AS user_name
		FROM cash_register cr JOIN users u ON u.id = cr.user_id
		WHERE cr.opened_at >= $1 AND cr.opened_at < $2
		ORDER BY cr.opened_at DESC`, from, to)
	return rows, err
}

func (r *Repository) FindByID(ctx context.Context, id int64) (*SessionRow, error) {
	var s SessionRow
	err := r.q.GetContext(ctx, &s, `
		SELECT cr.*, u.name AS user_name
		FROM cash_register cr JOIN users u ON u.id = cr.user_id WHERE cr.id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

type Service struct {
	db    *sqlx.DB
	repo  *Repository
	sales *sales.Service
	audit *audit.Service // optional; nil = no audit recording
}

func NewService(db *sqlx.DB, salesSvc *sales.Service) *Service {
	return &Service{db: db, repo: NewRepository(db), sales: salesSvc}
}

// WithAudit attaches an audit recorder so drawer withdrawals and closes are
// logged. Returns the service for chaining.
func (s *Service) WithAudit(a *audit.Service) *Service {
	s.audit = a
	return s
}

func (s *Service) recordAudit(ctx context.Context, userID int64, action, detail string) {
	if s.audit != nil {
		s.audit.Record(ctx, userID, action, "cash", "", detail)
	}
}

// Current returns the cashier's open session, or nil if none is open.
func (s *Service) Current(ctx context.Context, userID int64) (*Session, error) {
	sess, err := s.repo.FindOpen(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, apperr.Internal("failed to load session", err)
	}
	return sess, nil
}

// Summary returns the live drawer state (or, when no session is open, the last
// closing so the UI can offer "continue with last amount").
func (s *Service) Summary(ctx context.Context, userID int64) (*Summary, error) {
	out := &Summary{}
	sess, err := s.repo.FindOpen(ctx, userID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.Internal("failed to load session", err)
		}
		// No open session — surface the last close for the reopen options.
		if last, lerr := s.repo.LastClosed(ctx, userID); lerr == nil {
			out.LastClosing = last.ClosingCash
			out.LastBreakdown = last.ClosingBreakdown
		}
		return out, nil
	}
	out.Session = sess
	out.CashSales, err = s.sales.CashCollectedSince(ctx, userID, sess.OpenedAt)
	if err != nil {
		return nil, err
	}
	moves, err := s.repo.ListMovements(ctx, sess.ID)
	if err != nil {
		return nil, apperr.Internal("failed to load cash movements", err)
	}
	out.Movements = moves
	for _, m := range moves {
		switch m.Type {
		case MovePayIn, MoveCreditPayment:
			out.PayIns = out.PayIns.Add(m.Amount)
		case MoveWithdrawal, MoveRefund:
			out.Withdrawals = out.Withdrawals.Add(m.Amount.Abs())
		}
	}
	out.Expected = sess.OpeningCash.Add(out.CashSales).Add(out.PayIns).Sub(out.Withdrawals)
	return out, nil
}

func (s *Service) Open(ctx context.Context, userID int64, in OpenInput) (*Session, error) {
	opening, raw, ok := amountFrom(in.OpeningCash, in.Breakdown)
	if !ok || opening.IsNegative() {
		return nil, apperr.Validation("opening cash must be a non-negative amount")
	}
	var sess *Session
	err := appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		repo := NewRepository(tx)
		var ierr error
		sess, ierr = repo.Open(ctx, userID, opening, raw)
		if ierr != nil {
			return ierr
		}
		return repo.AddMovement(ctx, sess.ID, userID, MoveOpening, opening, "opening float", raw)
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "idx_cash_register_open_session") {
			return nil, apperr.Conflict("you already have an open register session")
		}
		return nil, apperr.Internal("failed to open register", err)
	}
	return sess, nil
}

func (s *Service) Close(ctx context.Context, userID int64, in CloseInput) (*CloseResult, error) {
	closing, raw, ok := amountFrom(in.ClosingCash, in.Breakdown)
	if !ok || closing.IsNegative() {
		return nil, apperr.Validation("closing cash must be a non-negative amount")
	}
	sess, err := s.repo.FindOpen(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.Conflict("no open register session to close")
		}
		return nil, apperr.Internal("failed to load session", err)
	}
	cashSales, err := s.sales.CashCollectedSince(ctx, userID, sess.OpenedAt)
	if err != nil {
		return nil, err
	}
	adj, err := s.repo.AdjustmentTotal(ctx, sess.ID)
	if err != nil {
		return nil, apperr.Internal("failed to total cash movements", err)
	}
	expected := sess.OpeningCash.Add(cashSales).Add(adj)
	diff := closing.Sub(expected)
	err = appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		repo := NewRepository(tx)
		if cerr := repo.Close(ctx, sess.ID, closing, expected, diff, raw); cerr != nil {
			return cerr
		}
		return repo.AddMovement(ctx, sess.ID, userID, MoveClosing, closing, "counted at close", raw)
	})
	if err != nil {
		return nil, apperr.Internal("failed to close register", err)
	}
	sess.ClosingCash = &closing
	sess.ExpectedCash = &expected
	sess.Difference = &diff
	s.recordAudit(ctx, userID, audit.ActionClose,
		"closed register: counted "+money.Display(closing)+", expected "+money.Display(expected)+", over/short "+money.Display(diff))
	return &CloseResult{Session: *sess, CashSales: cashSales, ExpectedCash: expected, Difference: diff}, nil
}

// Withdraw records cash taken out of the drawer mid-shift (e.g. banked, paid to
// a supplier). Stored as a negative movement so it lowers expected cash.
func (s *Service) Withdraw(ctx context.Context, userID int64, in MovementInput) (*Summary, error) {
	return s.adjust(ctx, userID, in, MoveWithdrawal, true, "withdrawal")
}

// PayIn records cash added to the drawer mid-shift (e.g. extra float).
func (s *Service) PayIn(ctx context.Context, userID int64, in MovementInput) (*Summary, error) {
	return s.adjust(ctx, userID, in, MovePayIn, false, "pay-in")
}

func (s *Service) adjust(ctx context.Context, userID int64, in MovementInput, mtype string, negate bool, label string) (*Summary, error) {
	amt, err := money.Parse(in.Amount)
	if err != nil || !amt.IsPositive() {
		return nil, apperr.Validation(label + " amount must be positive")
	}
	sess, err := s.repo.FindOpen(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.Conflict("open a register session first")
		}
		return nil, apperr.Internal("failed to load session", err)
	}
	// A withdrawal can never exceed the cash currently in the drawer (opening +
	// cash sales + net adjustments — the same balance the close reconciles to).
	if mtype == MoveWithdrawal {
		cashSales, cerr := s.sales.CashCollectedSince(ctx, userID, sess.OpenedAt)
		if cerr != nil {
			return nil, cerr
		}
		adj, aerr := s.repo.AdjustmentTotal(ctx, sess.ID)
		if aerr != nil {
			return nil, apperr.Internal("failed to total cash movements", aerr)
		}
		available := sess.OpeningCash.Add(cashSales).Add(adj)
		if amt.GreaterThan(available) {
			return nil, apperr.Validation("cannot withdraw more than the " + money.Display(available) + " currently in the drawer")
		}
	}
	signed := amt
	if negate {
		signed = amt.Neg()
	}
	if err := s.repo.AddMovement(ctx, sess.ID, userID, mtype, signed, in.Reason, nil); err != nil {
		return nil, apperr.Internal("failed to record cash movement", err)
	}
	if mtype == MoveWithdrawal {
		s.recordAudit(ctx, userID, audit.ActionWithdraw,
			"withdrew "+money.Display(amt)+" — "+in.Reason)
	}
	return s.Summary(ctx, userID)
}

// RecordCreditCash logs cash collected against a customer's credit into the
// cashier's open drawer. It is a no-op when the cashier has no open session, so
// callers (the credit-collection page) don't need to special-case the drawer.
func (s *Service) RecordCreditCash(ctx context.Context, userID int64, amount decimal.Decimal, reason string) {
	if !amount.IsPositive() {
		return
	}
	sess, err := s.repo.FindOpen(ctx, userID)
	if err != nil {
		return
	}
	_ = s.repo.AddMovement(ctx, sess.ID, userID, MoveCreditPayment, amount, reason, nil)
}

// RecordRefundCash logs a cash refund paid out of the cashier's open drawer for
// a return (negative, lowering expected cash). Like the other Record* helpers it
// is a no-op when no session is open or the amount isn't positive, so the return
// handler doesn't need to special-case the drawer.
func (s *Service) RecordRefundCash(ctx context.Context, userID int64, amount decimal.Decimal, reason string) {
	if !amount.IsPositive() {
		return
	}
	sess, err := s.repo.FindOpen(ctx, userID)
	if err != nil {
		return
	}
	_ = s.repo.AddMovement(ctx, sess.ID, userID, MoveRefund, amount.Neg(), reason, nil)
}

// RecordSupplierCash logs cash paid out to a supplier from the cashier's open
// drawer as a withdrawal (negative, lowering expected cash). Like
// RecordCreditCash it is a no-op when no session is open, so the supplier-pay
// page doesn't need to special-case the drawer.
func (s *Service) RecordSupplierCash(ctx context.Context, userID int64, amount decimal.Decimal, reason string) {
	if !amount.IsPositive() {
		return
	}
	sess, err := s.repo.FindOpen(ctx, userID)
	if err != nil {
		return
	}
	_ = s.repo.AddMovement(ctx, sess.ID, userID, MoveWithdrawal, amount.Neg(), reason, nil)
}

func (s *Service) RecentSessions(ctx context.Context, limit int) ([]SessionRow, error) {
	rows, err := s.repo.RecentSessions(ctx, limit)
	if err != nil {
		return nil, apperr.Internal("failed to list register sessions", err)
	}
	return rows, nil
}

// OpenSessions lists currently-open tills (for moving cash to/from a drawer).
func (s *Service) OpenSessions(ctx context.Context) ([]SessionRow, error) {
	rows, err := s.repo.OpenSessions(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to list open tills", err)
	}
	return rows, nil
}

func (s *Service) SessionsInRange(ctx context.Context, from, to time.Time) ([]SessionRow, error) {
	rows, err := s.repo.SessionsInRange(ctx, from, to)
	if err != nil {
		return nil, apperr.Internal("failed to list register sessions", err)
	}
	return rows, nil
}

func (s *Service) SessionDetail(ctx context.Context, id int64) (*SessionRow, []Movement, error) {
	sess, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, apperr.NotFound("register session")
		}
		return nil, nil, apperr.Internal("failed to load session", err)
	}
	moves, err := s.repo.ListMovements(ctx, id)
	if err != nil {
		return nil, nil, apperr.Internal("failed to load cash movements", err)
	}
	return sess, moves, nil
}

type APIHandler struct{ svc *Service }

func NewAPIHandler(svc *Service) *APIHandler { return &APIHandler{svc: svc} }

func (h *APIHandler) Current(c echo.Context) error {
	sess, err := h.svc.Current(c.Request().Context(), middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	return response.OK(c, sess)
}

func (h *APIHandler) Summary(c echo.Context) error {
	sum, err := h.svc.Summary(c.Request().Context(), middleware.CurrentUserID(c))
	if err != nil {
		return err
	}
	return response.OK(c, sum)
}

func (h *APIHandler) Open(c echo.Context) error {
	var in OpenInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	sess, err := h.svc.Open(c.Request().Context(), middleware.CurrentUserID(c), in)
	if err != nil {
		return err
	}
	return response.Created(c, sess)
}

func (h *APIHandler) Close(c echo.Context) error {
	var in CloseInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	res, err := h.svc.Close(c.Request().Context(), middleware.CurrentUserID(c), in)
	if err != nil {
		return err
	}
	return response.OK(c, res)
}

func (h *APIHandler) Withdraw(c echo.Context) error {
	var in MovementInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	sum, err := h.svc.Withdraw(c.Request().Context(), middleware.CurrentUserID(c), in)
	if err != nil {
		return err
	}
	return response.OK(c, sum)
}

func (h *APIHandler) PayIn(c echo.Context) error {
	var in MovementInput
	if err := c.Bind(&in); err != nil {
		return apperr.BadRequest("invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return err
	}
	sum, err := h.svc.PayIn(c.Request().Context(), middleware.CurrentUserID(c), in)
	if err != nil {
		return err
	}
	return response.OK(c, sum)
}

func RegisterAPI(e *echo.Echo, db *sqlx.DB, cfg *config.Config, salesSvc *sales.Service, auditSvc *audit.Service) *Service {
	svc := NewService(db, salesSvc).WithAudit(auditSvc)
	api := NewAPIHandler(svc)
	g := e.Group("/api/cash-register", middleware.JWTAuth(cfg.JWTSecret))
	g.GET("/current", api.Current)
	g.GET("/summary", api.Summary)
	g.POST("/open", api.Open)
	g.POST("/close", api.Close)
	g.POST("/withdraw", api.Withdraw)
	g.POST("/pay-in", api.PayIn)
	return svc
}

// amountFrom resolves the drawer amount: prefer the denomination breakdown (the
// authoritative count) and fall back to a typed total. Returns the canonical
// breakdown JSON to persist (nil when none was supplied).
func amountFrom(typed string, breakdown json.RawMessage) (decimal.Decimal, []byte, bool) {
	if total, raw, ok := breakdownTotal(breakdown); ok {
		return total, raw, true
	}
	if strings.TrimSpace(typed) == "" {
		return decimal.Zero, nil, false
	}
	v, err := money.Parse(typed)
	if err != nil {
		return decimal.Zero, nil, false
	}
	return v, nil, true
}

// breakdownTotal sums value×qty over a {denominationValue: count} map.
func breakdownTotal(raw json.RawMessage) (decimal.Decimal, []byte, bool) {
	if len(raw) == 0 {
		return decimal.Zero, nil, false
	}
	m := map[string]int{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return decimal.Zero, nil, false
	}
	total := decimal.Zero
	any := false
	clean := map[string]int{}
	for k, qty := range m {
		if qty <= 0 {
			continue
		}
		v, err := decimal.NewFromString(k)
		if err != nil || !v.IsPositive() {
			continue
		}
		total = total.Add(v.Mul(decimal.NewFromInt(int64(qty))))
		clean[k] = qty
		any = true
	}
	if !any {
		// An all-zero count is still a valid (empty) drawer.
		return decimal.Zero, []byte("{}"), true
	}
	out, _ := json.Marshal(clean)
	return total, out, true
}

func nullJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
