package recharge

import (
	"bytes"
	"context"
	"strings"
	"time"

	"karots-pos/internal/money"
	"karots-pos/internal/printing"

	"github.com/shopspring/decimal"
)

// printSlip prints a deposit / withdrawal / bill-payment slip on the shop's
// receipt printer. It is best-effort: any error (no printer configured, offline)
// is swallowed so the transaction itself still succeeds. It builds raw ESC/POS
// bytes directly — the core printing transport (printing.Raw) is reused, and no
// core printing/escpos code is touched.
func (p *Plugin) printSlip(ctx context.Context, kind, carrier string, amount decimal.Decimal, reference string) {
	cfg, err := p.core.Settings.Get(ctx)
	if err != nil || strings.TrimSpace(cfg.ReceiptPrinter) == "" {
		return
	}
	width := 32
	if strings.TrimSpace(cfg.ReceiptWidth) == "80" {
		width = 48
	}

	var b bytes.Buffer
	b.Write([]byte{0x1B, 0x40}) // ESC @  initialise
	center := func() { b.Write([]byte{0x1B, 0x61, 0x01}) }
	left := func() { b.Write([]byte{0x1B, 0x61, 0x00}) }
	bold := func(on bool) {
		if on {
			b.Write([]byte{0x1B, 0x45, 0x01})
		} else {
			b.Write([]byte{0x1B, 0x45, 0x00})
		}
	}
	line := func(s string) { b.WriteString(s); b.WriteByte('\n') }
	rule := func() { line(strings.Repeat("-", width)) }

	center()
	bold(true)
	line(cfg.ShopName)
	bold(false)
	if cfg.Address != nil && strings.TrimSpace(*cfg.Address) != "" {
		line(*cfg.Address)
	}
	if cfg.Phone != nil && strings.TrimSpace(*cfg.Phone) != "" {
		line(*cfg.Phone)
	}
	rule()
	bold(true)
	b.Write([]byte{0x1D, 0x21, 0x11}) // GS ! double width+height
	line(strings.ToUpper(txLabel(kind)))
	b.Write([]byte{0x1D, 0x21, 0x00})
	bold(false)
	rule()
	left()
	line("Carrier : " + carrier)
	line("Amount  : " + money.Format(cfg.CurrencySymbol, amount))
	if strings.TrimSpace(reference) != "" {
		line("Ref     : " + reference)
	}
	line("Date    : " + time.Now().Format("2006-01-02 15:04"))
	rule()
	center()
	line("Signature: ____________________")
	line("")
	line("Thank you")
	b.WriteString("\n\n\n")
	b.Write([]byte{0x1D, 0x56, 0x42, 0x00}) // GS V partial cut with feed

	_ = printing.Raw(ctx, cfg.ReceiptPrinter, b.Bytes())
}

// txLabel is the human label for a transaction type.
func txLabel(t string) string {
	switch t {
	case "deposit":
		return "deposit"
	case "withdrawal":
		return "withdrawal"
	case "billpay":
		return "bill payment"
	case "topup":
		return "float top-up"
	case "wallet_in":
		return "wallet payment"
	case "reload":
		return "reload"
	case "refill":
		return "supplier refill"
	}
	return t
}
