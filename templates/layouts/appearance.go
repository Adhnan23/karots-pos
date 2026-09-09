package layouts

import (
	"context"

	"karots-pos/internal/middleware"
)

// skinAttr / densityAttr read the system-user-locked look from the request
// context (set by web.withAppearance) so base.templ can stamp it on <html>.
// Both default safely, so a context without appearance still renders.
func skinAttr(ctx context.Context) string    { s, _ := middleware.AppearanceCtx(ctx); return s }
func densityAttr(ctx context.Context) string { _, d := middleware.AppearanceCtx(ctx); return d }
