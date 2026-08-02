package recharge

import (
	"context"
	"os"
	"testing"
	"time"

	appdb "karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// RangeEarnings is the shop's real recharge earning: service charge + realized
// (closed-session) float commission. Reload face value is NOT counted. Runs in a
// rolled-back tx on far-future dates so no real dev data is in range.

func earningsTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	conn, err := appdb.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestRangeEarnings(t *testing.T) {
	conn := earningsTestDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck

	// session_id is a plain BIGINT (no FK), so a literal is fine.
	const sessID = 990001
	var carrierID, deviceID int64
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		_, e := tx.ExecContext(ctx, q, args...)
		must(e)
	}
	var catID, unitID, prodID int64
	must(tx.GetContext(ctx, &catID, `SELECT id FROM categories LIMIT 1`))
	must(tx.GetContext(ctx, &unitID, `SELECT id FROM units LIMIT 1`))
	must(tx.GetContext(ctx, &prodID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price, is_service)
		VALUES ('TESTCARR svc', $1, $2, 0, 0, true) RETURNING id`, catID, unitID))
	must(tx.GetContext(ctx, &carrierID, `INSERT INTO recharge_carriers (name, product_id) VALUES ('TESTCARR', $1) RETURNING id`, prodID))
	must(tx.GetContext(ctx, &deviceID, `INSERT INTO recharge_devices (carrier_id, label) VALUES ($1,'TESTDEV') RETURNING id`, carrierID))

	inRange := "2099-01-01T10:00:00Z"

	// A reload of 100 (float −100) carrying a service charge of 5, in range.
	exec(`INSERT INTO recharge_transactions (session_id, carrier_id, device_id, type, amount, cash_delta, float_delta, service_charge, created_at)
		VALUES ($1,$2,$3,'reload',100,0,-100,5,$4)`, sessID, carrierID, deviceID, inRange)
	// A bill payment service charge of 2, in range.
	exec(`INSERT INTO recharge_bill_tx (bank_locker_id, bank_name, type, amount, service_charge, created_at)
		VALUES (1, 'TESTBANK', 'billpay', 500, 2, $1)`, inRange)
	// Device session closed in range: opening 1000, net float delta −100 (the
	// reload) → expected 900; counted closing 903 → 3 commission bonus.
	exec(`INSERT INTO recharge_device_sessions (session_id, device_id, opening, closing, closed_at)
		VALUES ($1,$2,1000,903,$3)`, sessID, deviceID, inRange)

	from := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2099, 1, 2, 0, 0, 0, 0, time.UTC)
	got, err := rangeEarnings(ctx, tx, from, to)
	must(err)
	// 5 (svc) + 2 (bill svc) + 3 (commission) = 10. NOT 100 (face value).
	if !got.Equal(decimal.NewFromInt(10)) {
		t.Errorf("RangeEarnings = %s, want 10 (5 svc + 2 bill + 3 commission)", got)
	}

	// A session closed OUTSIDE the range contributes nothing.
	outFrom := time.Date(2098, 1, 1, 0, 0, 0, 0, time.UTC)
	outTo := time.Date(2098, 1, 2, 0, 0, 0, 0, time.UTC)
	got2, err := rangeEarnings(ctx, tx, outFrom, outTo)
	must(err)
	if !got2.IsZero() {
		t.Errorf("out-of-range earnings = %s, want 0", got2)
	}
}
