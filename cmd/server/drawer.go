package main

import (
	"context"
	"time"

	"karots-pos/internal/escpos"
	"karots-pos/internal/features/settings"
)

// newDrawerKicker builds the best-effort action that pops the physical cash
// drawer wired to the receipt printer. It reads the current settings on each call
// (so toggling the feature takes effect without a restart) and is injected into
// the services that own a till cash event — cash register (open/close/deposit/
// withdraw) and sales (cash sale). A no-op when the shop hasn't enabled a drawer.
//
// The pulse is fired on a detached goroutine so it never blocks the caller's HTTP
// response. This matters most on a cash sale: the kick shells out to `lp` (CUPS),
// and a slow accept on a raw thermal queue was holding the /api/sales response —
// which in turn delayed the receipt print, since the browser only prints after
// the sale returns. Detaching keeps request-scoped values but drops cancellation
// (the request ctx is cancelled the moment the handler returns, which would kill
// the `lp` process mid-submission), and a timeout bounds a dead printer so the
// goroutine can't leak.
func newDrawerKicker(settingsSvc *settings.Service) func(context.Context) {
	return func(ctx context.Context) {
		bg := context.WithoutCancel(ctx)
		go func() {
			c, cancel := context.WithTimeout(bg, 20*time.Second)
			defer cancel()
			cfg, err := settingsSvc.Get(c)
			if err != nil || cfg == nil {
				return
			}
			escpos.KickDrawer(c, *cfg)
		}()
	}
}
