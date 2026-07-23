package web

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"karots-pos/internal/config"

	"github.com/labstack/echo/v4"
)

// A Secure cookie is discarded by the browser over plain http://, so setting it
// on a connection that is not HTTPS does not harden anything — it stops anyone
// staying logged in at all. This bit a shop network exactly as you would expect:
// every till signed in and bounced straight back to the login screen, while the
// server's own http://localhost kept working, because browsers treat localhost
// as a secure context.

func secureFor(t *testing.T, mode config.CookieSecureMode, req *http.Request) bool {
	t.Helper()
	h := &authUI{cookie: CookieConfig{Secure: mode}}
	return h.secureFlag(echo.New().NewContext(req, httptest.NewRecorder()))
}

func plainRequest() *http.Request { return httptest.NewRequest(http.MethodGet, "/login", nil) }

func tlsRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/login", nil)
	r.TLS = &tls.ConnectionState{}
	return r
}

func TestCookieAutoFollowsTheConnection(t *testing.T) {
	if secureFor(t, config.CookieSecureAuto, plainRequest()) {
		t.Error("marked the cookie Secure on a plain-HTTP request — the browser would drop it and nobody could log in")
	}
	if !secureFor(t, config.CookieSecureAuto, tlsRequest()) {
		t.Error("did not mark the cookie Secure on an HTTPS request")
	}
}

// TLS is usually terminated at a reverse proxy, so the request reaches the
// server as plain HTTP while the browser is genuinely on https.
func TestCookieAutoTrustsForwardedProto(t *testing.T) {
	r := plainRequest()
	r.Header.Set("X-Forwarded-Proto", "https")
	if !secureFor(t, config.CookieSecureAuto, r) {
		t.Error("ignored X-Forwarded-Proto: https, so a proxied HTTPS install loses the Secure flag")
	}
	r2 := plainRequest()
	r2.Header.Set("X-Forwarded-Proto", "HTTPS") // header values are not case-sensitive here
	if !secureFor(t, config.CookieSecureAuto, r2) {
		t.Error("X-Forwarded-Proto comparison should not be case-sensitive")
	}
}

func TestCookieModesOverrideTheConnection(t *testing.T) {
	if !secureFor(t, config.CookieSecureAlways, plainRequest()) {
		t.Error("always mode must force Secure even on plain HTTP")
	}
	if secureFor(t, config.CookieSecureNever, tlsRequest()) {
		t.Error("never mode must suppress Secure even on HTTPS")
	}
}

func TestParseCookieSecureDefaultsToAuto(t *testing.T) {
	// The default has to be auto: it is the only value that is correct for both a
	// LAN shop and an HTTPS install without anyone configuring anything.
	for _, v := range []string{"", "auto", "something-unrecognised"} {
		if got := config.ParseCookieSecure(v); got != config.CookieSecureAuto {
			t.Errorf("COOKIE_SECURE=%q gave mode %v, want auto", v, got)
		}
	}
	for _, v := range []string{"true", "always", "1", "YES"} {
		if got := config.ParseCookieSecure(v); got != config.CookieSecureAlways {
			t.Errorf("COOKIE_SECURE=%q gave mode %v, want always", v, got)
		}
	}
	for _, v := range []string{"false", "never", "0", "No"} {
		if got := config.ParseCookieSecure(v); got != config.CookieSecureNever {
			t.Errorf("COOKIE_SECURE=%q gave mode %v, want never", v, got)
		}
	}
}
