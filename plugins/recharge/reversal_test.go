package recharge

import (
	"context"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// seedCarrierProduct inserts a service product to satisfy recharge_carriers'
// NOT NULL product_id, returning its id.
func seedCarrierProduct(t *testing.T, ctx context.Context, tx *sqlx.Tx) int64 {
	t.Helper()
	var catID, unitID, prodID int64
	if err := tx.GetContext(ctx, &catID, `SELECT id FROM categories LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.GetContext(ctx, &unitID, `SELECT id FROM units LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.GetContext(ctx, &prodID, `
		INSERT INTO products (name, category_id, unit_id, cost_price, selling_price, is_service)
		VALUES ('T-REV svc', $1, $2, 0, 0, true) RETURNING id`, catID, unitID); err != nil {
		t.Fatal(err)
	}
	return prodID
}

// Reload reversal store logic. DB-guarded, runs in a rolled-back tx so no real dev
// data is touched. Mirrors earningsTestDB from earnings_test.go.

func TestReverseReload_FailedReturnsFloat(t *testing.T) {
	conn := earningsTestDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck

	must := func(e error) { t.Helper(); if e != nil { t.Fatal(e) } }
	var carrierID, deviceID int64
	must(tx.GetContext(ctx, &carrierID, `INSERT INTO recharge_carriers (name, product_id) VALUES ('T-RevFail', $1) RETURNING id`, seedCarrierProduct(t, ctx, tx)))
	must(tx.GetContext(ctx, &deviceID, `INSERT INTO recharge_devices (carrier_id, label, for_recharge, for_money, tracks_float) VALUES ($1,'D1',true,false,true) RETURNING id`, carrierID))

	// Store methods used here take the tx explicitly, so a nil db is fine.
	st := &Store{}
	reloadID, err := st.RecordTransactionTx(ctx, tx, TxInput{
		SessionID: 990010, CarrierID: carrierID, DeviceID: deviceID, Type: "reload",
		Amount: decimal.NewFromInt(100), CreatedBy: 1,
	})
	must(err)

	revID, err := st.reverseReloadTx(ctx, tx, reloadID, "failed", 1)
	must(err)

	var floatDelta decimal.Decimal
	must(tx.GetContext(ctx, &floatDelta, `SELECT float_delta FROM recharge_transactions WHERE id=$1`, revID))
	if !floatDelta.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("failed reversal float_delta = %s, want 100", floatDelta)
	}
	var reversedAt *string
	must(tx.GetContext(ctx, &reversedAt, `SELECT reversed_at::text FROM recharge_transactions WHERE id=$1`, reloadID))
	if reversedAt == nil {
		t.Fatal("original reload not stamped reversed_at")
	}

	if _, err := st.reverseReloadTx(ctx, tx, reloadID, "failed", 1); err == nil {
		t.Fatal("expected double-reversal to be rejected")
	}
}

func TestReverseReload_WrongNumberKeepsFloatGone(t *testing.T) {
	conn := earningsTestDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck

	must := func(e error) { t.Helper(); if e != nil { t.Fatal(e) } }
	var carrierID, deviceID int64
	must(tx.GetContext(ctx, &carrierID, `INSERT INTO recharge_carriers (name, product_id) VALUES ('T-RevWrong', $1) RETURNING id`, seedCarrierProduct(t, ctx, tx)))
	must(tx.GetContext(ctx, &deviceID, `INSERT INTO recharge_devices (carrier_id, label, for_recharge, for_money, tracks_float) VALUES ($1,'D2',true,false,true) RETURNING id`, carrierID))

	st := &Store{}
	reloadID, err := st.RecordTransactionTx(ctx, tx, TxInput{
		SessionID: 990011, CarrierID: carrierID, DeviceID: deviceID, Type: "reload",
		Amount: decimal.NewFromInt(100), CreatedBy: 1,
	})
	must(err)

	revID, err := st.reverseReloadTx(ctx, tx, reloadID, "wrong_number", 1)
	must(err)

	var floatDelta decimal.Decimal
	must(tx.GetContext(ctx, &floatDelta, `SELECT float_delta FROM recharge_transactions WHERE id=$1`, revID))
	if !floatDelta.IsZero() {
		t.Fatalf("wrong-number reversal float_delta = %s, want 0", floatDelta)
	}
	var revType string
	must(tx.GetContext(ctx, &revType, `SELECT type FROM recharge_transactions WHERE id=$1`, revID))
	if revType != "reversal_lost" {
		t.Fatalf("wrong-number reversal type = %s, want reversal_lost", revType)
	}
}
