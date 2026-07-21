// Package recipes owns product bills of materials: what one unit of a service
// consumes when it is sold. The core sale transaction already depletes declared
// components FEFO and uses their summed cost as the line's COGS; a recipe is
// simply a stored, reusable declaration of those components.
package recipes

import "github.com/shopspring/decimal"

// stockScale is the number of decimal places the stock columns can hold
// (migration 0045). Consumption is rounded to it so what is computed is exactly
// what is stored, rather than being silently truncated by the database.
const stockScale = 6

// Component is one ingredient of a recipe. Exactly one of QtyPerUnit and
// YieldUnits is set, enforced by product_recipes_qty_xor_yield.
type Component struct {
	ComponentProductID int64               `db:"component_product_id"`
	QtyPerUnit         decimal.NullDecimal `db:"qty_per_unit"`
	YieldUnits         decimal.NullDecimal `db:"yield_units"`
	WholeUnits         bool                `db:"whole_units"`
	// joined, for display
	ComponentName string `db:"component_name"`
	UnitAbbr      string `db:"unit_abbr"`
	// CostPrice is the component's current cost, used only to estimate what one
	// unit of the finished item costs. The COGS actually booked on a sale comes
	// from the batches depleted FEFO, not from here.
	CostPrice decimal.Decimal `db:"cost_price"`
}

// CostLine is a part of a recipe that is not stock: electricity for the coffee
// machine, a service fee, gas. It is a costing aid only — see Costs for why it
// never reaches the sale or the P&L.
type CostLine struct {
	ID          int64           `db:"id"`
	Label       string          `db:"label"`
	CostPerUnit decimal.Decimal `db:"cost_per_unit"`
}

// Costs splits what one unit of a service costs into the part that moves stock
// and the part that does not.
//
// Only Stock can ever become COGS. Other is an estimate the owner types in, and
// the real bill behind it is already recorded as an expense tagged to this
// service; adding it to COGS as well would charge the shop twice for the same
// electricity. True exists purely to answer "what should I charge for a cup?".
type Costs struct {
	Stock decimal.Decimal
	Other decimal.Decimal
}

func (c Costs) True() decimal.Decimal { return c.Stock.Add(c.Other) }

// CostPerUnit estimates the cost of making one unit, reusing Consumed so a
// yield component ("this bag makes 50 cups") is divided exactly as it is when
// stock is actually deducted.
func CostPerUnit(cs []Component, costs []CostLine) Costs {
	out := Costs{Stock: decimal.Zero, Other: decimal.Zero}
	one := decimal.NewFromInt(1)
	for _, c := range cs {
		out.Stock = out.Stock.Add(c.Consumed(one).Mul(c.CostPrice))
	}
	for _, l := range costs {
		out.Other = out.Other.Add(l.CostPerUnit)
	}
	return out
}

// Consumption is one component and how much of it a sale line uses.
type Consumption struct {
	ProductID int64
	Qty       decimal.Decimal
}

// Consumed returns how much of this component a sale of saleQty units eats.
//
// A yield component divides (1 unit spread across YieldUnits sales) and stays
// fractional. A whole-unit component rounds UP, because a single copy uses a
// whole sheet of paper. Applying that rounding to everything — which the
// documents plugin used to do — made a one-copy job consume an entire toner.
func (c Component) Consumed(saleQty decimal.Decimal) decimal.Decimal {
	if !saleQty.IsPositive() {
		return decimal.Zero
	}
	var per decimal.Decimal
	switch {
	case c.YieldUnits.Valid && c.YieldUnits.Decimal.IsPositive():
		// DivRound rather than storing a reciprocal: the owner knows "50 cups",
		// and 1/3000 written into a fixed-scale column loses precision.
		per = decimal.NewFromInt(1).DivRound(c.YieldUnits.Decimal, stockScale+2)
	case c.QtyPerUnit.Valid:
		per = c.QtyPerUnit.Decimal
	default:
		return decimal.Zero
	}
	used := per.Mul(saleQty)
	if c.WholeUnits {
		return used.Ceil()
	}
	return used.Round(stockScale)
}

// Expand turns a recipe into the component list the sale transaction consumes.
// Components that work out to nothing are dropped rather than emitted as
// zero-quantity movements.
func Expand(cs []Component, saleQty decimal.Decimal) []Consumption {
	out := make([]Consumption, 0, len(cs))
	for _, c := range cs {
		q := c.Consumed(saleQty)
		if q.IsPositive() {
			out = append(out, Consumption{ProductID: c.ComponentProductID, Qty: q})
		}
	}
	return out
}
