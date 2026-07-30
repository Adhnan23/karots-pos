package recharge

import (
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/lockers"

	"github.com/shopspring/decimal"
)

// bankLeg is one cashflow move in a bill-payment / get-money. Reason is applied
// by the handler (it is the same for every leg); Party labels the External
// counterparty on that leg's CR- receipt.
type bankLeg struct {
	From, To cashflow.Location
	Amount   decimal.Decimal
	Party    string
}

// buildBankLegs returns the cashflow legs for a bill payment ("billpay") or a
// get-money ("getmoney"). account is the storage the biller is paid from (billpay)
// or the e-money lands in (getmoney); cash is the physical-cash pile. svc (>= 0)
// is the service charge, always extra cash into the cash pile. biller labels the
// bank↔biller leg; the cash↔customer legs are labelled "Customer".
//
// The order matters: the overdraw-guarded leg (bank down for billpay, cash out
// for getmoney) comes first so an overdraw rolls the whole tx back before any
// money moves.
func buildBankLegs(typ string, account, cash cashflow.Location, amt, svc decimal.Decimal, biller string) []bankLeg {
	ext := cashflow.External()
	switch typ {
	case "billpay":
		return []bankLeg{
			{From: account, To: ext, Amount: amt, Party: biller},
			{From: ext, To: cash, Amount: amt.Add(svc), Party: "Customer"},
		}
	case "getmoney":
		legs := []bankLeg{
			{From: cash, To: ext, Amount: amt, Party: "Customer"},
			{From: ext, To: account, Amount: amt, Party: "Customer"},
		}
		if svc.IsPositive() {
			legs = append(legs, bankLeg{From: ext, To: cash, Amount: svc, Party: "Customer"})
		}
		return legs
	}
	return nil
}

// bankUsableByCashier reports whether a locker is one a cashier may run bill-pay
// against: an active bank the owner marked cashier-accessible. Used both to filter
// the cashier's bank picker and to guard the POST (a forged bank id can't slip
// past the picker filter otherwise).
func bankUsableByCashier(l *lockers.Locker) bool {
	return l != nil && l.IsActive && l.Kind == lockers.KindBank && l.CashierAccess
}
