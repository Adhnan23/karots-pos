package web

import (
	"context"
	"strconv"
	"strings"

	"karots-pos/internal/apperr"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/purchases"
	"karots-pos/internal/features/supplierpay"
	"karots-pos/internal/money"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

// payRequest is one supplier payment, already parsed and validated by the
// caller. Source is only read for cash — card and online payments record the
// payment without touching a drawer.
type payRequest struct {
	SupplierID   int64
	SupplierName string
	In           supplierpay.PayInput
	Source       cashflow.Location
}

// paySupplierTx records a supplier payment and moves the cash, inside the
// caller's transaction.
//
// It exists so the admin screen and the till run the same code. They differ
// only in which cash sources they offer and which URL the print prompt points
// at — never in what gets written.
//
// Returns the payment result and, for cash, the money receipt. A nil receipt
// means a non-cash method, not a failure.
func (s *Server) paySupplierTx(ctx context.Context, tx *sqlx.Tx, req payRequest, userID int64) (*supplierpay.Result, *cashflow.Receipt, error) {
	res, err := s.supplierPay.PayTx(ctx, tx, req.SupplierID, req.In, userID)
	if err != nil {
		return nil, nil, err
	}
	if res.Method != "cash" {
		return res, nil, nil
	}
	rec, err := s.cashflow.MoveTx(ctx, tx, cashflow.MoveInput{
		From:        req.Source,
		To:          cashflow.External(),
		Amount:      res.Total,
		Reason:      "supplier payment: " + req.SupplierName,
		ReceiptKind: "supplier_payment",
		Party:       req.SupplierName,
		Ref:         &cashflow.Ref{Kind: "supplier_payment", ID: res.PaymentID},
		ActorID:     userID,
	})
	if err != nil {
		return nil, nil, err
	}
	return res, rec, nil
}

// parseAllocations reads the per-invoice allocation inputs a pay form rendered
// (alloc_<id>), falling back to a plain unallocated amount for a supplier who
// carries a balance with no open invoices. Shared so the admin and counter
// forms can never drift in how they split a payment.
func parseAllocations(c echo.Context, invoices []purchases.Purchase) (supplierpay.PayInput, error) {
	in := supplierpay.PayInput{
		Method:    c.FormValue("method"),
		Reference: strings.TrimSpace(c.FormValue("reference")),
		Note:      strings.TrimSpace(c.FormValue("note")),
	}
	for _, pu := range invoices {
		raw := strings.TrimSpace(c.FormValue("alloc_" + strconv.FormatInt(pu.ID, 10)))
		if raw == "" {
			continue
		}
		amt, err := money.Parse(raw)
		if err != nil || amt.IsNegative() {
			return in, apperr.Validation("invalid allocation amount")
		}
		if amt.IsZero() {
			continue
		}
		in.Allocations = append(in.Allocations, supplierpay.Alloc{PurchaseID: pu.ID, Amount: amt})
	}
	if len(invoices) == 0 {
		if raw := strings.TrimSpace(c.FormValue("amount")); raw != "" {
			amt, err := money.Parse(raw)
			if err != nil || amt.IsNegative() {
				return in, apperr.Validation("invalid amount")
			}
			in.Unallocated = amt
		}
	}
	return in, nil
}

// payNow is the optional "paying the supplier now" part of a receive form.
type payNow struct {
	amount decimal.Decimal
	method string
	source cashflow.Location
}

// PayFields is the "paying now" block of a receive form.
//
// It lives here rather than on purchases.ReceiveInput deliberately. Echo's Bind
// consumes a JSON body, so these cannot be read with c.FormValue afterwards —
// they have to be part of the bound struct. Embedding the purchase input in a
// web-layer wrapper gets that without putting payment fields back into the
// purchases package, where a stored paid amount that moved no money was the
// original defect.
type PayFields struct {
	PayAmount string `json:"pay_amount" form:"pay_amount"`
	PayMethod string `json:"pay_method" form:"pay_method"`
	PaySource string `json:"pay_source" form:"pay_source"`
}

// receiveRequest is an admin/counter receive: the goods, plus optional payment.
type receiveRequest struct {
	purchases.ReceiveInput
	PayFields
}

// createRequest is a walk-in delivery: the goods, plus optional payment.
type createRequest struct {
	purchases.CreateInput
	PayFields
}

// parsePayNow validates the payment block. A blank or zero amount means the
// goods are taken in on account and nothing is paid.
func parsePayNow(f PayFields) (payNow, error) {
	raw := strings.TrimSpace(f.PayAmount)
	if raw == "" || raw == "0" {
		return payNow{amount: decimal.Zero}, nil
	}
	amt, err := money.Parse(raw)
	if err != nil || amt.IsNegative() {
		return payNow{}, apperr.Validation("payment amount must be a non-negative number")
	}
	if !amt.IsPositive() {
		return payNow{amount: decimal.Zero}, nil
	}
	method, ok := normSupplierMethod(f.PayMethod)
	if !ok {
		return payNow{}, apperr.Validation("invalid payment method")
	}
	out := payNow{amount: amt, method: method}
	if method == "cash" {
		src, perr := parseLocation(f.PaySource)
		if perr != nil {
			return payNow{}, perr
		}
		out.source = src
	}
	return out, nil
}
