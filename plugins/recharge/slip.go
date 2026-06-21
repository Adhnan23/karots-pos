package recharge

import (
	"bytes"
	"context"
	"strings"
	"time"

	"karots-pos/internal/features/settings"
	"karots-pos/internal/money"
	"karots-pos/internal/printing"

	"github.com/shopspring/decimal"
)

// slipData is everything a transaction slip prints. It is built either live (from
// the tx handler) or on reprint (from a stored ledger row), so a reprinted slip
// matches the original.
type slipData struct {
	Kind          string
	Carrier       string
	Device        string
	Reference     string
	Amount        decimal.Decimal
	ServiceCharge decimal.Decimal
	When          time.Time
}

// printSlip prints a deposit / withdrawal / bill-payment slip on the shop's
// receipt printer. It is best-effort: any error (no printer configured, offline)
// is swallowed so the transaction itself still succeeds.
func (p *Plugin) printSlip(ctx context.Context, kind, carrier string, amount, serviceCharge decimal.Decimal, reference string) {
	cfg, err := p.core.Settings.Get(ctx)
	if err != nil || cfg == nil || strings.TrimSpace(cfg.ReceiptPrinter) == "" {
		return
	}
	_ = printing.Raw(ctx, cfg.ReceiptPrinter, buildSlip(cfg, slipData{
		Kind: kind, Carrier: carrier, Amount: amount, ServiceCharge: serviceCharge,
		Reference: reference, When: time.Now(),
	}))
}

// reprintTx rebuilds a slip from a stored ledger row and sends it to the printer.
// Unlike printSlip it returns the error so the reprint endpoint can report it.
func (p *Plugin) reprintTx(ctx context.Context, t TxRow) error {
	cfg, err := p.core.Settings.Get(ctx)
	if err != nil {
		return err
	}
	if cfg == nil || strings.TrimSpace(cfg.ReceiptPrinter) == "" {
		return nil // no printer configured — treat as a no-op success
	}
	device := ""
	if t.Device != "—" {
		device = t.Device
	}
	return printing.Raw(ctx, cfg.ReceiptPrinter, buildSlip(cfg, slipData{
		Kind: t.Type, Carrier: t.Carrier, Device: device, Amount: t.Amount,
		ServiceCharge: t.ServiceCharge, Reference: refText(t.Reference), When: t.CreatedAt,
	}))
}

// buildSlip renders a transaction slip as raw ESC/POS bytes. It reuses the core
// printing transport (printing.Raw) and touches no core printing/escpos code.
func buildSlip(cfg *settings.Settings, d slipData) []byte {
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
	line(strings.ToUpper(txLabel(d.Kind)))
	b.Write([]byte{0x1D, 0x21, 0x00})
	bold(false)
	rule()
	left()
	line("Carrier : " + d.Carrier)
	if strings.TrimSpace(d.Device) != "" {
		line("Device  : " + d.Device)
	}
	line("Amount  : " + money.Format(cfg.CurrencySymbol, d.Amount))
	if d.ServiceCharge.IsPositive() {
		line("Service : " + money.Format(cfg.CurrencySymbol, d.ServiceCharge))
		line("Total   : " + money.Format(cfg.CurrencySymbol, d.Amount.Add(d.ServiceCharge)))
	}
	if strings.TrimSpace(d.Reference) != "" {
		line("Ref     : " + d.Reference)
	}
	line("Date    : " + d.When.Format("2006-01-02 15:04"))
	rule()
	center()
	line("Signature: ____________________")
	line("")
	line("Thank you")
	b.WriteString("\n\n\n")
	b.Write([]byte{0x1D, 0x56, 0x42, 0x00}) // GS V partial cut with feed
	return b.Bytes()
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
