package middleware

import (
	"strings"

	"karots-pos/internal/apperr"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

const (
	ctxUserID        = "user_id"
	ctxRole          = "role"
	ctxName          = "user_name"
	ctxMustChangePin = "must_change_pin"

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
			c.Set(ctxUserID, claims.UserID)
			c.Set(ctxRole, claims.Role)
			c.Set(ctxName, claims.Name)
			c.Set(ctxMustChangePin, claims.MustChangePin)
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
