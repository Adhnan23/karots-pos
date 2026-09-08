package repairs

import (
	"time"

	"karots-pos/internal/features/sales"

	"github.com/shopspring/decimal"
)

// warrantyUntil is the date a repair's warranty runs to: nil when no warranty,
// else the collection date (date-only) plus the warranty days.
func warrantyUntil(days int, from time.Time) *time.Time {
	if days <= 0 {
		return nil
	}
	u := from.Truncate(24*time.Hour).AddDate(0, 0, days)
	return &u
}

var hundred = decimal.NewFromInt(100)

// normDiscType defaults a blank/unknown per-line discount type to "fixed".
func normDiscType(t string) string {
	if t == "percent" {
		return "percent"
	}
	return "fixed"
}

// resolvePartDiscount turns a per-part discount into an amount off the line.
// Fixed is per unit (× qty); percent is off the gross. Clamped to [0, gross].
func resolvePartDiscount(dtype string, value, gross, qty decimal.Decimal) decimal.Decimal {
	var amt decimal.Decimal
	if dtype == "percent" {
		amt = gross.Mul(value).Div(hundred)
	} else {
		amt = value.Mul(qty)
	}
	amt = amt.Round(2)
	if amt.IsNegative() {
		return decimal.Zero
	}
	if amt.GreaterThan(gross) {
		return gross
	}
	return amt
}

// JobTotals sums the job: total = Σ charges + Σ(part gross − part discount);
// depositPaid = deposits − refunds; balance = max(0, total − depositPaid).
// Part totals use the stored unit_charge, which the add-part handler seeds from
// the product's catalogue price so it matches what the collection sale charges.
// ponytail: a catalogue price change between add and collect can drift this from
// the sale's recomputed total; acceptable in the short collection window —
// re-price at collect if that ever bites.
func JobTotals(d *Detail) (total, depositPaid, balance decimal.Decimal) {
	total = decimal.Zero
	for _, c := range d.Charges {
		total = total.Add(c.Amount)
	}
	for _, p := range d.Parts {
		gross := p.Qty.Mul(p.UnitCharge)
		total = total.Add(gross.Sub(resolvePartDiscount(p.DiscountType, p.DiscountValue, gross, p.Qty)))
	}
	total = total.Round(2)

	depositPaid = decimal.Zero
	for _, pm := range d.Payments {
		if pm.Kind == "refund" {
			depositPaid = depositPaid.Sub(pm.Amount)
		} else {
			depositPaid = depositPaid.Add(pm.Amount)
		}
	}
	depositPaid = depositPaid.Round(2)

	balance = total.Sub(depositPaid)
	if balance.IsNegative() {
		balance = decimal.Zero
	}
	return total, depositPaid, balance
}

// saleItems turns the job's parts + charges into sale lines: one line per part
// (priced by the catalogue, carrying its per-item discount) and one line per
// charge (the hidden labour/service product, amount via PriceOverride).
func saleItems(d *Detail, labourProductID int64) []sales.ItemInput {
	items := make([]sales.ItemInput, 0, len(d.Parts)+len(d.Charges))
	for _, p := range d.Parts {
		items = append(items, sales.ItemInput{
			ProductID:    p.ProductID,
			Quantity:     p.Qty.String(),
			Discount:     p.DiscountValue.String(),
			DiscountType: normDiscType(p.DiscountType),
		})
	}
	for _, c := range d.Charges {
		items = append(items, sales.ItemInput{
			ProductID:     labourProductID,
			Quantity:      "1",
			PriceOverride: c.Amount.String(),
		})
	}
	return items
}

// collectionTenders splits a collection into sale tenders: the already-paid
// deposit as a non-cash `wallet` tender, the pay-now amount via `method`, and
// any remainder on account (credit). Zero legs are omitted; the three sum to the
// job total. `method` is the pay-now method (cash/card/online); a blank or
// "credit" method leaves nothing paid-now (the balance goes on account).
func collectionTenders(depositPaid, payNow, onAccount decimal.Decimal, method string) []sales.PaymentInput {
	switch method {
	case "cash", "card", "online":
	default:
		method = "cash"
	}
	out := make([]sales.PaymentInput, 0, 3)
	if depositPaid.IsPositive() {
		out = append(out, sales.PaymentInput{Method: "wallet", Amount: depositPaid.StringFixed(2)})
	}
	if payNow.IsPositive() {
		out = append(out, sales.PaymentInput{Method: method, Amount: payNow.StringFixed(2)})
	}
	if onAccount.IsPositive() {
		out = append(out, sales.PaymentInput{Method: "credit", Amount: onAccount.StringFixed(2)})
	}
	return out
}

// BuildCollectionSale assembles the settling sale from the job's lines and the
// resolved tenders.
func BuildCollectionSale(d *Detail, labourProductID int64, customerID *int64, tenders []sales.PaymentInput) sales.CreateInput {
	return sales.CreateInput{
		CustomerID: customerID,
		SaleType:   "retail",
		Items:      saleItems(d, labourProductID),
		Payments:   tenders,
	}
}
