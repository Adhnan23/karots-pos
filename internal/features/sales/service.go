package sales

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/customers"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/recipes"
	"karots-pos/internal/features/stock"
	"karots-pos/internal/features/warranty"
	"karots-pos/internal/money"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

type Service struct {
	db   *sqlx.DB
	repo *Repository
}

func NewService(db *sqlx.DB) *Service { return &Service{db: db, repo: NewRepository(db)} }

type ItemInput struct {
	ProductID    int64  `json:"product_id"    validate:"required,gt=0"`
	Quantity     string `json:"quantity"      validate:"required"`
	Discount     string `json:"discount"`
	DiscountType string `json:"discount_type" validate:"omitempty,oneof=fixed percent"`
	// PriceOverride sets the unit price for a service line (is_service products,
	// e.g. a recharge top-up amount). Ignored for normal stocked products, whose
	// price is always recomputed server-side from the catalogue.
	PriceOverride string `json:"price_override"`
	// BatchID names the lot the cashier picked at the "which price?" prompt when a
	// product's live batches disagree on price (an old bottle stickered at the old
	// price alongside newly-received stock). The client sends only the id — the
	// price is read from the batch here, never accepted from the client — and the
	// sale then depletes THAT lot instead of taking FEFO. Zero means the normal
	// path. Ignored for is_service products, which hold no stock.
	BatchID int64 `json:"batch_id"`
	// Serials carries one unique serial number per unit for serial-tracked
	// products (length must equal the quantity); ignored for other products.
	Serials []string `json:"serials"`
	// Description is an optional per-line label shown on the receipt/history
	// instead of the product name (e.g. a plugin service line "A4 colour x20").
	Description string `json:"description"`
	// Components lists stock to deplete for a service line (e.g. a document job
	// consuming paper). Honoured only for is_service products; the line's
	// cost_price becomes the summed FEFO cost of the consumed components.
	Components []ServiceComponent `json:"components"`
}

// ServiceComponent is one consumable a service line draws down from stock.
type ServiceComponent struct {
	ProductID int64  `json:"product_id"`
	Quantity  string `json:"quantity"`
}

// ServiceComponentParsed is a validated component (qty parsed), used to record the
// stock movement for a service line's consumption after the sale id is known.
type ServiceComponentParsed struct {
	ProductID int64
	Qty       decimal.Decimal
}

// serialBatch holds the captured serials for one serial-tracked line, recorded
// as warranty units once the sale id is known.
type serialBatch struct {
	productID int64
	months    int
	serials   []string
}

type PaymentInput struct {
	Method    string  `json:"method"    validate:"required,oneof=cash card online wallet credit"`
	Amount    string  `json:"amount"    validate:"required"`
	Reference *string `json:"reference"`
}

type CreateInput struct {
	CustomerID   *int64         `json:"customer_id"`
	SaleType     string         `json:"sale_type"     validate:"required,oneof=retail wholesale"`
	Discount     string         `json:"discount"`
	DiscountType string         `json:"discount_type" validate:"omitempty,oneof=fixed percent"`
	Notes        *string        `json:"notes"`
	Items        []ItemInput    `json:"items"    validate:"required,min=1,dive"`
	Payments     []PaymentInput `json:"payments" validate:"dive"`
}

var hundred = decimal.NewFromInt(100)

// normDiscountType defaults a blank discount type to "fixed".
func normDiscountType(t string) string {
	if t == "percent" {
		return "percent"
	}
	return "fixed"
}

// returnedLotPrice is the price to stamp on the lot that returned goods re-enter
// stock in. It carries the sold price back ONLY for a line that was rung from a
// picked lot — an old bottle sold at the old price goes back on the shelf at the
// old price, and can be picked again at the till.
//
// Every other line returns zero, leaving the lot following the product. That is
// deliberate: a wholesale line's unit price is a customer-type price, and
// stamping it here would put the goods back on the shelf at the wholesale rate.
func returnedLotPrice(it SaleItem) decimal.Decimal {
	if it.BatchID == nil {
		return decimal.Zero
	}
	return it.UnitPrice
}

// resolveBillDiscount turns the bill-level discount (a flat fixed amount, or a
// percentage of base) into a concrete amount, clamped to [0, base].
func resolveBillDiscount(dtype string, value, base decimal.Decimal) decimal.Decimal {
	amt := value
	if dtype == "percent" {
		amt = base.Mul(value).Div(hundred).Round(2)
	}
	return clampDiscount(amt, base)
}

// resolveItemDiscount turns a per-item discount into a line amount. A fixed
// value is PER UNIT and multiplies by quantity (Rs 5 off × 3 = Rs 15); a percent
// is taken off the line gross (equivalent to % of the unit price). Clamped to
// [0, lineGross].
func resolveItemDiscount(dtype string, value, lineGross, qty decimal.Decimal) decimal.Decimal {
	amt := value.Mul(qty).Round(2)
	if dtype == "percent" {
		amt = lineGross.Mul(value).Div(hundred).Round(2)
	}
	return clampDiscount(amt, lineGross)
}

func clampDiscount(amt, base decimal.Decimal) decimal.Decimal {
	if amt.IsNegative() {
		return decimal.Zero
	}
	if amt.GreaterThan(base) {
		return base
	}
	return amt
}

// Create writes a sale atomically. The whole computation — pricing, stock
// guard, audit, customer credit — happens inside one transaction so a failure
// at any step rolls everything back.
func (s *Service) Create(ctx context.Context, in CreateInput, cashierID int64) (*Detail, error) {
	// Bill-level discount: the cashier enters a value that is either a fixed
	// amount or a percentage (resolved against the pre-tax net, after item
	// discounts, once the lines are totalled below).
	billDiscValue, err := money.Parse(in.Discount)
	if err != nil || billDiscValue.IsNegative() {
		return nil, apperr.Validation("discount must be a non-negative amount")
	}
	billDiscType := normDiscountType(in.DiscountType)

	var detail *Detail
	err = appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		var (
			saleRepo = NewRepository(tx)
			prodRepo = products.NewRepository(tx)
			stkRepo  = stock.NewRepository(tx)
			custRepo = customers.NewRepository(tx)
		)

		subtotal := decimal.Zero
		taxTotal := decimal.Zero
		itemDiscTotal := decimal.Zero
		lines := make([]SaleItem, 0, len(in.Items))
		// Service products (is_service) carry no inventory: they skip stock
		// depletion above and the ledger movement below.
		serviceProducts := map[int64]bool{}
		// componentMoves holds, per line index, the consumables a service line drew
		// down (e.g. paper for a document job) — recorded as movements once the sale
		// id exists.
		componentMoves := map[int][]ServiceComponentParsed{}

		warrRepo := warranty.NewRepository(tx)
		serialSeen := map[string]bool{}
		var serialBatches []serialBatch

		for _, it := range in.Items {
			p, err := prodRepo.FindByID(ctx, it.ProductID)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return apperr.Validation(fmt.Sprintf("product %d not found", it.ProductID))
				}
				return apperr.Internal("failed to load product", err)
			}
			qty, err := money.Parse(it.Quantity)
			if err != nil || !qty.IsPositive() {
				return apperr.Validation(fmt.Sprintf("quantity for %s must be greater than zero", p.Name))
			}
			// Whole-only units (pieces, packets…) reject fractional quantities;
			// only units flagged allow_decimal (kg, g, ltr, ml) may be fractional.
			if !p.UnitAllowDecimal && !qty.Equal(qty.Truncate(0)) {
				return apperr.Validation(fmt.Sprintf("quantity for %s must be a whole number", p.Name))
			}
			// Serial-tracked products capture one unique serial per unit; validate
			// here and record them as warranty units once the sale id is known.
			if p.TrackSerial {
				if !qty.Equal(qty.Truncate(0)) {
					return apperr.Validation(fmt.Sprintf("%s is serial-tracked — quantity must be a whole number", p.Name))
				}
				want := int(qty.IntPart())
				serials := make([]string, 0, want)
				for _, raw := range it.Serials {
					if sn := strings.TrimSpace(raw); sn != "" {
						serials = append(serials, sn)
					}
				}
				if len(serials) != want {
					return apperr.Validation(fmt.Sprintf("enter %d serial number(s) for %s", want, p.Name))
				}
				for _, sn := range serials {
					if serialSeen[sn] {
						return apperr.Validation(fmt.Sprintf("duplicate serial number %q", sn))
					}
					serialSeen[sn] = true
					exists, err := warrRepo.SerialExists(ctx, sn)
					if err != nil {
						return apperr.Internal("failed to check serial", err)
					}
					if exists {
						return apperr.Validation(fmt.Sprintf("serial number %q is already on record", sn))
					}
				}
				serialBatches = append(serialBatches, serialBatch{productID: p.ID, months: p.WarrantyMonths, serials: serials})
			}
			discValue, err := money.Parse(it.Discount)
			if err != nil || discValue.IsNegative() {
				return apperr.Validation(fmt.Sprintf("discount for %s is invalid", p.Name))
			}
			discType := normDiscountType(it.DiscountType)

			// The cashier picked a specific lot at the till because it matches the
			// sticker on the package. Lock it now: it decides both the price below
			// and which batch is depleted further down, and locking here means a
			// concurrent sale cannot empty it between the check and the take.
			var pickedBatch *stock.Batch
			if it.BatchID > 0 && !p.IsService {
				pickedBatch, err = stkRepo.LockBatch(ctx, it.BatchID, p.ID)
				if err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						return apperr.Validation(fmt.Sprintf("that price is no longer available for %s — scan it again", p.Name))
					}
					return apperr.Internal("failed to load batch", err)
				}
				if pickedBatch.QtyRemaining.LessThan(qty) {
					return apperr.Conflict(fmt.Sprintf(
						"only %s of %s left at that price", pickedBatch.QtyRemaining.String(), p.Name))
				}
			}

			unitPrice := p.SellingPrice
			if p.IsService && strings.TrimSpace(it.PriceOverride) != "" {
				// Service lines (e.g. recharge) carry a per-line amount, not a
				// fixed catalogue price.
				ov, err := money.Parse(it.PriceOverride)
				if err != nil || ov.IsNegative() {
					return apperr.Validation(fmt.Sprintf("price for %s is invalid", p.Name))
				}
				unitPrice = ov
			} else if in.SaleType == "wholesale" && p.WholesalePrice.IsPositive() {
				// Wholesale wins over a lot price: it is a customer-type price, not
				// a property of the lot sitting on the shelf.
				unitPrice = p.WholesalePrice
			} else if pickedBatch != nil && pickedBatch.SellingPrice.IsPositive() {
				// This lot was received at its own price, which is what the package
				// in the customer's hand is stickered with — so that is what it
				// rings up at, whatever the shelf price has since moved to. A lot
				// with no price of its own keeps following the product.
				unitPrice = pickedBatch.SellingPrice
			}
			lineGross := qty.Mul(unitPrice).Round(2)
			// Per-item discount: fixed is per-unit (× qty), percent is off the line.
			// Clamped to [0, lineGross] so the net is never negative.
			disc := resolveItemDiscount(discType, discValue, lineGross, qty)
			lineNet := lineGross.Sub(disc)
			lineTax := lineNet.Mul(p.TaxRate).Div(hundred).Round(2)

			subtotal = subtotal.Add(lineGross)
			itemDiscTotal = itemDiscTotal.Add(disc)
			taxTotal = taxTotal.Add(lineTax)

			// Service lines (is_service) carry no inventory of their own. They may
			// still declare components to consume (e.g. paper for a document job):
			// deplete those FEFO and use their summed cost as the line COGS.
			cost := decimal.Zero
			var lineComps []ServiceComponentParsed
			if p.IsService {
				serviceProducts[p.ID] = true
				// A stored recipe supplies the components when the caller did not.
				// The documents plugin passes explicit components (its paper choice
				// depends on the size picked at the till), so an explicit list always
				// wins; everything else — coffee, any service with a recipe — is
				// expanded here so no plugin is needed to sell it.
				if len(it.Components) == 0 {
					rcs, rerr := recipes.NewRepository(tx).For(ctx, p.ID)
					if rerr != nil {
						return apperr.Internal("failed to load recipe", rerr)
					}
					for _, cons := range recipes.Expand(rcs, qty) {
						it.Components = append(it.Components, ServiceComponent{
							ProductID: cons.ProductID,
							Quantity:  cons.Qty.String(),
						})
					}
				}
				for _, comp := range it.Components {
					if comp.ProductID <= 0 {
						continue
					}
					cq, err := money.Parse(comp.Quantity)
					if err != nil || !cq.IsPositive() {
						return apperr.Validation(fmt.Sprintf("invalid consumable quantity for %s", p.Name))
					}
					ok, err := stkRepo.DecrementGuarded(ctx, comp.ProductID, cq)
					if err != nil {
						return apperr.Internal("failed to update stock", err)
					}
					if !ok {
						return apperr.Conflict("insufficient stock for a consumable used by " + p.Name)
					}
					ccost, err := stkRepo.DepleteFEFO(ctx, comp.ProductID, cq)
					if err != nil {
						return apperr.Internal("failed to deplete batches", err)
					}
					// DepleteFEFO returns the weighted cost *per consumed unit*; the
					// line's COGS is the total consumed (per-unit × qty) of every
					// component, divided by the line quantity so cost_price stays a
					// per-unit figure like every other sale line.
					cost = cost.Add(ccost.Mul(cq))
					lineComps = append(lineComps, ServiceComponentParsed{ProductID: comp.ProductID, Qty: cq})
				}
				if qty.IsPositive() {
					cost = cost.Div(qty).Round(2)
				}
			}
			if !p.IsService {
				// Atomic guard: prevents overselling under concurrency.
				ok, err := stkRepo.DecrementGuarded(ctx, p.ID, qty)
				if err != nil {
					return apperr.Internal("failed to update stock", err)
				}
				if !ok {
					return apperr.Conflict(fmt.Sprintf("insufficient stock for %s", p.Name))
				}
				// Deplete batches FEFO; the weighted cost of the consumed units is the
				// COGS snapshot for this line (more accurate than the product's current
				// cost when batches have different costs).
				//
				// A picked lot overrides FEFO: the customer is holding that package,
				// so that is the one leaving the shelf. Its own cost is the COGS.
				if pickedBatch != nil {
					cost, err = stkRepo.DepleteBatch(ctx, pickedBatch.ID, qty)
				} else {
					cost, err = stkRepo.DepleteFEFO(ctx, p.ID, qty)
				}
				if err != nil {
					return apperr.Internal("failed to deplete batches", err)
				}
				if cost.IsZero() {
					cost = p.CostPrice
				}
			}

			var desc *string
			if s := strings.TrimSpace(it.Description); s != "" {
				desc = &s
			}
			var batchID *int64
			if pickedBatch != nil {
				batchID = &pickedBatch.ID
			}
			lines = append(lines, SaleItem{
				BatchID:       batchID,
				ProductID:     p.ID,
				Quantity:      qty,
				UnitPrice:     unitPrice,
				CostPrice:     cost,
				Discount:      disc,
				DiscountType:  discType,
				DiscountValue: discValue,
				Subtotal:      lineNet,
				Description:   desc,
			})
			if len(lineComps) > 0 {
				componentMoves[len(lines)-1] = lineComps
			}
		}

		// Resolve the bill discount against the pre-tax net (after item discounts).
		billDiscount := resolveBillDiscount(billDiscType, billDiscValue, subtotal.Sub(itemDiscTotal))
		discount := itemDiscTotal.Add(billDiscount)
		total := subtotal.Sub(discount).Add(taxTotal).Round(2)
		if total.IsNegative() {
			return apperr.Validation("bill discount exceeds the sale total")
		}

		methods := make([]string, 0, len(in.Payments))
		amounts := make([]decimal.Decimal, 0, len(in.Payments))
		for _, p := range in.Payments {
			amt, err := money.Parse(p.Amount)
			if err != nil || amt.IsNegative() {
				return apperr.Validation("payment amount is invalid")
			}
			methods = append(methods, p.Method)
			amounts = append(amounts, amt)
		}
		tender := SplitTender(methods, amounts)

		// The customer is only loaded when something is going on their account,
		// so an ordinary cash sale still costs no extra query.
		available := decimal.Zero
		var cust *customers.Customer
		if tender.OnAccount.IsPositive() && in.CustomerID != nil {
			c, err := custRepo.FindByID(ctx, *in.CustomerID)
			if err != nil {
				return apperr.Validation("selected customer not found")
			}
			cust = c
			available = c.AvailableCredit()
		}
		if err := CheckTender(tender, total, in.CustomerID != nil, available); err != nil {
			return err
		}

		// Status follows the on-account line alone. It used to follow
		// underpayment, which converted a sale to credit silently — the debt was
		// then recorded as an ordinary retail sale and never showed in the
		// receipts list.
		status := "completed"
		change := tender.Paid.Add(tender.OnAccount).Sub(total)
		if tender.OnAccount.IsPositive() {
			if err := custRepo.AddBalance(ctx, cust.ID, tender.OnAccount); err != nil {
				return apperr.Internal("failed to update customer balance", err)
			}
			status = "credit"
			change = decimal.Zero
		}

		receiptNo, err := saleRepo.NextReceiptNo(ctx)
		if err != nil {
			return apperr.Internal("failed to allocate receipt number", err)
		}

		saleID, err := saleRepo.InsertSale(ctx, saleRow{
			ReceiptNo:     receiptNo,
			CustomerID:    in.CustomerID,
			SaleType:      in.SaleType,
			Subtotal:      subtotal,
			Discount:      discount,
			DiscountType:  billDiscType,
			DiscountValue: billDiscValue,
			Tax:           taxTotal,
			Total:         total,
			// Money actually received — the on-account part is a debt, not a payment.
			PaidAmount:  tender.Paid,
			ChangeGiven: change,
			Status:      status,
			CashierID:   cashierID,
			Notes:       in.Notes,
		})
		if err != nil {
			return apperr.Internal("failed to save sale", err)
		}

		for i := range lines {
			lines[i].SaleID = saleID
			if err := saleRepo.InsertItem(ctx, saleID, lines[i]); err != nil {
				return apperr.Internal("failed to save sale item", err)
			}
			if serviceProducts[lines[i].ProductID] {
				// Service line: no inventory of its own, but record a movement for
				// each consumable it drew down (e.g. paper for a document job).
				refType := "sale"
				for _, comp := range componentMoves[i] {
					if err := stkRepo.InsertMovement(ctx, stock.MovementInput{
						ProductID:     comp.ProductID,
						Type:          stock.MoveSale,
						Quantity:      comp.Qty.Neg(),
						ReferenceID:   &saleID,
						ReferenceType: &refType,
						UserID:        cashierID,
					}); err != nil {
						return apperr.Internal("failed to record stock movement", err)
					}
				}
				continue
			}
			neg := lines[i].Quantity.Neg()
			refType := "sale"
			if err := stkRepo.InsertMovement(ctx, stock.MovementInput{
				ProductID:     lines[i].ProductID,
				Type:          stock.MoveSale,
				Quantity:      neg,
				ReferenceID:   &saleID,
				ReferenceType: &refType,
				UserID:        cashierID,
			}); err != nil {
				return apperr.Internal("failed to record stock movement", err)
			}
		}

		// Record captured serials as warranty units, now that the sale id exists.
		if len(serialBatches) > 0 {
			now := time.Now()
			for _, sb := range serialBatches {
				for _, sn := range sb.serials {
					if _, err := warrRepo.InsertUnit(ctx, warranty.NewUnit{
						ProductID:      sb.productID,
						SerialNo:       sn,
						SaleID:         &saleID,
						CustomerID:     in.CustomerID,
						SoldAt:         now,
						WarrantyMonths: sb.months,
						WarrantyUntil:  warranty.Until(now, sb.months),
						Source:         "sale",
					}); err != nil {
						return apperr.Internal("failed to record warranty serial", err)
					}
				}
			}
		}

		for _, p := range in.Payments {
			amt, err := money.Parse(p.Amount)
			if err != nil {
				return apperr.Validation("payment amount is invalid")
			}
			// Skip blank/zero tender lines (e.g. an unused method in a multi-tender
			// form); negative amounts were already rejected above.
			if !amt.IsPositive() {
				continue
			}
			if err := saleRepo.InsertPayment(ctx, saleID, p.Method, amt, p.Reference); err != nil {
				return apperr.Internal("failed to save payment", err)
			}
		}

		d, err := s.loadDetail(ctx, saleRepo, saleID)
		if err != nil {
			return err
		}
		detail = d
		return nil
	})
	if err != nil {
		return nil, err
	}
	return detail, nil
}

func (s *Service) Get(ctx context.Context, id int64) (*Detail, error) {
	return s.loadDetail(ctx, s.repo, id)
}

// ReturnReceipt loads the most recent return on a sale for a printed refund slip.
func (s *Service) ReturnReceipt(ctx context.Context, saleID int64) (*ReturnReceipt, error) {
	rr, err := s.repo.LatestReturn(ctx, saleID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("return")
		}
		return nil, apperr.Internal("failed to load return", err)
	}
	return rr, nil
}

// Return reverses a completed sale: every line is restocked and audited, any
// credit balance the sale created is removed from the customer, and the sale is
// marked 'returned' (which also excludes it from revenue in reports). One tx.
func (s *Service) Return(ctx context.Context, id int64, userID int64) (*Detail, error) {
	var detail *Detail
	err := appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		saleRepo := NewRepository(tx)
		stkRepo := stock.NewRepository(tx)
		custRepo := customers.NewRepository(tx)

		sale, err := saleRepo.FindByID(ctx, id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return apperr.NotFound("sale")
			}
			return apperr.Internal("failed to load sale", err)
		}
		if sale.Status == "returned" {
			return apperr.Conflict("this sale has already been returned")
		}
		// A partial return already restocked some lines AND reduced the customer's
		// credit by that portion. A full return here would remove the full original
		// owed again, over-crediting the customer — so funnel the remainder through
		// line-level returns (PartialReturn), which is credit-correct.
		if sale.Status == "partially_returned" {
			return apperr.Conflict("this sale was already partially returned — use line returns for the rest")
		}

		items, err := saleRepo.Items(ctx, id)
		if err != nil {
			return apperr.Internal("failed to load sale items", err)
		}
		// Service lines (recharge/airtime) can't be returned. If the sale still has
		// one outstanding, block the whole-sale return rather than silently dropping
		// it — the cashier can line-return the other items.
		for _, it := range items {
			if it.IsService && it.Quantity.Sub(it.ReturnedQty).IsPositive() {
				return apperr.Conflict("this sale includes a recharge item, which can't be returned")
			}
		}
		ref := "sale"
		for _, it := range items {
			remaining := it.Quantity.Sub(it.ReturnedQty)
			if !remaining.IsPositive() {
				continue
			}
			if err := stkRepo.Increment(ctx, it.ProductID, remaining); err != nil {
				return apperr.Internal("failed to restock", err)
			}
			// Put it back where it came from when we know; only open a return lot
			// when we do not, so returns stop fragmenting the batch list.
			back, berr := stkRepo.RestockLot(ctx, it.BatchID, it.ProductID, remaining)
			if berr != nil {
				return apperr.Internal("failed to restock batch", berr)
			}
			if !back {
				if _, err := stkRepo.InsertBatch(ctx, stock.NewBatch{
					ProductID: it.ProductID, Quantity: remaining, CostPrice: it.CostPrice,
					SellingPrice: returnedLotPrice(it), Source: "return",
				}); err != nil {
					return apperr.Internal("failed to restock batch", err)
				}
			}
			if err := stkRepo.InsertMovement(ctx, stock.MovementInput{
				ProductID: it.ProductID, Type: stock.MoveReturn, Quantity: remaining,
				ReferenceID: &id, ReferenceType: &ref, UserID: userID,
				Note: strPtr("return of " + sale.ReceiptNo),
			}); err != nil {
				return apperr.Internal("failed to record return movement", err)
			}
			if err := saleRepo.MarkItemFullyReturned(ctx, it.ID); err != nil {
				return apperr.Internal("failed to mark line returned", err)
			}
		}

		// Remove any credit this sale created from the customer's balance.
		if sale.CustomerID != nil {
			if owed := sale.Total.Sub(sale.PaidAmount); owed.IsPositive() {
				if err := custRepo.AddBalance(ctx, *sale.CustomerID, owed.Neg()); err != nil {
					return apperr.Internal("failed to adjust customer balance", err)
				}
			}
		}

		if err := saleRepo.UpdateStatus(ctx, id, "returned"); err != nil {
			return apperr.Internal("failed to update sale status", err)
		}
		d, err := s.loadDetail(ctx, saleRepo, id)
		if err != nil {
			return err
		}
		detail = d
		return nil
	})
	if err != nil {
		return nil, err
	}
	return detail, nil
}

func strPtr(s string) *string { return &s }

// ReturnLineInput is one line of a partial return: how much of a specific sale
// item to send back.
type ReturnLineInput struct {
	SaleItemID int64  `json:"sale_item_id" validate:"required,gt=0"`
	Quantity   string `json:"quantity"     validate:"required"`
	// Disposition decides what happens to the returned goods: "restock" (default)
	// puts them back into sellable stock; "damage" writes them off as a loss so
	// faulty items never re-enter inventory. The customer is refunded either way.
	Disposition string `json:"disposition"`
}

type PartialReturnInput struct {
	Reason *string           `json:"reason"`
	Lines  []ReturnLineInput `json:"lines" validate:"required,min=1,dive"`
}

// PartialReturn sends back specific quantities of specific lines. It restocks
// (into a return batch), reduces the customer's credit for the credit portion
// and treats the rest as a cash refund, records the return, and moves the sale
// to 'partially_returned' (or 'returned' when nothing is left). One tx.
// The returned decimal is the cash-refund portion (refund value minus any credit
// reduction), so the caller can post it to the cashier's drawer ledger.
func (s *Service) PartialReturn(ctx context.Context, saleID int64, in PartialReturnInput, userID int64) (*Detail, decimal.Decimal, error) {
	var detail *Detail
	cashRefund := decimal.Zero
	err := appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
		d, refund, _, err := s.PartialReturnTx(ctx, tx, saleID, in, userID)
		if err != nil {
			return err
		}
		detail, cashRefund = d, refund
		return nil
	})
	if err != nil {
		return nil, decimal.Zero, err
	}
	return detail, cashRefund, nil
}

// PartialReturnTx performs a partial return over an existing transaction, so a
// caller can book the cash refund (cashflow.MoveTx) atomically in the same tx.
// Returns the refreshed sale detail, the cash-refund amount and the return id.
func (s *Service) PartialReturnTx(ctx context.Context, tx *sqlx.Tx, saleID int64, in PartialReturnInput, userID int64) (*Detail, decimal.Decimal, int64, error) {
	cashRefund := decimal.Zero
	var detail *Detail
	var returnID int64
	err := func() error {
		saleRepo := NewRepository(tx)
		stkRepo := stock.NewRepository(tx)
		custRepo := customers.NewRepository(tx)

		sale, err := saleRepo.FindByID(ctx, saleID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return apperr.NotFound("sale")
			}
			return apperr.Internal("failed to load sale", err)
		}
		if sale.Status == "returned" {
			return apperr.Conflict("this sale has already been fully returned")
		}

		ref := "sale"
		totalRefundValue := decimal.Zero
		returnID, err = saleRepo.InsertSaleReturn(ctx, saleID, userID, decimal.Zero, decimal.Zero, in.Reason)
		if err != nil {
			return apperr.Internal("failed to open return", err)
		}

		for _, ln := range in.Lines {
			qty, err := money.Parse(ln.Quantity)
			if err != nil || !qty.IsPositive() {
				return apperr.Validation("return quantity must be greater than zero")
			}
			it, err := saleRepo.FindItem(ctx, saleID, ln.SaleItemID)
			if err != nil {
				return apperr.Validation("sale line not found on this sale")
			}
			if it.IsService {
				return apperr.Conflict(fmt.Sprintf("%s is a recharge item and can't be returned", it.ProductName))
			}
			if qty.GreaterThan(it.ReturnableQty()) {
				return apperr.Conflict(fmt.Sprintf("cannot return %s of %s — only %s remain",
					money.Display(qty), it.ProductName, money.Display(it.ReturnableQty())))
			}
			ok, err := saleRepo.AddReturnedQty(ctx, it.ID, qty)
			if err != nil {
				return apperr.Internal("failed to update returned qty", err)
			}
			if !ok {
				return apperr.Conflict("return quantity exceeds what was sold")
			}
			// per-unit refund value = line net / line qty
			unitValue := it.Subtotal.Div(it.Quantity).Round(2)
			lineRefund := unitValue.Mul(qty).Round(2)
			totalRefundValue = totalRefundValue.Add(lineRefund)

			if err := stkRepo.Increment(ctx, it.ProductID, qty); err != nil {
				return apperr.Internal("failed to restock", err)
			}
			back, berr := stkRepo.RestockLot(ctx, it.BatchID, it.ProductID, qty)
			if berr != nil {
				return apperr.Internal("failed to restock batch", berr)
			}
			if !back {
				if _, err := stkRepo.InsertBatch(ctx, stock.NewBatch{
					ProductID: it.ProductID, Quantity: qty, CostPrice: it.CostPrice,
					SellingPrice: returnedLotPrice(*it), Source: "return",
				}); err != nil {
					return apperr.Internal("failed to restock batch", err)
				}
			}
			if err := stkRepo.InsertMovement(ctx, stock.MovementInput{
				ProductID: it.ProductID, Type: stock.MoveReturn, Quantity: qty,
				ReferenceID: &returnID, ReferenceType: &ref, UserID: userID,
				Note: strPtr("return of " + sale.ReceiptNo),
			}); err != nil {
				return apperr.Internal("failed to record return movement", err)
			}
			if err := saleRepo.InsertSaleReturnItem(ctx, returnID, it.ID, it.ProductID, qty, lineRefund); err != nil {
				return apperr.Internal("failed to record return line", err)
			}

			// "Send to damage": the cashier judged these goods faulty, so don't
			// leave them in sellable stock. They came back in just above (return
			// movement + batch); now write them straight back out through the same
			// path as the Damage screen — a damage movement valued at FEFO cost.
			// Net sellable stock is unchanged and BOTH the returns and the
			// damage/loss reports stay accurate. The refund is unaffected.
			if strings.EqualFold(strings.TrimSpace(ln.Disposition), "damage") {
				ok, err := stkRepo.DecrementGuarded(ctx, it.ProductID, qty)
				if err != nil {
					return apperr.Internal("failed to write off damaged return", err)
				}
				if !ok {
					return apperr.Conflict("not enough stock to write off the damaged return")
				}
				cost, err := stkRepo.DepleteFEFO(ctx, it.ProductID, qty)
				if err != nil {
					return apperr.Internal("failed to deplete batches", err)
				}
				if err := stkRepo.InsertMovement(ctx, stock.MovementInput{
					ProductID: it.ProductID, Type: stock.MoveDamage, Quantity: qty.Neg(),
					ReferenceID: &returnID, ReferenceType: &ref, UserID: userID,
					Note: strPtr("damaged on return of " + sale.ReceiptNo),
					Cost: cost.Mul(qty),
				}); err != nil {
					return apperr.Internal("failed to record damage movement", err)
				}
			}
		}

		// Split the returned value: credit portion reduces the customer balance,
		// the remainder is a cash refund.
		creditReduction := decimal.Zero
		if sale.CustomerID != nil {
			owed := sale.Total.Sub(sale.PaidAmount)
			if owed.IsPositive() {
				creditReduction = decimal.Min(totalRefundValue, owed)
				if err := custRepo.AddBalance(ctx, *sale.CustomerID, creditReduction.Neg()); err != nil {
					return apperr.Internal("failed to adjust customer balance", err)
				}
			}
		}
		refund := totalRefundValue.Sub(creditReduction)
		cashRefund = refund
		if err := saleRepo.SetReturnTotals(ctx, returnID, refund, creditReduction); err != nil {
			return apperr.Internal("failed to finalize return", err)
		}

		// Recompute sale status.
		outstanding, err := saleRepo.OutstandingItems(ctx, saleID)
		if err != nil {
			return apperr.Internal("failed to recompute status", err)
		}
		newStatus := "partially_returned"
		if outstanding == 0 {
			newStatus = "returned"
		}
		if err := saleRepo.UpdateStatus(ctx, saleID, newStatus); err != nil {
			return apperr.Internal("failed to update sale status", err)
		}

		d, err := s.loadDetail(ctx, saleRepo, saleID)
		if err != nil {
			return err
		}
		detail = d
		return nil
	}()
	if err != nil {
		return nil, decimal.Zero, 0, err
	}
	return detail, cashRefund, returnID, nil
}

func (s *Service) loadDetail(ctx context.Context, repo *Repository, id int64) (*Detail, error) {
	sale, err := repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("sale")
		}
		return nil, apperr.Internal("failed to load sale", err)
	}
	items, err := repo.Items(ctx, id)
	if err != nil {
		return nil, apperr.Internal("failed to load sale items", err)
	}
	payments, err := repo.Payments(ctx, id)
	if err != nil {
		return nil, apperr.Internal("failed to load payments", err)
	}
	return &Detail{Sale: *sale, Items: items, Payments: payments}, nil
}

func (s *Service) List(ctx context.Context, f ListFilter) ([]Sale, error) {
	rows, err := s.repo.List(ctx, f)
	if err != nil {
		return nil, apperr.Internal("failed to list sales", err)
	}
	return rows, nil
}

// Summarize returns the totals for every sale matching f, regardless of paging.
func (s *Service) Summarize(ctx context.Context, f ListFilter) (*ListSummary, error) {
	sum, err := s.repo.Summarize(ctx, f)
	if err != nil {
		return nil, apperr.Internal("failed to summarise sales", err)
	}
	return sum, nil
}

// MethodTotal is the money taken via one payment method in a period.
type MethodTotal struct {
	Method string          `db:"method" json:"method"`
	Amount decimal.Decimal `db:"amount" json:"amount"`
}

// PeriodSummary aggregates a cashier's sales within a time window, for the
// day-end (Z) report.
type PeriodSummary struct {
	Count    int             `json:"count"`
	Gross    decimal.Decimal `json:"gross"`
	Discount decimal.Decimal `json:"discount"`
	Returns  decimal.Decimal `json:"returns"` // value of returned lines on these sales
	Net      decimal.Decimal `json:"net"`     // sales total − returns
	ByMethod []MethodTotal   `json:"by_method"`
}

// PeriodSummary totals a cashier's non-void sales between [from,to] and breaks
// payments down by method.
func (s *Service) PeriodSummary(ctx context.Context, cashierID int64, from, to time.Time) (*PeriodSummary, error) {
	out := &PeriodSummary{}
	var agg struct {
		Count    int             `db:"count"`
		Gross    decimal.Decimal `db:"gross"`
		Discount decimal.Decimal `db:"discount"`
		Net      decimal.Decimal `db:"net"`
	}
	err := s.db.GetContext(ctx, &agg, `
		SELECT COUNT(*) AS count,
		       COALESCE(SUM(subtotal),0) AS gross,
		       COALESCE(SUM(discount),0) AS discount,
		       COALESCE(SUM(total),0)    AS net
		FROM sales
		WHERE cashier_id = $1 AND created_at >= $2 AND created_at <= $3 AND status <> 'void'`,
		cashierID, from, to)
	if err != nil {
		return nil, apperr.Internal("failed to summarize sales", err)
	}
	out.Count, out.Gross, out.Discount = agg.Count, agg.Gross, agg.Discount

	// Returns booked against this cashier's sales in the window, valued per unit
	// at the line's net price — mirrors the P&L so the day-end Net is net of
	// refunds (matches the cash actually kept after refunds out of the drawer).
	var returns decimal.Decimal
	err = s.db.GetContext(ctx, &returns, `
		SELECT COALESCE(SUM((si.subtotal / NULLIF(si.quantity,0)) * si.returned_qty), 0)
		FROM sale_items si JOIN sales s ON s.id = si.sale_id
		WHERE s.cashier_id = $1 AND s.created_at >= $2 AND s.created_at <= $3 AND s.status <> 'void'`,
		cashierID, from, to)
	if err != nil {
		return nil, apperr.Internal("failed to summarize returns", err)
	}
	out.Returns = returns
	out.Net = agg.Net.Sub(returns)

	err = s.db.SelectContext(ctx, &out.ByMethod, `
		SELECT pmt.method AS method, COALESCE(SUM(pmt.amount),0) AS amount
		FROM payments pmt JOIN sales s ON s.id = pmt.sale_id
		WHERE s.cashier_id = $1 AND s.created_at >= $2 AND s.created_at <= $3 AND s.status <> 'void'
		GROUP BY pmt.method ORDER BY pmt.method`,
		cashierID, from, to)
	if err != nil {
		return nil, apperr.Internal("failed to summarize payments", err)
	}
	return out, nil
}

// CashCollectedSince totals cash payments taken by a cashier since a time,
// used to compute the expected drawer cash at register close.
// CashCollectedSince is the NET cash a cashier put in the drawer since `since`:
// cash tendered minus change handed back (change is always cash), so an
// overpaid sale doesn't overstate the expected drawer balance.
func (s *Service) CashCollectedSince(ctx context.Context, cashierID int64, since time.Time) (decimal.Decimal, error) {
	var total decimal.Decimal
	err := s.db.GetContext(ctx, &total, `
		SELECT COALESCE((
			SELECT SUM(pmt.amount) FROM payments pmt
			JOIN sales s ON s.id = pmt.sale_id
			WHERE pmt.method = 'cash' AND s.cashier_id = $1 AND s.created_at >= $2
		), 0) - COALESCE((
			SELECT SUM(s.change_given) FROM sales s
			WHERE s.cashier_id = $1 AND s.created_at >= $2
		), 0)`,
		cashierID, since)
	if err != nil {
		return decimal.Zero, apperr.Internal("failed to total cash", err)
	}
	return total, nil
}
