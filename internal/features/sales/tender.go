package sales

import (
	"karots-pos/internal/apperr"
	"karots-pos/internal/money"

	"github.com/shopspring/decimal"
)

// MethodOnAccount is the payment method that settles a sale without money
// changing hands. It is shown to cashiers as "On account".
const MethodOnAccount = "credit"

// Tender is a sale's payment split into what was actually received and what the
// customer now owes. They are kept apart because an on-account line is a debt,
// not money: counting it as paid would report a debt as a completed cash sale,
// and would inflate the drawer's expected balance.
type Tender struct {
	Paid      decimal.Decimal
	OnAccount decimal.Decimal
}

// SplitTender sorts parallel method/amount lists into the two figures.
func SplitTender(methods []string, amounts []decimal.Decimal) Tender {
	t := Tender{Paid: decimal.Zero, OnAccount: decimal.Zero}
	for i, m := range methods {
		if i >= len(amounts) {
			break
		}
		if m == MethodOnAccount {
			t.OnAccount = t.OnAccount.Add(amounts[i])
		} else {
			t.Paid = t.Paid.Add(amounts[i])
		}
	}
	return t
}

// CheckTender validates a split against the bill.
//
// A shortfall is refused rather than silently becoming credit — that silence
// was the defect: a credit sale would be recorded as an ordinary retail one and
// the debt was invisible in the receipts list. The till resolves a shortfall
// through its confirmation prompt and posts an explicit on-account line, so
// this is the backstop rather than the cashier's experience of it.
func CheckTender(t Tender, total decimal.Decimal, hasCustomer bool, availableCredit decimal.Decimal) error {
	covered := t.Paid.Add(t.OnAccount)
	if covered.LessThan(total) {
		return apperr.Validation(money.Display(total.Sub(covered)) +
			" is unpaid — take the money, or put it on a customer's account")
	}
	if !t.OnAccount.IsPositive() {
		return nil
	}
	if !hasCustomer {
		return apperr.Validation("choose a customer to put this on account")
	}
	// No change against money that was never paid: a part-account sale must
	// land exactly on the total.
	if covered.GreaterThan(total) {
		return apperr.Validation("this sale is over-paid — reduce the amount on account")
	}
	if t.OnAccount.GreaterThan(availableCredit) {
		return apperr.Conflict("credit limit exceeded (available " +
			money.Display(availableCredit) + ")")
	}
	return nil
}
