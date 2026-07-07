package recharge

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"karots-pos/internal/escpos"
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
	ReceiptNo     string // e.g. RB-000001 (bill) / RL-000001 (reload) — shown in the header
	Carrier       string
	Device        string
	Reference     string
	Amount        decimal.Decimal
	ServiceCharge decimal.Decimal
	Operator      string // who recorded it (shown as "By: …")
	When          time.Time
}

// billNo / floatNo format a recharge receipt number for the slip header, matching
// the S- / CR- / DP- style used by core receipts (its own prefix, not a CR-).
func billNo(id int64) string  { return fmt.Sprintf("RB-%06d", id) }
func floatNo(id int64) string { return fmt.Sprintf("RL-%06d", id) }

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
		Kind: t.Type, ReceiptNo: floatNo(t.ID), Carrier: t.Carrier, Device: device,
		Amount: t.Amount, ServiceCharge: t.ServiceCharge, Reference: refText(t.Reference),
		Operator: t.Operator, When: t.CreatedAt,
	}))
}

// reprintBill rebuilds a bill-payment / get-money slip from its stored log row and
// sends it to the printer, returning the error so the reprint endpoint can report
// it. The bank name prints in the "Bank" line (buildSlip keys off the kind).
func (p *Plugin) reprintBill(ctx context.Context, t BillTxRow) error {
	cfg, err := p.core.Settings.Get(ctx)
	if err != nil {
		return err
	}
	if cfg == nil || strings.TrimSpace(cfg.ReceiptPrinter) == "" {
		return nil
	}
	return printing.Raw(ctx, cfg.ReceiptPrinter, buildSlip(cfg, slipData{
		Kind: t.Type, ReceiptNo: billNo(t.ID), Carrier: t.Bank, Amount: t.Amount,
		ServiceCharge: t.ServiceCharge, Reference: refText(t.Reference),
		Operator: t.Operator, When: t.CreatedAt,
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
	// Sanitise every free-text/label field to printable ASCII, exactly like the
	// core money-receipt slip (the thermal font has no dash/non-Latin glyphs).
	line := func(s string) { b.WriteString(escpos.ASCII(s)); b.WriteByte('\n') }
	rule := func() { b.WriteString(strings.Repeat("-", width)); b.WriteByte('\n') }

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
	// Same header shape as CR-/DP- receipts: bold "<TITLE> RECEIPT" then the number.
	bold(true)
	line(strings.ToUpper(txLabel(d.Kind)) + " RECEIPT")
	bold(false)
	if strings.TrimSpace(d.ReceiptNo) != "" {
		line(d.ReceiptNo)
	}
	rule()
	left()
	// Bill-pay / get-money name the bank; everything else names a carrier.
	if d.Kind == "billpay" || d.Kind == "getmoney" {
		line("Bank    : " + d.Carrier)
	} else {
		line("Carrier : " + d.Carrier)
	}
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
	if strings.TrimSpace(d.Operator) != "" {
		line("By      : " + d.Operator)
	}
	line("Date    : " + d.When.Format("2006-01-02 15:04"))
	rule()
	center()
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
	case "getmoney":
		return "money out"
	case "topup":
		return "reload top-up"
	case "wallet_in":
		return "wallet payment"
	case "reload":
		return "reload"
	case "refill":
		return "supplier refill"
	}
	return t
}
