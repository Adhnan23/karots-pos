package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
	"golang.org/x/crypto/bcrypt"
)

// Clock lets tests inject deterministic time; production uses time.Now.
type Clock func() time.Time

type Service struct {
	db         *sqlx.DB
	repo       *Repository
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	now        Clock
}

func NewService(db *sqlx.DB, secret string, accessTTL, refreshTTL time.Duration) *Service {
	return &Service{
		db:         db,
		repo:       NewRepository(db),
		secret:     []byte(secret),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
		now:        time.Now,
	}
}

func (s *Service) LoginUsers(ctx context.Context) ([]UserPublic, error) {
	users, err := s.repo.ListActivePublic(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to load users", err)
	}
	return users, nil
}

func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	users, err := s.repo.ListAll(ctx)
	if err != nil {
		return nil, apperr.Internal("failed to load users", err)
	}
	return users, nil
}

// Login verifies a user's PIN and issues a fresh access + refresh token pair.
// The error message is intentionally generic to avoid revealing whether the
// phone number or the PIN was wrong.
func (s *Service) Login(ctx context.Context, in LoginInput) (*TokenPair, error) {
	u, err := s.repo.FindByPhone(ctx, in.Phone)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.Unauthorized("invalid credentials")
		}
		return nil, apperr.Internal("login failed", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PinHash), []byte(in.PIN)) != nil {
		return nil, apperr.Unauthorized("invalid credentials")
	}
	return s.issuePair(ctx, u)
}

// VerifyCredentials checks a phone + PIN and returns the matching user's public
// identity WITHOUT issuing any token — used by the screen-unlock flow, which must
// not rotate the session (the existing token is only re-signed with the lock flag
// cleared). Returns an Unauthorized apperr on a bad phone or PIN.
func (s *Service) VerifyCredentials(ctx context.Context, in LoginInput) (*UserPublic, error) {
	u, err := s.repo.FindByPhone(ctx, in.Phone)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.Unauthorized("invalid credentials")
		}
		return nil, apperr.Internal("unlock failed", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PinHash), []byte(in.PIN)) != nil {
		return nil, apperr.Unauthorized("invalid credentials")
	}
	return &UserPublic{ID: u.ID, Name: u.Name, Role: u.Role}, nil
}

// Refresh rotates a refresh token: the presented token is consumed and a new
// pair is issued. A reused or unknown token is rejected.
func (s *Service) Refresh(ctx context.Context, raw string) (*TokenPair, error) {
	hash := hashToken(raw)
	rt, err := s.repo.FindRefresh(ctx, hash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.Unauthorized("invalid refresh token")
		}
		return nil, apperr.Internal("refresh failed", err)
	}
	if s.now().After(rt.ExpiresAt) {
		_ = s.repo.DeleteRefresh(ctx, hash)
		return nil, apperr.Unauthorized("refresh token expired")
	}
	u, err := s.repo.FindByID(ctx, rt.UserID)
	if err != nil {
		return nil, apperr.Unauthorized("account is no longer active")
	}

	var pair *TokenPair
	err = appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		txRepo := NewRepository(tx)
		if err := txRepo.DeleteRefresh(ctx, hash); err != nil {
			return err
		}
		var ierr error
		pair, ierr = s.issuePairTx(ctx, txRepo, u)
		return ierr
	})
	if err != nil {
		return nil, apperr.Internal("refresh failed", err)
	}
	return pair, nil
}

func (s *Service) Logout(ctx context.Context, raw string) error {
	if raw == "" {
		return nil
	}
	if err := s.repo.DeleteRefresh(ctx, hashToken(raw)); err != nil {
		return apperr.Internal("logout failed", err)
	}
	return nil
}

func (s *Service) CreateUser(ctx context.Context, in CreateUserInput) (*User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(in.PIN), bcrypt.DefaultCost)
	if err != nil {
		return nil, apperr.Internal("could not hash PIN", err)
	}
	u, err := s.repo.Create(ctx, in.Name, &in.Phone, in.Role, string(hash), in.ReceiptPrinter, s.forcePinChange(ctx), checkboxOn(in.CanHandleSuppliers))
	if err != nil {
		if appdb.IsUniqueViolation(err) {
			return nil, apperr.Conflict("that phone number is already used by another user")
		}
		return nil, apperr.Internal("could not create user", err)
	}
	return u, nil
}

// GetUser loads a single active user (for the edit form). The hidden system
// admin is reported as not found so it can't be opened via a hand-typed URL.
func (s *Service) GetUser(ctx context.Context, id int64) (*User, error) {
	u, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("user")
		}
		return nil, apperr.Internal("could not load user", err)
	}
	if u.IsSystem {
		return nil, apperr.NotFound("user")
	}
	return u, nil
}

// forcePinChange reports whether new/admin-reset PINs must be changed on next
// login, per the shop setting. Defaults to false (no forced change) on any error.
func (s *Service) forcePinChange(ctx context.Context) bool {
	var v bool
	if err := s.db.GetContext(ctx, &v, `SELECT force_pin_change FROM settings WHERE id = 1`); err != nil {
		return false
	}
	return v
}

// AllowCashierPinChange reports whether cashiers may change their own PIN, per
// the shop setting. Defaults to true (allowed) on any error.
func (s *Service) AllowCashierPinChange(ctx context.Context) bool {
	var v bool
	if err := s.db.GetContext(ctx, &v, `SELECT allow_cashier_pin_change FROM settings WHERE id = 1`); err != nil {
		return true
	}
	return v
}

// UpdateUser edits a user's name/phone/role and, when a new PIN is supplied,
// resets it. A PIN reset revokes the user's refresh tokens so old sessions die.
func (s *Service) UpdateUser(ctx context.Context, id int64, in UpdateUserInput) error {
	err := s.repo.Update(ctx, id, in.Name, &in.Phone, in.Role, in.ReceiptPrinter, checkboxOn(in.CanHandleSuppliers))
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFound("user")
	}
	if appdb.IsUniqueViolation(err) {
		return apperr.Conflict("that phone number is already used by another user")
	}
	if err != nil {
		return apperr.Internal("could not update user", err)
	}
	if strings.TrimSpace(in.PIN) != "" {
		hash, herr := bcrypt.GenerateFromPassword([]byte(in.PIN), bcrypt.DefaultCost)
		if herr != nil {
			return apperr.Internal("could not hash PIN", herr)
		}
		if uerr := s.repo.UpdatePin(ctx, id, string(hash)); uerr != nil {
			return apperr.Internal("could not reset PIN", uerr)
		}
		// An admin-set PIN is not one the user chose. Force a change on next
		// login only when the shop has opted into that policy.
		if s.forcePinChange(ctx) {
			if uerr := s.repo.SetMustChangePin(ctx, id, true); uerr != nil {
				return apperr.Internal("could not flag PIN reset", uerr)
			}
		}
		_ = s.repo.DeleteAllRefreshForUser(ctx, id)
	}
	return nil
}

// ChangeOwnPIN lets the authenticated user replace their own PIN. It verifies
// the current PIN, enforces a different new PIN, and clears the forced-change
// flag (via UpdatePin). The fresh user is returned so the caller can mint a new
// session token carrying the cleared flag.
func (s *Service) ChangeOwnPIN(ctx context.Context, userID int64, in ChangeOwnPINInput) (*User, error) {
	if in.NewPIN != in.ConfirmPIN {
		return nil, apperr.BadRequest("the new PIN and confirmation do not match")
	}
	if in.NewPIN == in.CurrentPIN {
		return nil, apperr.BadRequest("the new PIN must be different from the current one")
	}
	u, err := s.repo.FindByID(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.Unauthorized("account is no longer active")
		}
		return nil, apperr.Internal("could not load account", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PinHash), []byte(in.CurrentPIN)) != nil {
		return nil, apperr.BadRequest("your current PIN is incorrect")
	}
	hash, herr := bcrypt.GenerateFromPassword([]byte(in.NewPIN), bcrypt.DefaultCost)
	if herr != nil {
		return nil, apperr.Internal("could not hash PIN", herr)
	}
	if uerr := s.repo.UpdatePin(ctx, userID, string(hash)); uerr != nil {
		return nil, apperr.Internal("could not change PIN", uerr)
	}
	// Old sessions on the previous PIN should die.
	_ = s.repo.DeleteAllRefreshForUser(ctx, userID)
	u.MustChangePin = false
	u.PinHash = string(hash)
	return u, nil
}

func (s *Service) DeactivateUser(ctx context.Context, id int64) error {
	if err := s.repo.Deactivate(ctx, id); err != nil {
		return apperr.Internal("could not deactivate user", err)
	}
	return nil
}

func (s *Service) ReactivateUser(ctx context.Context, id int64) error {
	if err := s.repo.Activate(ctx, id); err != nil {
		// phone is unique among active users — reactivating into a collision fails.
		if appdb.IsUniqueViolation(err) {
			return apperr.Conflict("another active user already uses that phone number")
		}
		return apperr.Internal("could not reactivate user", err)
	}
	return nil
}

// AccessTokenFor mints a standalone access token for an already-authenticated
// user — used to refresh the UI cookie after a self-service change (e.g. a PIN
// change that must clear the forced-change claim).
func (s *Service) AccessTokenFor(u *User) (string, error) {
	access, _, err := s.issueAccess(u, s.now())
	if err != nil {
		return "", apperr.Internal("could not issue token", err)
	}
	return access, nil
}

func (s *Service) issuePair(ctx context.Context, u *User) (*TokenPair, error) {
	pair, err := s.issuePairTx(ctx, s.repo, u)
	if err != nil {
		return nil, apperr.Internal("could not issue tokens", err)
	}
	return pair, nil
}

func (s *Service) issuePairTx(ctx context.Context, repo *Repository, u *User) (*TokenPair, error) {
	now := s.now()
	access, exp, err := s.issueAccess(u, now)
	if err != nil {
		return nil, err
	}
	raw, hash, err := newRefreshToken()
	if err != nil {
		return nil, err
	}
	if err := repo.StoreRefresh(ctx, u.ID, hash, now.Add(s.refreshTTL)); err != nil {
		return nil, err
	}
	return &TokenPair{
		AccessToken:  access,
		RefreshToken: raw,
		ExpiresAt:    exp,
		User:         UserPublic{ID: u.ID, Name: u.Name, Role: u.Role},
	}, nil
}
