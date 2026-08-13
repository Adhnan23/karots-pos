package receiptimg

import (
	"context"
	"io/fs"

	"karots-pos/internal/escpos"
	"karots-pos/internal/features/settings"
)

// SlipOptions renders the shop logo and the secondary (non-Latin) shop name to
// ESC/POS raster blocks so EVERY thermal slip — sale, return, warranty, credit,
// money receipt, recharge — carries the same branding. Failures are non-fatal:
// the receipt still prints without that element. Callers pass the embedded static
// FS so a "/static/…" logo path resolves; a data: URI or http(s) logo needs no FS.
func SlipOptions(ctx context.Context, cfg *settings.Settings, staticFS fs.FS) escpos.Options {
	var opts escpos.Options
	if cfg == nil {
		return opts
	}
	dots := PrinterDots(cfg.ReceiptWidth)
	if src := cfg.LogoSrc(); src != "" {
		if img, err := LoadImage(ctx, src, staticFS); err == nil {
			opts.Logo = Logo(img, dots)
		}
	}
	if cfg.ShopNameSi != nil && *cfg.ShopNameSi != "" {
		opts.SubName = SubName(*cfg.ShopNameSi, dots, dots/14)
	}
	return opts
}
