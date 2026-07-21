package auth

import (
	"context"
	"database/sql"
	"time"

	"karots-pos/internal/db"
)

type Repository struct{ db db.Queryer }

func NewRepository(q db.Queryer) *Repository { return &Repository{db: q} }

func (r *Repository) FindByID(ctx context.Context, id int64) (*User, error) {
	var u User
	err := r.db.GetContext(ctx, &u,
		`SELECT * FROM users WHERE id = $1 AND is_active = true`, id)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// FindByPhone looks up an active user by phone number for login.
func (r *Repository) FindByPhone(ctx context.Context, phone string) (*User, error) {
	var u User
	err := r.db.GetContext(ctx, &u,
		`SELECT * FROM users WHERE phone = $1 AND is_active = true`, phone)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// ListActivePublic returns active users for the login picker. The hidden system
// admin is excluded so it never shows on any screen.
func (r *Repository) ListActivePublic(ctx context.Context) ([]UserPublic, error) {
	var users []UserPublic
	err := r.db.SelectContext(ctx, &users,
		`SELECT id, name, role FROM users WHERE is_active = true AND is_system = false ORDER BY name`)
	return users, err
}

// ListAll returns every (non-system) user for the admin user panel.
func (r *Repository) ListAll(ctx context.Context) ([]User, error) {
	var users []User
	err := r.db.SelectContext(ctx, &users,
		`SELECT * FROM users WHERE is_system = false ORDER BY is_active DESC, name`)
	return users, err
}

// Create inserts a new user. mustChange arms the forced PIN-change flag — the
// caller decides based on the shop's "force PIN change" setting.
func (r *Repository) Create(ctx context.Context, name string, phone *string, role, pinHash, receiptPrinter string, mustChange, canSuppliers bool) (*User, error) {
	var u User
	err := r.db.GetContext(ctx, &u,
		`INSERT INTO users (name, phone, role, pin_hash, must_change_pin, receipt_printer, can_handle_suppliers)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING *`, name, phone, role, pinHash, mustChange, receiptPrinter, canSuppliers)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// Update edits a profile. The is_system guard means the hidden system admin can
// never be edited through the admin UI (0 rows → NotFound).
func (r *Repository) Update(ctx context.Context, id int64, name string, phone *string, role, receiptPrinter string, canSuppliers bool) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE users SET name = $1, phone = $2, role = $3, receipt_printer = $4, can_handle_suppliers = $5
		 WHERE id = $6 AND is_system = false`,
		name, phone, role, receiptPrinter, canSuppliers, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// UpdatePin sets a user's PIN (already bcrypt-hashed) and clears the
// must-change flag — the new PIN is one the user has now (re)established.
// Admin-driven resets re-arm the flag afterwards via SetMustChangePin.
func (r *Repository) UpdatePin(ctx context.Context, id int64, pinHash string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE users SET pin_hash = $1, must_change_pin = false WHERE id = $2`, pinHash, id)
	return err
}

// SetMustChangePin arms or clears the forced-change flag.
func (r *Repository) SetMustChangePin(ctx context.Context, id int64, must bool) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE users SET must_change_pin = $1 WHERE id = $2`, must, id)
	return err
}

// Deactivate guards the system admin: it can never be deactivated from the UI.
func (r *Repository) Deactivate(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE users SET is_active = false WHERE id = $1 AND is_system = false`, id)
	return err
}

func (r *Repository) Activate(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE users SET is_active = true WHERE id = $1`, id)
	return err
}

// --- refresh tokens ---

func (r *Repository) StoreRefresh(ctx context.Context, userID int64, hash string, expiresAt time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, hash, expiresAt)
	return err
}

func (r *Repository) FindRefresh(ctx context.Context, hash string) (*RefreshToken, error) {
	var t RefreshToken
	err := r.db.GetContext(ctx, &t,
		`SELECT * FROM refresh_tokens WHERE token_hash = $1`, hash)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *Repository) DeleteRefresh(ctx context.Context, hash string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE token_hash = $1`, hash)
	return err
}

func (r *Repository) DeleteAllRefreshForUser(ctx context.Context, userID int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE user_id = $1`, userID)
	return err
}

func (r *Repository) PurgeExpiredRefresh(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE expires_at < NOW()`)
	return err
}
