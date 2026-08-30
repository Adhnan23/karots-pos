package supplierpay

import (
	"karots-pos/internal/features/purchases"

	"github.com/shopspring/decimal"
)

// Distribute turns a single payment amount into a PayInput by cascading it
// across the supplier's debts, so the admin can just type one figure.
//
// mode "old" pays the old (opening) debt down first; any other mode ("pay")
// pays the open invoices oldest-first first. In both cases whatever is left
// after invoices and old debt becomes an unallocated advance — a credit sitting
// with the supplier. invoices must be oldest-first (as OpenInvoices returns).
func Distribute(invoices []purchases.Purchase, opening, amount decimal.Decimal, mode string) PayInput {
	in := PayInput{}
	remaining := amount
	if remaining.IsNegative() {
		remaining = decimal.Zero
	}

	payOpening := func() {
		if !remaining.IsPositive() || !opening.IsPositive() {
			return
		}
		pay := opening
		if remaining.LessThan(pay) {
			pay = remaining
		}
		in.Opening = pay
		remaining = remaining.Sub(pay)
	}
	payInvoices := func() {
		for _, pu := range invoices {
			if !remaining.IsPositive() {
				break
			}
			bal := pu.Balance()
			if !bal.IsPositive() {
				continue
			}
			pay := bal
			if remaining.LessThan(pay) {
				pay = remaining
			}
			in.Allocations = append(in.Allocations, Alloc{PurchaseID: pu.ID, Amount: pay})
			remaining = remaining.Sub(pay)
		}
	}

	if mode == "old" {
		payOpening()
		payInvoices()
	} else {
		payInvoices()
		payOpening()
	}
	// Anything still left over is an advance/credit against the next delivery.
	if remaining.IsPositive() {
		in.Unallocated = remaining
	}
	return in
}
