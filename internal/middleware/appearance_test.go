package middleware

import (
	"context"
	"testing"
)

func TestAppearanceCtxDefaults(t *testing.T) {
	skin, density := AppearanceCtx(context.Background())
	if skin != "default" || density != "comfortable" {
		t.Fatalf("empty ctx: got %q/%q, want default/comfortable", skin, density)
	}
}

func TestAppearanceCtxRoundTrip(t *testing.T) {
	ctx := SetAppearanceCtx(context.Background(), "teal", "compact", "", "")
	skin, density := AppearanceCtx(ctx)
	if skin != "teal" || density != "compact" {
		t.Fatalf("got %q/%q, want teal/compact", skin, density)
	}
}
