package recharge

import (
	"context"
	"strconv"
	"time"

	"karots-pos/internal/money"

	"github.com/shopspring/decimal"
)

// txKind defines how a transaction type moves the cash drawer and the device
// float, given a positive amount. cashSign: +to-drawer/−from-drawer; floatSign:
// +into-float/−out-of-float. See migrations 00002 & 00004.
type txKind struct{ cashSign, floatSign int }

var txKinds = map[string]txKind{
	"deposit":    {cashSign: +1, floatSign: -1}, // cash in, e-money sent to customer wallet
	"billpay":    {cashSign: +1, floatSign: -1}, // cash in, bill paid from float
	"withdrawal": {cashSign: -1, floatSign: +1}, // cash out, e-money received into float
	"topup":      {cashSign: -1, floatSign: +1}, // buy float from supplier (money out)
	"wallet_in":  {cashSign: 0, floatSign: +1},  // customer pays a sale by wallet transfer
	"reload":     {cashSign: 0, floatSign: -1},  // airtime sold (cash handled by the sale)
	"refill":     {cashSign: 0, floatSign: +1},  // admin buys float from supplier (no drawer; expense booked)
}

// decreasesFloat reports whether a positive-amount transaction of this type
// lowers the device float (and so is subject to the overdraw hard-block).
func decreasesFloat(typ string) bool { return txKinds[typ].floatSign < 0 }

// Deltas returns the cash and float deltas for a transaction type + amount.
func Deltas(typ string, amount decimal.Decimal) (cashDelta, floatDelta decimal.Decimal) {
	k := txKinds[typ]
	return amount.Mul(decimal.NewFromInt(int64(k.cashSign))),
		amount.Mul(decimal.NewFromInt(int64(k.floatSign)))
}

// TxInput is one money movement to record in the ledger. The matching cash-drawer
// movement (PayIn/Withdraw) and any expense are handled by the caller. DeviceID
// is mandatory — the float lives on a specific device.
type TxInput struct {
	SessionID     int64
	CarrierID     int64
	DeviceID      int64
	Type          string
	Amount        decimal.Decimal
	SaleID        *int64
	ExpenseID     *int64
	Reference     string
	Note          string
	CreatedBy     int64
	Untracked     bool            // bank card: record cash movement but no float delta
	ServiceCharge decimal.Decimal // shop fee collected in cash on top of the principal
}

// RecordTransaction inserts a money movement, deriving its cash/float deltas.
// For an untracked device (bank card) the float delta is forced to zero — the
// cash side still posts, but there is no float to move.
func (s *Store) RecordTransaction(ctx context.Context, in TxInput) (int64, error) {
	cashDelta, floatDelta := Deltas(in.Type, in.Amount)
	if in.Untracked {
		floatDelta = decimal.Zero
	}
	var id int64
	err := s.db.GetContext(ctx, &id, `
		INSERT INTO recharge_transactions
		  (session_id, carrier_id, device_id, type, amount, cash_delta, float_delta,
		   sale_id, expense_id, reference, note, created_by, service_charge)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id`,
		in.SessionID, in.CarrierID, in.DeviceID, in.Type, in.Amount, cashDelta, floatDelta,
		in.SaleID, in.ExpenseID, nullStr(in.Reference), nullStr(in.Note), in.CreatedBy, in.ServiceCharge)
	return id, err
}

// DeviceBalance returns a device's live float in a session:
//
//	openingBase + Σ float_delta(session, device)
//
// where openingBase is this session's saved opening, else the device's last
// counted closing (carry-over), else 0.
func (s *Store) DeviceBalance(ctx context.Context, sessionID, deviceID int64) (decimal.Decimal, error) {
	var v decimal.Decimal
	err := s.db.GetContext(ctx, &v, `
		SELECT (CASE WHEN os.opening IS NOT NULL THEN os.opening
		             ELSE COALESCE(lc.closing,0) + COALESCE(carry.v,0) END)
		       + COALESCE(tx.net, 0) AS balance
		FROM recharge_devices d
		LEFT JOIN recharge_device_sessions os ON os.session_id=$1 AND os.device_id=d.id
		LEFT JOIN LATERAL (
		  SELECT closing, closed_at FROM recharge_device_sessions
		  WHERE device_id=d.id AND closed_at IS NOT NULL ORDER BY closed_at DESC LIMIT 1) lc ON true
		LEFT JOIN LATERAL (
		  SELECT SUM(float_delta) AS v FROM recharge_transactions
		  WHERE device_id=d.id AND session_id=0 AND (lc.closed_at IS NULL OR created_at > lc.closed_at)) carry ON true
		LEFT JOIN LATERAL (
		  SELECT SUM(float_delta) AS net FROM recharge_transactions
		  WHERE session_id=$1 AND device_id=d.id) tx ON true
		WHERE d.id=$2`,
		sessionID, deviceID)
	return v, err
}

// wouldOverdraw reports whether decreasing this device's float by amt would push
// it below zero (used for the hard-block on deposit/bill-pay; reload is blocked
// at the till before the sale).
func (s *Store) wouldOverdraw(ctx context.Context, sessionID, deviceID int64, amt decimal.Decimal) (bool, error) {
	bal, err := s.DeviceBalance(ctx, sessionID, deviceID)
	if err != nil {
		return false, err
	}
	return amt.GreaterThan(bal), nil
}

// DeviceBalanceRow is a device + its live balance, for the dynamic pickers.
// TracksFloat=false (a bank card) means Balance is meaningless: the picker shows
// it without a balance and never blocks on overdraw.
type DeviceBalanceRow struct {
	ID          int64           `db:"id"           json:"id"`
	CarrierID   int64           `db:"carrier_id"   json:"carrier_id"`
	Carrier     string          `db:"carrier"      json:"carrier"`
	Label       string          `db:"label"        json:"label"`
	Number      string          `db:"number"       json:"number"`
	Balance     decimal.Decimal `db:"balance"      json:"balance"`
	TracksFloat bool            `db:"tracks_float" json:"tracks_float"`
}

// DevicesWithBalance lists active devices with their live balance in the given
// session. carrierID 0 returns every carrier's devices (the flat wallet picker
// and the checkout overdraw map); a non-zero id narrows to one carrier (the
// reload popup and tx form). purpose "recharge"/"money" narrows to devices
// tagged for that use (""=all). Powers all the dynamic device pickers.
func (s *Store) DevicesWithBalance(ctx context.Context, sessionID, carrierID int64, purpose string) ([]DeviceBalanceRow, error) {
	var rows []DeviceBalanceRow
	err := s.db.SelectContext(ctx, &rows, `
		SELECT d.id, d.carrier_id, c.name AS carrier, d.label, COALESCE(d.number,'') AS number,
		       (CASE WHEN os.opening IS NOT NULL THEN os.opening
		             ELSE COALESCE(lc.closing,0) + COALESCE(carry.v,0) END)
		       + COALESCE(tx.net, 0) AS balance, d.tracks_float
		FROM recharge_devices d
		JOIN recharge_carriers c ON c.id = d.carrier_id
		LEFT JOIN recharge_device_sessions os ON os.session_id=$1 AND os.device_id=d.id
		LEFT JOIN LATERAL (
		  SELECT closing, closed_at FROM recharge_device_sessions
		  WHERE device_id=d.id AND closed_at IS NOT NULL ORDER BY closed_at DESC LIMIT 1) lc ON true
		LEFT JOIN LATERAL (
		  SELECT SUM(float_delta) AS v FROM recharge_transactions
		  WHERE device_id=d.id AND session_id=0 AND (lc.closed_at IS NULL OR created_at > lc.closed_at)) carry ON true
		LEFT JOIN LATERAL (
		  SELECT SUM(float_delta) AS net FROM recharge_transactions
		  WHERE session_id=$1 AND device_id=d.id) tx ON true
		WHERE d.is_active=true AND c.is_active=true AND ($2=0 OR d.carrier_id=$2)
		  AND (CASE WHEN $3='recharge' THEN (d.for_recharge AND d.tracks_float)
		            WHEN $3='money' THEN d.for_money
		            ELSE true END)
		ORDER BY c.name, d.label`, sessionID, carrierID, purpose)
	return rows, err
}

// DeviceBalanceNow is one active device's current float balance for the admin
// "where's my money" panel: last counted closing carried forward plus every
// float movement since that close.
type DeviceBalanceNow struct {
	ID        int64           `db:"id"         json:"id"`
	CarrierID int64           `db:"carrier_id" json:"carrier_id"`
	Carrier   string          `db:"carrier"    json:"carrier"`
	Label     string          `db:"label"      json:"label"`
	Number    string          `db:"number"     json:"number"`
	Balance   decimal.Decimal `db:"balance"    json:"balance"`
	LastAt    *time.Time      `db:"last_at"    json:"last_at"`
}

// DeviceBalances returns every active device's current float balance, session-
// agnostic: COALESCE(last closing, 0) + Σ float_delta of movements after that
// close (all movements when never closed). Newest carrier/device order.
func (s *Store) DeviceBalances(ctx context.Context) ([]DeviceBalanceNow, error) {
	var rows []DeviceBalanceNow
	err := s.db.SelectContext(ctx, &rows, `
		SELECT d.id, d.carrier_id, c.name AS carrier, d.label, COALESCE(d.number,'') AS number,
		       COALESCE(lc.closing,0) + COALESCE(tx.net,0) AS balance, lm.last_at
		FROM recharge_devices d
		JOIN recharge_carriers c ON c.id = d.carrier_id
		LEFT JOIN LATERAL (
		  SELECT closing, closed_at FROM recharge_device_sessions
		  WHERE device_id=d.id AND closed_at IS NOT NULL ORDER BY closed_at DESC LIMIT 1) lc ON true
		LEFT JOIN LATERAL (
		  SELECT SUM(float_delta) AS net FROM recharge_transactions
		  WHERE device_id=d.id AND (lc.closed_at IS NULL OR created_at > lc.closed_at)) tx ON true
		LEFT JOIN LATERAL (
		  SELECT MAX(created_at) AS last_at FROM recharge_transactions WHERE device_id=d.id) lm ON true
		WHERE d.is_active=true AND c.is_active=true AND d.tracks_float
		ORDER BY c.name, d.label`)
	return rows, err
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
	DeviceID  int64            `json:"device_id"`
	Label     string           `json:"label"`
	Number    string           `json:"number"`
	Opening   decimal.Decimal  `json:"opening"`   // openingBase (saved this session, else carried)
	FloatIn   decimal.Decimal  `json:"float_in"`  // withdrawals + topups + wallet-ins
	FloatOut  decimal.Decimal  `json:"float_out"` // deposits + bill payments + reloads
	Expected  decimal.Decimal  `json:"expected"`  // opening + in − out  (== live balance)
	Closing   *decimal.Decimal `json:"closing"`
	BonusLoss *decimal.Decimal `json:"bonus_loss"` // closing − expected (when closed)
	Started   bool             `json:"started"`    // opening saved for this session
	Closed    bool             `json:"closed"`
}

// CarrierRecon aggregates a carrier's devices and float movements for a session.
type CarrierRecon struct {
	CarrierID  int64           `json:"carrier_id"`
	Carrier    string          `json:"carrier"`
	Devices    []DeviceRecon   `json:"devices"`
	Opening    decimal.Decimal `json:"opening"`
	FloatIn    decimal.Decimal `json:"float_in"`
	FloatOut   decimal.Decimal `json:"float_out"`
	Expected   decimal.Decimal `json:"expected"`
	Closing    decimal.Decimal `json:"closing"`    // Σ closings of closed devices
	BonusLoss  decimal.Decimal `json:"bonus_loss"` // Σ bonus of closed devices
	AnyStarted bool            `json:"any_started"`
	AllClosed  bool            `json:"all_closed"`
}

type reconRow struct {
	DeviceID    int64            `db:"device_id"`
	CarrierID   int64            `db:"carrier_id"`
	Carrier     string           `db:"carrier"`
	Label       string           `db:"label"`
	Number      string           `db:"number"`
	Started     bool             `db:"started"`
	OpeningBase decimal.Decimal  `db:"opening_base"`
	Closing     *decimal.Decimal `db:"closing"`
	Closed      bool             `db:"closed"`
	FloatIn     decimal.Decimal  `db:"float_in"`
	FloatOut    decimal.Decimal  `db:"float_out"`
}

// Reconciliation builds the per-carrier reconciliation (each with its devices)
// for a cash-drawer session, entirely from the device ledger + device sessions.
func (s *Store) Reconciliation(ctx context.Context, sessionID int64) ([]CarrierRecon, error) {
	var rows []reconRow
	err := s.db.SelectContext(ctx, &rows, `
		SELECT d.id AS device_id, d.carrier_id, c.name AS carrier,
		       d.label, COALESCE(d.number,'') AS number,
		       (os.device_id IS NOT NULL) AS started,
		       (CASE WHEN os.opening IS NOT NULL THEN os.opening
		             ELSE COALESCE(lc.closing,0) + COALESCE(carry.v,0) END) AS opening_base,
		       os.closing, (os.closed_at IS NOT NULL) AS closed,
		       COALESCE(tin.v, 0)  AS float_in,
		       COALESCE(tout.v, 0) AS float_out
		FROM recharge_devices d
		JOIN recharge_carriers c ON c.id = d.carrier_id
		LEFT JOIN recharge_device_sessions os ON os.session_id=$1 AND os.device_id=d.id
		LEFT JOIN LATERAL (
		  SELECT closing, closed_at FROM recharge_device_sessions
		  WHERE device_id=d.id AND closed_at IS NOT NULL ORDER BY closed_at DESC LIMIT 1) lc ON true
		LEFT JOIN LATERAL (
		  SELECT SUM(float_delta) AS v FROM recharge_transactions
		  WHERE device_id=d.id AND session_id=0 AND (lc.closed_at IS NULL OR created_at > lc.closed_at)) carry ON true
		LEFT JOIN LATERAL (
		  SELECT SUM(float_delta) AS v FROM recharge_transactions
		  WHERE session_id=$1 AND device_id=d.id AND float_delta>0) tin ON true
		LEFT JOIN LATERAL (
		  SELECT SUM(-float_delta) AS v FROM recharge_transactions
		  WHERE session_id=$1 AND device_id=d.id AND float_delta<0) tout ON true
		WHERE d.is_active=true AND c.is_active=true AND d.tracks_float
		ORDER BY c.name, d.label`, sessionID)
	if err != nil {
		return nil, err
	}

	var out []CarrierRecon
	idx := map[int64]int{}
	for _, r := range rows {
		i, ok := idx[r.CarrierID]
		if !ok {
			out = append(out, CarrierRecon{CarrierID: r.CarrierID, Carrier: r.Carrier, AllClosed: true})
			i = len(out) - 1
			idx[r.CarrierID] = i
		}
		cr := &out[i]
		expected := r.OpeningBase.Add(r.FloatIn).Sub(r.FloatOut)
		dr := DeviceRecon{
			DeviceID: r.DeviceID, Label: r.Label, Number: r.Number,
			Opening: r.OpeningBase, FloatIn: r.FloatIn, FloatOut: r.FloatOut,
			Expected: expected, Closing: r.Closing, Started: r.Started, Closed: r.Closed,
		}
		if r.Closed && r.Closing != nil {
			b := r.Closing.Sub(expected)
			dr.BonusLoss = &b
			cr.Closing = cr.Closing.Add(*r.Closing)
			cr.BonusLoss = cr.BonusLoss.Add(b)
		} else {
			cr.AllClosed = false
		}
		if r.Started {
			cr.AnyStarted = true
		}
		cr.Opening = cr.Opening.Add(r.OpeningBase)
		cr.FloatIn = cr.FloatIn.Add(r.FloatIn)
		cr.FloatOut = cr.FloatOut.Add(r.FloatOut)
		cr.Expected = cr.Expected.Add(expected)
		cr.Devices = append(cr.Devices, dr)
	}
	for i := range out {
		if len(out[i].Devices) == 0 {
			out[i].AllClosed = false
		}
	}
	return out, nil
}

// openingValue is the prefill for a device's opening input — always the carried
// base, so the cashier sees the running balance and only adjusts on drift.
func openingValue(d DeviceRecon) string { return d.Opening.StringFixed(2) }

// closingValue is the prefill for a device's closing input ("" when not counted).
func closingValue(d DeviceRecon) string {
	if d.Closing == nil {
		return ""
	}
	return d.Closing.StringFixed(2)
}

// hasActivity reports whether a carrier saw any recharge movement in a session.
func hasActivity(cr CarrierRecon) bool {
	return cr.AnyStarted || cr.FloatIn.IsPositive() || cr.FloatOut.IsPositive() || cr.Closing.IsPositive()
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

// bonusText renders a device's bonus/loss for the report ("—" when not closed).
func bonusText(symbol string, v *decimal.Decimal) string {
	if v == nil {
		return "—"
	}
	return money.Format(symbol, *v)
}

// usedFor renders a device's purpose tags for the admin device table.
func usedFor(d Device) string {
	if !d.TracksFloat {
		return "Bank card (no float)"
	}
	switch {
	case d.ForRecharge && d.ForMoney:
		return "Recharge + Money"
	case d.ForRecharge:
		return "Recharge"
	case d.ForMoney:
		return "Money"
	}
	return "—"
}

// lastAt renders an optional "last movement" timestamp.
func lastAt(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.Format("02 Jan 15:04")
}

// serviceText renders a service charge for the ledger ("—" when none).
func serviceText(symbol string, v decimal.Decimal) string {
	if v.IsZero() {
		return "—"
	}
	return money.Format(symbol, v)
}

// sumServiceCharge totals the service charge across ledger rows (filtered total).
func sumServiceCharge(rows []TxRow) decimal.Decimal {
	t := decimal.Zero
	for _, r := range rows {
		t = t.Add(r.ServiceCharge)
	}
	return t
}

// refText renders an optional reference for the report.
func refText(ref *string) string {
	if ref == nil || *ref == "" {
		return "—"
	}
	return *ref
}

// TxRow is one ledger entry for the admin report/ledger.
type TxRow struct {
	ID            int64           `db:"id"`
	CreatedAt     time.Time       `db:"created_at"`
	Carrier       string          `db:"carrier"`
	Device        string          `db:"device"`
	Type          string          `db:"type"`
	Amount        decimal.Decimal `db:"amount"`
	ServiceCharge decimal.Decimal `db:"service_charge"`
	CashDelta     decimal.Decimal `db:"cash_delta"`
	FloatDelta    decimal.Decimal `db:"float_delta"`
	Reference     *string         `db:"reference"`
}

// LedgerFilter narrows the admin ledger query (zero values = no filter).
type LedgerFilter struct {
	From      *time.Time
	To        *time.Time
	CarrierID int64
	DeviceID  int64
	Type      string
	Limit     int
}

// Ledger lists money movements matching a filter, newest first.
func (s *Store) Ledger(ctx context.Context, f LedgerFilter) ([]TxRow, error) {
	q := `
		SELECT t.id, t.created_at, c.name AS carrier, COALESCE(d.label,'—') AS device, t.type,
		       t.amount, t.service_charge, t.cash_delta, t.float_delta, t.reference
		FROM recharge_transactions t
		JOIN recharge_carriers c ON c.id = t.carrier_id
		LEFT JOIN recharge_devices d ON d.id = t.device_id
		WHERE 1=1`
	var args []any
	add := func(cond string, v any) { args = append(args, v); q += " AND " + cond + " $" + strconv.Itoa(len(args)) }
	if f.From != nil {
		add("t.created_at >=", *f.From)
	}
	if f.To != nil {
		add("t.created_at <", *f.To)
	}
	if f.CarrierID != 0 {
		add("t.carrier_id =", f.CarrierID)
	}
	if f.DeviceID != 0 {
		add("t.device_id =", f.DeviceID)
	}
	if f.Type != "" {
		add("t.type =", f.Type)
	}
	q += " ORDER BY t.created_at DESC"
	if f.Limit > 0 {
		args = append(args, f.Limit)
		q += " LIMIT $" + strconv.Itoa(len(args))
	}
	var rows []TxRow
	err := s.db.SelectContext(ctx, &rows, q, args...)
	return rows, err
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
