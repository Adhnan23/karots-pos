package middleware

import (
	"context"
	"net/http"
	"strings"

	"karots-pos/internal/apperr"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

// UserValidator reports whether the user behind a verified token may still act —
// i.e. the account still exists and is active. It is injected so this package
// keeps no dependency on the auth feature.
type UserValidator func(ctx context.Context, userID int64) bool

// userValidator is set once at startup (SetUserValidator) and consulted by every
// JWTAuth instance. A package-level hook keeps JWTAuth's signature unchanged, so
// the many feature API routes that build their own JWTAuth all get the check for
// free. When nil, tokens are trusted on signature + expiry alone.
var userValidator UserValidator

// SetUserValidator installs the account-still-active check used by all JWTAuth
// middleware. Call it once during startup, before serving requests.
func SetUserValidator(v UserValidator) { userValidator = v }

const (
	ctxUserID        = "user_id"
	ctxRole          = "role"
	ctxName          = "user_name"
	ctxMustChangePin = "must_change_pin"
	ctxLocked        = "locked"

	// CookieName is the httpOnly cookie that carries the access token for the
	// server-rendered UI.
	CookieName = "pos_token"
)

// Claims is the JWT payload. Kept here (not in the auth feature) so middleware
// has no dependency on feature packages.
type Claims struct {
	UserID        int64  `json:"uid"`
	Role          string `json:"role"`
	Name          string `json:"name"`
	MustChangePin bool   `json:"mcp,omitempty"`
	Locked        bool   `json:"lck,omitempty"`
	jwt.RegisteredClaims
}

// JWTAuth authenticates a request from either a Bearer header (API clients) or
// the httpOnly cookie (UI). On failure it returns an apperr the error handler
// converts to a 401/redirect; this keeps the middleware free of rendering
// concerns.
func JWTAuth(secret string) echo.MiddlewareFunc {
	key := []byte(secret)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			tokenStr := extractToken(c)
			if tokenStr == "" {
				return apperr.Unauthorized("")
			}
			claims := &Claims{}
			_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, apperr.Unauthorized("invalid signing method")
				}
				return key, nil
			})
			if err != nil {
				return apperr.Unauthorized("invalid or expired session")
			}
			// A valid signature isn't enough: the account may have been deleted or
			// deactivated since the token was issued. Reject it so a fired/disabled
			// user is forced out instead of riding their cookie until it expires.
			if userValidator != nil && !userValidator(c.Request().Context(), claims.UserID) {
				return apperr.Unauthorized("your account is no longer active — please sign in again")
			}
			c.Set(ctxUserID, claims.UserID)
			c.Set(ctxRole, claims.Role)
			c.Set(ctxName, claims.Name)
			c.Set(ctxMustChangePin, claims.MustChangePin)
			c.Set(ctxLocked, claims.Locked)
			return next(c)
		}
	}
}

func extractToken(c echo.Context) string {
	if h := c.Request().Header.Get(echo.HeaderAuthorization); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if cookie, err := c.Cookie(CookieName); err == nil {
		return cookie.Value
	}
	return ""
}

// ParseClaims validates a token's signature + expiry and returns its claims. It
// does NOT run the user-active check, so it is suitable for handlers that are
// intentionally ungated (e.g. logout) but still want to know who the caller is.
// Returns false when the token is missing, malformed, expired, or wrongly signed.
func ParseClaims(c echo.Context, secret string) (*Claims, bool) {
	tokenStr := extractToken(c)
	if tokenStr == "" {
		return nil, false
	}
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, apperr.Unauthorized("invalid signing method")
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, false
	}
	return claims, true
}

// RequireRole authorizes the request against an allowlist of roles. Must run
// after JWTAuth.
func RequireRole(roles ...string) echo.MiddlewareFunc {
	allowed := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			role, _ := c.Get(ctxRole).(string)
			if _, ok := allowed[role]; !ok {
				return apperr.Forbidden("")
			}
			return next(c)
		}
	}
}

// CurrentUserID returns the authenticated user's id, or 0 if unauthenticated.
func CurrentUserID(c echo.Context) int64 {
	id, _ := c.Get(ctxUserID).(int64)
	return id
}

// CurrentRole returns the authenticated user's role, or "".
func CurrentRole(c echo.Context) string {
	r, _ := c.Get(ctxRole).(string)
	return r
}

// CurrentUserName returns the authenticated user's display name, or "".
func CurrentUserName(c echo.Context) string {
	n, _ := c.Get(ctxName).(string)
	return n
}

// MustChangePin reports whether the authenticated user is required to change
// their PIN before using the rest of the app.
func MustChangePin(c echo.Context) bool {
	b, _ := c.Get(ctxMustChangePin).(bool)
	return b
}

// RequirePinChosen redirects a user who still carries a server-assigned PIN to
// the change-PIN screen. The change-PIN routes and logout stay reachable so the
// user can always escape the gate. Must run after JWTAuth.
func RequirePinChosen() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if MustChangePin(c) {
				p := c.Request().URL.Path
				if !strings.HasPrefix(p, "/account/pin") && p != "/logout" {
					return c.Redirect(303, "/account/pin")
				}
			}
			return next(c)
		}
	}
}

// IsLocked reports whether the current session is locked (screen lock, NOT a
// logout — the identity and any open till/float are intact).
func IsLocked(c echo.Context) bool {
	b, _ := c.Get(ctxLocked).(bool)
	return b
}

// RequireUnlocked redirects a locked session to the /lock screen. The lock/unlock
// routes and logout stay reachable so the user can always resume or sign out. Must
// run after JWTAuth. A GET is redirected; a non-GET (e.g. an HTMX POST fired just
// as the lock kicked in) gets a 401 the client turns into a full redirect.
func RequireUnlocked() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if IsLocked(c) {
				p := c.Request().URL.Path
				if p != "/lock" && p != "/unlock" && p != "/logout" {
					if c.Request().Method == http.MethodGet {
						return c.Redirect(303, "/lock")
					}
					c.Response().Header().Set("HX-Redirect", "/lock")
					return apperr.Unauthorized("session locked")
				}
			}
			return next(c)
		}
	}
}
