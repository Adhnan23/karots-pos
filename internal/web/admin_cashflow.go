package web

import (
	"karots-pos/internal/features/reports"
	"karots-pos/internal/middleware"
	"karots-pos/internal/response"
	adminpages "karots-pos/templates/pages/admin"

	"github.com/labstack/echo/v4"
)

// Cashflow renders the combined cash-flow view: live balances per location
// (lockers + open tills), the net in/out for the range, and one unified
// time-ordered ledger merging locker movements and till cash movements.
func (a *adminUI) Cashflow(c echo.Context) error {
	ctx := c.Request().Context()
	preset := c.QueryParam("preset")
	from, to, fromStr, toStr, err := reports.ResolveRange(preset, c.QueryParam("from"), c.QueryParam("to"))
	if err != nil {
		return err
	}

	d := adminpages.CashflowData{
		UserName: middleware.CurrentUserName(c),
		Symbol:   a.symbol(ctx),
		Preset:   preset,
		From:     fromStr,
		To:       toStr,
	}

	// Live balances: every active locker, plus every open till's expected cash.
	lockerRows, err := a.s.lockers.List(ctx, true)
	if err != nil {
		return err
	}
	for _, l := range lockerRows {
		d.Lockers = append(d.Lockers, adminpages.CashLocationBal{Name: l.Name, Balance: l.Balance})
		d.TotalCash = d.TotalCash.Add(l.Balance)
	}
	tills, err := a.s.cashRegister.OpenSessions(ctx)
	if err != nil {
		return err
	}
	for _, t := range tills {
		bal := t.OpeningCash
		if sum, serr := a.s.cashRegister.Summary(ctx, t.UserID); serr == nil {
			bal = sum.Expected
		}
		d.Tills = append(d.Tills, adminpages.CashLocationBal{Name: "Till — " + t.UserName, Balance: bal})
		d.TotalCash = d.TotalCash.Add(bal)
	}

	// Unified ledger + net in/out for the range.
	rows, err := a.s.cashflow.UnifiedLedger(ctx, from, to, 500)
	if err != nil {
		return err
	}
	d.Rows = rows
	for _, r := range rows {
		if r.Delta.IsPositive() {
			d.NetIn = d.NetIn.Add(r.Delta)
		} else {
			d.NetOut = d.NetOut.Add(r.Delta.Neg())
		}
	}
	d.NetChange = d.NetIn.Sub(d.NetOut)

	// Net position: cash on hand + stock at cost + customer receivables − supplier
	// payables = an estimate of the business's net worth right now.
	_ = a.db.GetContext(ctx, &d.StockValue, `SELECT COALESCE(SUM(qty_remaining * cost_price),0) FROM stock_batches`)
	_ = a.db.GetContext(ctx, &d.Receivables, `SELECT COALESCE(SUM(outstanding_balance),0) FROM customers`)
	_ = a.db.GetContext(ctx, &d.Payables, `SELECT COALESCE(SUM(outstanding_balance),0) FROM suppliers`)
	d.NetPosition = d.TotalCash.Add(d.StockValue).Add(d.Receivables).Sub(d.Payables)

	return response.RenderPage(c, adminpages.CashflowPage(d))
}
