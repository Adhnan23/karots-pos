package sales

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"karots-pos/internal/apperr"
	appdb "karots-pos/internal/db"
	"karots-pos/internal/features/customers"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/stock"
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
}

type PaymentInput struct {
	Method    string  `json:"method"    validate:"required,oneof=cash card online"`
	Amount    string  `json:"amount"    validate:"required"`
	Reference *string `json:"reference"`
}

type CreateInput struct {
	CustomerID   *int64         `json:"customer_id"`
	SaleType     string         `json:"sale_type"     validate:"required,oneof=retail wholesale credit"`
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
			discValue, err := money.Parse(it.Discount)
			if err != nil || discValue.IsNegative() {
				return apperr.Validation(fmt.Sprintf("discount for %s is invalid", p.Name))
			}
			discType := normDiscountType(it.DiscountType)

			unitPrice := p.SellingPrice
			if in.SaleType == "wholesale" && p.WholesalePrice.IsPositive() {
				unitPrice = p.WholesalePrice
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
			cost, err := stkRepo.DepleteFEFO(ctx, p.ID, qty)
			if err != nil {
				return apperr.Internal("failed to deplete batches", err)
			}
			if cost.IsZero() {
				cost = p.CostPrice
			}

			lines = append(lines, SaleItem{
				ProductID:     p.ID,
				Quantity:      qty,
				UnitPrice:     unitPrice,
				CostPrice:     cost,
				Discount:      disc,
				DiscountType:  discType,
				DiscountValue: discValue,
				Subtotal:      lineNet,
			})
		}

		// Resolve the bill discount against the pre-tax net (after item discounts).
		billDiscount := resolveBillDiscount(billDiscType, billDiscValue, subtotal.Sub(itemDiscTotal))
		discount := itemDiscTotal.Add(billDiscount)
		total := subtotal.Sub(discount).Add(taxTotal).Round(2)
		if total.IsNegative() {
			return apperr.Validation("bill discount exceeds the sale total")
		}

		paid := decimal.Zero
		for _, p := range in.Payments {
			amt, err := money.Parse(p.Amount)
			if err != nil || amt.IsNegative() {
				return apperr.Validation("payment amount is invalid")
			}
			paid = paid.Add(amt)
		}

		status := "completed"
		change := decimal.Zero
		if paid.GreaterThanOrEqual(total) {
			change = paid.Sub(total)
		} else {
			// Underpayment becomes customer credit.
			owed := total.Sub(paid)
			if in.CustomerID == nil {
				return apperr.Validation("a customer is required to put the balance on credit")
			}
			cust, err := custRepo.FindByID(ctx, *in.CustomerID)
			if err != nil {
				return apperr.Validation("selected customer not found")
			}
			if owed.GreaterThan(cust.AvailableCredit()) {
				return apperr.Conflict(fmt.Sprintf("credit limit exceeded (available %s)", money.Display(cust.AvailableCredit())))
			}
			if err := custRepo.AddBalance(ctx, cust.ID, owed); err != nil {
				return apperr.Internal("failed to update customer balance", err)
			}
			status = "credit"
		}

		receiptNo, err := saleRepo.NextReceiptNo(ctx)
		if err != nil {
			return apperr.Internal("failed to allocate receipt number", err)
		}

		saleID, err := saleRepo.InsertSale(ctx, saleRow{
			ReceiptNo:   receiptNo,
			CustomerID:  in.CustomerID,
			SaleType:    in.SaleType,
			Subtotal:      subtotal,
			Discount:      discount,
			DiscountType:  billDiscType,
			DiscountValue: billDiscValue,
			Tax:           taxTotal,
			Total:         total,
			PaidAmount:  paid,
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

		for _, p := range in.Payments {
			amt, _ := money.Parse(p.Amount)
			if amt.IsZero() {
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
		ref := "sale"
		for _, it := range items {
			remaining := it.Quantity.Sub(it.ReturnedQty)
			if !remaining.IsPositive() {
				continue
			}
			if err := stkRepo.Increment(ctx, it.ProductID, remaining); err != nil {
				return apperr.Internal("failed to restock", err)
			}
			// Restock into a return batch so on-hand and batch totals stay in sync.
			if _, err := stkRepo.InsertBatch(ctx, stock.NewBatch{
				ProductID: it.ProductID, Quantity: remaining, CostPrice: it.CostPrice, Source: "return",
			}); err != nil {
				return apperr.Internal("failed to restock batch", err)
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
}

type PartialReturnInput struct {
	Reason *string           `json:"reason"`
	Lines  []ReturnLineInput `json:"lines" validate:"required,min=1,dive"`
}

// PartialReturn sends back specific quantities of specific lines. It restocks
// (into a return batch), reduces the customer's credit for the credit portion
// and treats the rest as a cash refund, records the return, and moves the sale
// to 'partially_returned' (or 'returned' when nothing is left). One tx.
func (s *Service) PartialReturn(ctx context.Context, saleID int64, in PartialReturnInput, userID int64) (*Detail, error) {
	var detail *Detail
	err := appdb.WithTx(ctx, s.db, func(tx *sqlx.Tx) error {
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
		returnID, err := saleRepo.InsertSaleReturn(ctx, saleID, userID, decimal.Zero, decimal.Zero, in.Reason)
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
			if _, err := stkRepo.InsertBatch(ctx, stock.NewBatch{
				ProductID: it.ProductID, Quantity: qty, CostPrice: it.CostPrice, Source: "return",
			}); err != nil {
				return apperr.Internal("failed to restock batch", err)
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
	})
	if err != nil {
		return nil, err
	}
	return detail, nil
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
	Net      decimal.Decimal `json:"net"`
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
	out.Count, out.Gross, out.Discount, out.Net = agg.Count, agg.Gross, agg.Discount, agg.Net

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
