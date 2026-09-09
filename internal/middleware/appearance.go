package middleware

import "context"

// appearance is the system-user-locked look for the current request, stashed in
// the request context so base.templ can stamp it on <html> without threading a
// parameter through every page's data struct (same idea as UserFlags).
type appearance struct{ skin, density, customBrand, customRadius string }

var ctxAppearanceKey = ctxKey{"appearance"}

// SetAppearanceCtx returns a context carrying the skin + density (+ the custom
// brand hex and card-shape keyword, used only when skin == "custom") for
// templates. Values must be pre-validated by the caller (the web layer
// validates against the settings registry; this package stores plain strings to
// avoid an import cycle with the settings feature).
func SetAppearanceCtx(ctx context.Context, skin, density, customBrand, customRadius string) context.Context {
	return context.WithValue(ctx, ctxAppearanceKey, appearance{skin: skin, density: density, customBrand: customBrand, customRadius: customRadius})
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

// CustomBrandCtx returns the custom brand hex for the request (blank unless the
// active skin is "custom" with a colour chosen).
func CustomBrandCtx(ctx context.Context) string {
	a, _ := ctx.Value(ctxAppearanceKey).(appearance)
	return a.customBrand
}

// CustomRadiusCtx returns the custom skin's card-shape keyword (sharp|rounded|
// round), defaulting to "rounded".
func CustomRadiusCtx(ctx context.Context) string {
	a, _ := ctx.Value(ctxAppearanceKey).(appearance)
	if a.customRadius == "" {
		return "rounded"
	}
	return a.customRadius
}
