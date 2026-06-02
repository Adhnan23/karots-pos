package auth

import (
	"time"

	"karots-pos/internal/config"
	"karots-pos/internal/middleware"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
)

// NewLoginLimiter returns a rate limiter sized to blunt PIN brute-forcing.
// PINs are low entropy, so login endpoints (UI and API) share this policy.
func NewLoginLimiter() echo.MiddlewareFunc {
	return echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
		Store: echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
			Rate:      0.5,
			Burst:     10,
			ExpiresIn: 3 * time.Minute,
		}),
	})
}

// RegisterAPI wires the JSON auth + user-management endpoints and returns the
// shared Service so the web (UI) layer can reuse it without re-instantiating.
func RegisterAPI(e *echo.Echo, db *sqlx.DB, cfg *config.Config, limiter echo.MiddlewareFunc) *Service {
	svc := NewService(db, cfg.JWTSecret, cfg.JWTAccessTTL, cfg.JWTRefreshTTL)
	api := NewAPIHandler(svc)

	e.POST("/api/auth/login", api.Login, limiter)
	e.POST("/api/auth/refresh", api.Refresh)
	e.POST("/api/auth/logout", api.Logout)

	jwt := middleware.JWTAuth(cfg.JWTSecret)
	users := e.Group("/api/users", jwt, middleware.RequireRole(RoleAdmin))
	users.GET("", api.ListUsers)
	users.POST("", api.CreateUser)
	users.DELETE("/:id", api.DeactivateUser)

	return svc
}
