package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"karots-pos/internal/middleware"

	"github.com/golang-jwt/jwt/v5"
)

// issueAccess mints a signed JWT access token carrying the user's identity and
// role, expiring after accessTTL.
func (s *Service) issueAccess(u *User, now time.Time) (string, time.Time, error) {
	exp := now.Add(s.accessTTL)
	claims := middleware.Claims{
		UserID: u.ID,
		Role:   u.Role,
		Name:   u.Name,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(u.ID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(s.secret)
	return signed, exp, err
}

// newRefreshToken returns a high-entropy raw token (given to the client) and
// its SHA-256 hash (stored in the DB).
func newRefreshToken() (raw, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	raw = hex.EncodeToString(b)
	return raw, hashToken(raw), nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
