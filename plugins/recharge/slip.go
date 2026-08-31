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
	"karots-pos/internal/receiptimg"
	poststatic "karots-pos/static"

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
	BillType      string // kind of bill (Electricity, ...) — shown on bill-pay slips
	Reference     string
	Amount        decimal.Decimal
	ServiceCharge decimal.Decimal
	CashGiven     decimal.Decimal // cash the customer handed over; zero = not recorded (no change line)
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
	return printing.Raw(ctx, cfg.ReceiptPrinter, buildSlip(cfg, receiptimg.SlipOptions(ctx, cfg, poststatic.Files), slipData{
		Kind: t.Type, ReceiptNo: floatNo(t.ID), Carrier: t.Carrier, Device: device,
		Amount: t.Amount, ServiceCharge: t.ServiceCharge, CashGiven: derefDec(t.CashGiven),
		Reference: refText(t.Reference), Operator: t.Operator, When: t.CreatedAt,
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
	return printing.Raw(ctx, cfg.ReceiptPrinter, buildSlip(cfg, receiptimg.SlipOptions(ctx, cfg, poststatic.Files), slipData{
		Kind: t.Type, ReceiptNo: billNo(t.ID), BillType: derefStr(t.BillType), Amount: t.Amount,
		ServiceCharge: t.ServiceCharge, CashGiven: derefDec(t.CashGiven),
		Reference: refText(t.Reference), Operator: t.Operator, When: t.CreatedAt,
	}))
}

// buildSlip renders a reload/bill transaction slip as raw ESC/POS bytes using the
// SHARED core receipt header/footer (escpos.Header/Footer), so a recharge slip
// carries the same branding — big shop name, secondary-language name, address,
// settings footer + credit line — as the sale receipt. Only the body (carrier/
// bank, amounts, change) is recharge-specific.
func buildSlip(cfg *settings.Settings, opts escpos.Options, d slipData) []byte {
	w := escpos.Columns(cfg.ReceiptWidth)
	sym := cfg.CurrencySymbol
	if sym == "" {
		sym = "Rs."
	}
	var b bytes.Buffer
	escpos.Init(&b)
	escpos.Header(&b, *cfg, opts)
	escpos.Title(&b, txLabel(d.Kind), w)

	// --- Meta (left, values right-aligned like the sale) ---
	escpos.Left(&b)
	escpos.Divider(&b, w)
	if strings.TrimSpace(d.ReceiptNo) != "" {
		escpos.Line(&b, escpos.LeftRight("Receipt:", d.ReceiptNo, w))
	}
	escpos.Line(&b, escpos.LeftRight("Date:", d.When.Format("2006-01-02 15:04"), w))
	// Bill-pay / get-money are customer receipts: name the kind of bill, never the
	// shop's internal account. A reload names its carrier.
	if d.Kind == "billpay" || d.Kind == "getmoney" {
		if bt := strings.TrimSpace(d.BillType); bt != "" {
			escpos.Line(&b, escpos.LeftRight("Bill:", escpos.ASCII(bt), w))
		}
	} else {
		escpos.Line(&b, escpos.LeftRight("Carrier:", escpos.ASCII(d.Carrier), w))
	}
	if strings.TrimSpace(d.Device) != "" {
		escpos.Line(&b, escpos.LeftRight("Device:", escpos.ASCII(d.Device), w))
	}
	if strings.TrimSpace(d.Reference) != "" {
		escpos.Line(&b, escpos.LeftRight("Ref:", escpos.ASCII(d.Reference), w))
	}
	if strings.TrimSpace(d.Operator) != "" {
		escpos.Line(&b, escpos.LeftRight("By:", escpos.ASCII(d.Operator), w))
	}
	escpos.Divider(&b, w)

	// --- Amounts (Total/Amount emphasized like the sale's TOTAL) ---
	due := d.Amount
	if d.ServiceCharge.IsPositive() {
		escpos.Line(&b, escpos.LeftRight("Amount", money.Format(sym, d.Amount), w))
		escpos.Line(&b, escpos.LeftRight("Service", money.Format(sym, d.ServiceCharge), w))
		due = d.Amount.Add(d.ServiceCharge)
		escpos.Emphasis(&b, true)
		escpos.Line(&b, escpos.LeftRight("Total", money.Format(sym, due), w))
		escpos.Emphasis(&b, false)
	} else {
		escpos.Emphasis(&b, true)
		escpos.Line(&b, escpos.LeftRight("Amount", money.Format(sym, d.Amount), w))
		escpos.Emphasis(&b, false)
	}
	// Tender + change, like a sale receipt — only when the cash given was recorded.
	if d.CashGiven.IsPositive() {
		escpos.Line(&b, escpos.LeftRight("Paid", money.Format(sym, d.CashGiven), w))
		if change := d.CashGiven.Sub(due); change.IsNegative() {
			escpos.Line(&b, escpos.LeftRight("Balance", money.Format(sym, change.Neg()), w))
		} else {
			escpos.Line(&b, escpos.LeftRight("Change", money.Format(sym, change), w))
		}
	}
	escpos.Divider(&b, w)

	escpos.Footer(&b, *cfg)
	return b.Bytes()
}

// derefDec unwraps a nullable money column to a plain decimal (NULL → zero), so a
// slip with no recorded tender simply omits the change line.
func derefDec(d *decimal.Decimal) decimal.Decimal {
	if d == nil {
		return decimal.Zero
	}
	return *d
}

// derefStr unwraps a nullable text column to a plain string (NULL → ""), so an
// absent value simply omits its slip line rather than printing a placeholder.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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
	case "opening":
		return "opening balance"
	}
	return t
}
