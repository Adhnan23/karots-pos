package main

import (
	"context"

	"karots-pos/internal/escpos"
	"karots-pos/internal/features/settings"
)

// newDrawerKicker builds the best-effort action that pops the physical cash
// drawer wired to the receipt printer. It reads the current settings on each call
// (so toggling the feature takes effect without a restart) and is injected into
// the services that own a till cash event — cash register (open/close/deposit/
// withdraw) and sales (cash sale). A no-op when the shop hasn't enabled a drawer.
func newDrawerKicker(settingsSvc *settings.Service) func(context.Context) {
	return func(ctx context.Context) {
		cfg, err := settingsSvc.Get(ctx)
		if err != nil || cfg == nil {
			return
		}
		escpos.KickDrawer(ctx, *cfg)
	}
}
