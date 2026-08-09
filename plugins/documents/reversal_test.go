package documents

import (
	"context"
	"os"
	"testing"

	appdb "karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
)

func testDB(t *testing.T) *sqlx.DB {
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

func TestMarkJobReversed(t *testing.T) {
	conn := testDB(t)
	defer conn.Close()
	ctx := context.Background()
	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	must := func(e error) { t.Helper(); if e != nil { t.Fatal(e) } }

	var jobID int64
	must(tx.GetContext(ctx, &jobID, `
		INSERT INTO doc_job (description, qty, unit_price, line_total, consumable_cost, kind)
		VALUES ('test copy', 1, 10, 10, 3, 'sale') RETURNING id`))

	st := &Store{}
	must(st.MarkJobReversedTx(ctx, tx, jobID))

	var reversed bool
	must(tx.GetContext(ctx, &reversed, `SELECT reversed_at IS NOT NULL FROM doc_job WHERE id=$1`, jobID))
	if !reversed {
		t.Fatal("job not stamped reversed")
	}

	if err := st.MarkJobReversedTx(ctx, tx, jobID); err == nil {
		t.Fatal("expected second reversal to be rejected")
	}

	// An own_use job is not a 'sale' and must not be reversible this way.
	var ownID int64
	must(tx.GetContext(ctx, &ownID, `
		INSERT INTO doc_job (description, qty, unit_price, line_total, consumable_cost, kind)
		VALUES ('shop use', 1, 0, 0, 3, 'own_use') RETURNING id`))
	if err := st.MarkJobReversedTx(ctx, tx, ownID); err == nil {
		t.Fatal("expected own_use job reversal to be rejected")
	}
}
