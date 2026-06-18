package recharge

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// txKind defines how a transaction type moves the cash drawer and the carrier
// float, given a positive amount. cashSign: +to-drawer/−from-drawer;
// floatSign: +into-float/−out-of-float. See migration 00002.
type txKind struct{ cashSign, floatSign int }

var txKinds = map[string]txKind{
	"deposit":    {cashSign: +1, floatSign: -1}, // cash in, e-money sent to customer wallet
	"billpay":    {cashSign: +1, floatSign: -1}, // cash in, bill paid from float
	"withdrawal": {cashSign: -1, floatSign: +1}, // cash out, e-money received into float
	"topup":      {cashSign: -1, floatSign: +1}, // buy float from supplier (money out)
	"wallet_in":  {cashSign: 0, floatSign: +1},  // customer pays a sale by wallet transfer
}

// Deltas returns the cash and float deltas for a transaction type + amount.
func Deltas(typ string, amount decimal.Decimal) (cashDelta, floatDelta decimal.Decimal) {
	k := txKinds[typ]
	return amount.Mul(decimal.NewFromInt(int64(k.cashSign))),
		amount.Mul(decimal.NewFromInt(int64(k.floatSign)))
}

// TxInput is one money movement to record in the ledger. The matching cash-drawer
// movement (PayIn/Withdraw) and any expense are handled by the caller.
type TxInput struct {
	SessionID int64
	CarrierID int64
	DeviceID  *int64
	Type      string
	Amount    decimal.Decimal
	SaleID    *int64
	ExpenseID *int64
	Reference string
	Note      string
	CreatedBy int64
}

// RecordTransaction inserts a money movement, deriving its cash/float deltas.
func (s *Store) RecordTransaction(ctx context.Context, in TxInput) (int64, error) {
	cashDelta, floatDelta := Deltas(in.Type, in.Amount)
	var id int64
	err := s.db.GetContext(ctx, &id, `
		INSERT INTO recharge_transactions
		  (session_id, carrier_id, device_id, type, amount, cash_delta, float_delta,
		   sale_id, expense_id, reference, note, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`,
		in.SessionID, in.CarrierID, in.DeviceID, in.Type, in.Amount, cashDelta, floatDelta,
		in.SaleID, in.ExpenseID, nullStr(in.Reference), nullStr(in.Note), in.CreatedBy)
	return id, err
}

// SaveOpening upserts a device's opening float (only while the device's session
// row is still open).
func (s *Store) SaveOpening(ctx context.Context, sessionID, deviceID int64, opening decimal.Decimal) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO recharge_device_sessions (session_id, device_id, opening)
		VALUES ($1,$2,$3)
		ON CONFLICT (session_id, device_id)
		DO UPDATE SET opening = EXCLUDED.opening
		WHERE recharge_device_sessions.closed_at IS NULL`,
		sessionID, deviceID, opening)
	return err
}

// SaveClosing records a device's counted closing float and stamps closed_at.
func (s *Store) SaveClosing(ctx context.Context, sessionID, deviceID int64, closing decimal.Decimal) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO recharge_device_sessions (session_id, device_id, opening, closing, closed_at)
		VALUES ($1,$2,0,$3, now())
		ON CONFLICT (session_id, device_id)
		DO UPDATE SET closing = EXCLUDED.closing, closed_at = now()`,
		sessionID, deviceID, closing)
	return err
}

// DeviceRecon is one device's reconciliation line within a carrier.
type DeviceRecon struct {
	DeviceID int64            `json:"device_id"`
	Label    string           `json:"label"`
	Number   string           `json:"number"`
	Opening  decimal.Decimal  `json:"opening"`
	Closing  *decimal.Decimal `json:"closing"`
	Started  bool             `json:"started"` // opening float entered for this session
}

// CarrierRecon aggregates a carrier's devices and float movements for a session.
type CarrierRecon struct {
	CarrierID  int64           `json:"carrier_id"`
	Carrier    string          `json:"carrier"`
	Devices    []DeviceRecon   `json:"devices"`
	Opening    decimal.Decimal `json:"opening"`    // Σ device openings
	FloatIn    decimal.Decimal `json:"float_in"`   // withdrawals + topups + wallet-ins
	FloatOut   decimal.Decimal `json:"float_out"`  // deposits + bill payments
	Reload     decimal.Decimal `json:"reload"`     // airtime sold via the till
	Expected   decimal.Decimal `json:"expected"`   // opening + in − out − reload
	Closing    decimal.Decimal `json:"closing"`    // Σ device closings
	BonusLoss  decimal.Decimal `json:"bonus_loss"` // closing − expected
	AnyStarted bool            `json:"any_started"`
	AllClosed  bool            `json:"all_closed"`
}

type deviceSessionRow struct {
	DeviceID int64            `db:"device_id"`
	Opening  decimal.Decimal  `db:"opening"`
	Closing  *decimal.Decimal `db:"closing"`
	Closed   bool             `db:"closed"`
}

type carrierFloatRow struct {
	CarrierID int64           `db:"carrier_id"`
	FloatIn   decimal.Decimal `db:"float_in"`
	FloatOut  decimal.Decimal `db:"float_out"`
}

// reloadTotal sums airtime sold for a carrier's hidden service product by the
// session's cashier since the drawer opened (excluding returned sales).
func (s *Store) reloadTotal(ctx context.Context, productID, cashierID int64, since time.Time) (decimal.Decimal, error) {
	var v decimal.Decimal
	err := s.db.GetContext(ctx, &v, `
		SELECT COALESCE(SUM(si.subtotal),0)
		FROM sale_items si JOIN sales s ON s.id = si.sale_id
		WHERE si.product_id = $1 AND s.cashier_id = $2
		  AND s.created_at >= $3 AND s.status <> 'returned'`,
		productID, cashierID, since)
	return v, err
}

// Reconciliation builds the per-carrier reconciliation (each with its devices)
// for a cash-drawer session. since is the drawer's opened_at.
func (s *Store) Reconciliation(ctx context.Context, sessionID, cashierID int64, since time.Time) ([]CarrierRecon, error) {
	carriers, err := s.Carriers(ctx)
	if err != nil {
		return nil, err
	}
	devices, err := s.Devices(ctx)
	if err != nil {
		return nil, err
	}

	var dsRows []deviceSessionRow
	if err := s.db.SelectContext(ctx, &dsRows,
		`SELECT device_id, opening, closing, (closed_at IS NOT NULL) AS closed
		 FROM recharge_device_sessions WHERE session_id = $1`, sessionID); err != nil {
		return nil, err
	}
	ds := make(map[int64]deviceSessionRow, len(dsRows))
	for _, r := range dsRows {
		ds[r.DeviceID] = r
	}

	var floatRows []carrierFloatRow
	if err := s.db.SelectContext(ctx, &floatRows, `
		SELECT carrier_id,
		       COALESCE(SUM(CASE WHEN float_delta > 0 THEN float_delta ELSE 0 END),0)  AS float_in,
		       COALESCE(SUM(CASE WHEN float_delta < 0 THEN -float_delta ELSE 0 END),0) AS float_out
		FROM recharge_transactions WHERE session_id = $1 GROUP BY carrier_id`, sessionID); err != nil {
		return nil, err
	}
	floats := make(map[int64]carrierFloatRow, len(floatRows))
	for _, r := range floatRows {
		floats[r.CarrierID] = r
	}

	byCarrier := map[int64][]Device{}
	for _, d := range devices {
		byCarrier[d.CarrierID] = append(byCarrier[d.CarrierID], d)
	}

	out := make([]CarrierRecon, 0, len(carriers))
	for _, c := range carriers {
		cr := CarrierRecon{CarrierID: c.ID, Carrier: c.Name, AllClosed: true}
		for _, d := range byCarrier[c.ID] {
			dr := DeviceRecon{DeviceID: d.ID, Label: d.Label, Number: d.Number}
			if row, ok := ds[d.ID]; ok {
				dr.Started = true
				dr.Opening = row.Opening
				dr.Closing = row.Closing
				cr.AnyStarted = true
				cr.Opening = cr.Opening.Add(row.Opening)
				if row.Closed && row.Closing != nil {
					cr.Closing = cr.Closing.Add(*row.Closing)
				} else {
					cr.AllClosed = false
				}
			} else {
				cr.AllClosed = false
			}
			cr.Devices = append(cr.Devices, dr)
		}
		if f, ok := floats[c.ID]; ok {
			cr.FloatIn, cr.FloatOut = f.FloatIn, f.FloatOut
		}
		reload, err := s.reloadTotal(ctx, c.ProductID, cashierID, since)
		if err != nil {
			return nil, err
		}
		cr.Reload = reload
		cr.Expected = cr.Opening.Add(cr.FloatIn).Sub(cr.FloatOut).Sub(cr.Reload)
		cr.BonusLoss = cr.Closing.Sub(cr.Expected)
		if !cr.AnyStarted {
			cr.AllClosed = false
		}
		out = append(out, cr)
	}
	return out, nil
}

// TxRow is one ledger entry for the admin report.
type TxRow struct {
	CreatedAt  time.Time       `db:"created_at"`
	Carrier    string          `db:"carrier"`
	Type       string          `db:"type"`
	Amount     decimal.Decimal `db:"amount"`
	CashDelta  decimal.Decimal `db:"cash_delta"`
	FloatDelta decimal.Decimal `db:"float_delta"`
	Reference  *string         `db:"reference"`
}

// RecentTransactions lists the latest ledger entries for the admin report.
func (s *Store) RecentTransactions(ctx context.Context, limit int) ([]TxRow, error) {
	var rows []TxRow
	err := s.db.SelectContext(ctx, &rows, `
		SELECT t.created_at, c.name AS carrier, t.type, t.amount, t.cash_delta,
		       t.float_delta, t.reference
		FROM recharge_transactions t
		JOIN recharge_carriers c ON c.id = t.carrier_id
		ORDER BY t.created_at DESC LIMIT $1`, limit)
	return rows, err
}

// openingValue is the prefill for a device's opening input ("" when not yet set).
func openingValue(d DeviceRecon) string {
	if !d.Started {
		return ""
	}
	return d.Opening.StringFixed(2)
}

// closingValue is the prefill for a device's closing input ("" when not counted).
func closingValue(d DeviceRecon) string {
	if d.Closing == nil {
		return ""
	}
	return d.Closing.StringFixed(2)
}

// hasActivity reports whether a carrier saw any recharge movement in a session.
func hasActivity(cr CarrierRecon) bool {
	return cr.AnyStarted || cr.Reload.IsPositive() || cr.FloatIn.IsPositive() ||
		cr.FloatOut.IsPositive() || cr.Closing.IsPositive()
}

// anyActivity reports whether any carrier in the set saw activity.
func anyActivity(rows []CarrierRecon) bool {
	for _, cr := range rows {
		if hasActivity(cr) {
			return true
		}
	}
	return false
}

// refText renders an optional reference for the report.
func refText(ref *string) string {
	if ref == nil || *ref == "" {
		return "—"
	}
	return *ref
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
