package web

import (
	"net/http"
	"net/url"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/auth"
	"karots-pos/internal/features/cashregister"
	"karots-pos/internal/middleware"
	"karots-pos/internal/plugin"
	"karots-pos/internal/response"
	authpages "karots-pos/templates/pages/auth"

	"github.com/labstack/echo/v4"
)

// CookieConfig controls how the UI session cookie is written.
type CookieConfig struct {
	Secure bool
	MaxAge time.Duration
}

// authUI renders the login screen and manages the session cookie. It lives in
// the web layer (not the auth feature) so the auth package never imports
// templates — that mutual import would be a cycle.
type authUI struct {
	svc          *auth.Service
	cookie       CookieConfig
	cashRegister *cashregister.Service
	jwtSecret    string
}

func (h *authUI) ShowLogin(c echo.Context) error {
	return response.RenderPage(c, authpages.LoginPage(""))
}

// Login is a full-page form POST. Errors re-render the login page inline rather
// than bouncing through the global toast handler.
func (h *authUI) Login(c echo.Context) error {
	var in auth.LoginInput
	if err := c.Bind(&in); err != nil {
		return h.loginError(c, "Please enter your phone number and PIN")
	}
	if err := c.Validate(&in); err != nil {
		return h.loginError(c, "Please enter a valid phone number and PIN")
	}
	pair, err := h.svc.Login(c.Request().Context(), in)
	if err != nil {
		if ae, ok := apperr.As(err); ok {
			return h.loginError(c, ae.Message)
		}
		return err
	}
	h.setCookie(c, pair.AccessToken)
	return c.Redirect(http.StatusSeeOther, auth.HomePath(pair.User.Role))
}

// Logout signs the caller out — but first refuses to drop the session while the
// user has unfinished cash work open, so the cash trail stays honest. If their
// till (or any plugin float, via a registered LogoutGuard) is still open, we keep
// the cookie and bounce them to the matching close/count screen (with ?logout=1
// so it auto-opens and returns here when done). Only once nothing is open do we
// clear the cookie and land on /login. The whole gate is best-effort: a token we
// can't read, or a guard/lookup that errors, falls through to a clean logout so a
// user can never get trapped unable to sign out.
func (h *authUI) Logout(c echo.Context) error {
	claims, ok := middleware.ParseClaims(c, h.jwtSecret)
	if !ok || claims.UserID == 0 {
		h.clearCookie(c)
		return c.Redirect(http.StatusSeeOther, "/login")
	}
	ctx := c.Request().Context()

	// Plugin guards first (e.g. recharge float, which is scoped to the open till
	// session) so the float is closed before the till it belongs to.
	for _, guard := range plugin.LogoutGuards() {
		if block, redirect, _ := guard(ctx, claims.UserID); block && redirect != "" {
			return c.Redirect(http.StatusSeeOther, withLogoutFlag(redirect))
		}
	}

	// Core: an open cash-register session must be counted and closed.
	if h.cashRegister != nil {
		if sess, err := h.cashRegister.Current(ctx, claims.UserID); err == nil && sess != nil {
			return c.Redirect(http.StatusSeeOther, withLogoutFlag("/cashier"))
		}
	}

	h.clearCookie(c)
	return c.Redirect(http.StatusSeeOther, "/login")
}

// withLogoutFlag adds ?logout=1 to a close-screen path so it knows to auto-open
// the close dialog and, once the close succeeds, send the user back to /logout.
func withLogoutFlag(path string) string {
	u, err := url.Parse(path)
	if err != nil {
		return path
	}
	q := u.Query()
	q.Set("logout", "1")
	u.RawQuery = q.Encode()
	return u.String()
}

// pinChangeBlocked reports whether this user may NOT change their own PIN: a
// cashier, when the shop has disabled cashier PIN changes and the user is not
// being forced to change (forced users must always be able to reach the screen,
// or they'd be stuck in a redirect loop).
func (h *authUI) pinChangeBlocked(c echo.Context) bool {
	if middleware.CurrentRole(c) != auth.RoleCashier || middleware.MustChangePin(c) {
		return false
	}
	return !h.svc.AllowCashierPinChange(c.Request().Context())
}

// ChangePINForm renders the self-service / forced PIN-change screen.
func (h *authUI) ChangePINForm(c echo.Context) error {
	if h.pinChangeBlocked(c) {
		return c.Redirect(http.StatusSeeOther, auth.HomePath(middleware.CurrentRole(c)))
	}
	return response.RenderPage(c, authpages.ChangePINPage("", middleware.MustChangePin(c)))
}

// ChangePIN applies a user's own PIN change, then mints a fresh cookie so the
// cleared forced-change claim takes effect immediately.
func (h *authUI) ChangePIN(c echo.Context) error {
	if h.pinChangeBlocked(c) {
		return c.Redirect(http.StatusSeeOther, auth.HomePath(middleware.CurrentRole(c)))
	}
	var in auth.ChangeOwnPINInput
	if err := c.Bind(&in); err != nil {
		return h.changePINError(c, "Please fill in all three fields")
	}
	if err := c.Validate(&in); err != nil {
		return h.changePINError(c, "PINs must be 4–6 digits")
	}
	u, err := h.svc.ChangeOwnPIN(c.Request().Context(), middleware.CurrentUserID(c), in)
	if err != nil {
		if ae, ok := apperr.As(err); ok {
			return h.changePINError(c, ae.Message)
		}
		return err
	}
	token, err := h.svc.AccessTokenFor(u)
	if err != nil {
		return err
	}
	h.setCookie(c, token)
	return c.Redirect(http.StatusSeeOther, auth.HomePath(u.Role))
}

func (h *authUI) changePINError(c echo.Context, msg string) error {
	c.Response().Header().Set(echo.HeaderContentType, echo.MIMETextHTMLCharsetUTF8)
	c.Response().WriteHeader(http.StatusBadRequest)
	return authpages.ChangePINPage(msg, middleware.MustChangePin(c)).Render(c.Request().Context(), c.Response().Writer)
}

func (h *authUI) loginError(c echo.Context, msg string) error {
	c.Response().Header().Set(echo.HeaderContentType, echo.MIMETextHTMLCharsetUTF8)
	c.Response().WriteHeader(http.StatusUnauthorized)
	return authpages.LoginPage(msg).Render(c.Request().Context(), c.Response().Writer)
}

func (h *authUI) setCookie(c echo.Context, token string) {
	c.SetCookie(&http.Cookie{
		Name:     middleware.CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(h.cookie.MaxAge.Seconds()),
		HttpOnly: true,
		Secure:   h.cookie.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *authUI) clearCookie(c echo.Context) {
	c.SetCookie(&http.Cookie{
		Name:     middleware.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.cookie.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}
