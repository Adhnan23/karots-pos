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
