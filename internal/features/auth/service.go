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
	u, err := s.repo.Create(ctx, in.Name, &in.Phone, in.Role, string(hash))
	if err != nil {
		return nil, apperr.Internal("could not create user", err)
	}
	return u, nil
}

// GetUser loads a single active user (for the edit form).
func (s *Service) GetUser(ctx context.Context, id int64) (*User, error) {
	u, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("user")
		}
		return nil, apperr.Internal("could not load user", err)
	}
	return u, nil
}

// UpdateUser edits a user's name/phone/role and, when a new PIN is supplied,
// resets it. A PIN reset revokes the user's refresh tokens so old sessions die.
func (s *Service) UpdateUser(ctx context.Context, id int64, in UpdateUserInput) error {
	err := s.repo.Update(ctx, id, in.Name, &in.Phone, in.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFound("user")
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
		_ = s.repo.DeleteAllRefreshForUser(ctx, id)
	}
	return nil
}

func (s *Service) DeactivateUser(ctx context.Context, id int64) error {
	if err := s.repo.Deactivate(ctx, id); err != nil {
		return apperr.Internal("could not deactivate user", err)
	}
	return nil
}

func (s *Service) ReactivateUser(ctx context.Context, id int64) error {
	if err := s.repo.Activate(ctx, id); err != nil {
		return apperr.Internal("could not reactivate user", err)
	}
	return nil
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
