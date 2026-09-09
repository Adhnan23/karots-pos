package middleware

import "context"

// appearance is the system-user-locked look for the current request, stashed in
// the request context so base.templ can stamp it on <html> without threading a
// parameter through every page's data struct (same idea as UserFlags).
type appearance struct{ skin, density string }

var ctxAppearanceKey = ctxKey{"appearance"}

// SetAppearanceCtx returns a context carrying the skin + density for templates.
// Values must be pre-validated by the caller (the web layer validates against
// the settings registry; this package stores plain strings to avoid an import
// cycle with the settings feature).
func SetAppearanceCtx(ctx context.Context, skin, density string) context.Context {
	return context.WithValue(ctx, ctxAppearanceKey, appearance{skin: skin, density: density})
}

// AppearanceCtx returns the request's skin + density, defaulting to the base
// look when unset — so any context (a fragment, a test, a non-page request)
// renders correctly rather than blank.
func AppearanceCtx(ctx context.Context) (skin, density string) {
	a, ok := ctx.Value(ctxAppearanceKey).(appearance)
	if !ok || a.skin == "" {
		return "default", "comfortable"
	}
	return a.skin, a.density
}
