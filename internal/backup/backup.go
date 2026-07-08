// Package backup implements a dependency-free logical backup/restore that runs
// entirely over the application's existing SQL connection — no pg_dump, no psql,
// no docker CLI. This matches the deployment model "ship a binary + a connection
// string": the Postgres it points at may live in a Docker container or on a
// remote VPS, and either way the binary can already talk to it.
//
// Only DATA is captured; the schema is owned by the embedded migrations (which
// run on startup), so a restore TRUNCATEs the existing tables and reloads rows.
// Every value is read and written as text (columns are cast with ::text on dump
// and bound as text parameters on restore), so Postgres' own input/output
// functions handle every type — numeric, timestamptz, jsonb, enums — exactly,
// driver-independently.
package backup

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/jmoiron/sqlx"
)

// FormatVersion is bumped if the on-disk shape ever changes incompatibly.
const FormatVersion = 1

// skipTable reports tables never dumped or touched on restore. All goose
// migration bookkeeping — core (goose_db_version) AND per-plugin
// (goose_db_version_<name>) — is owned by the embedded migrations that run at
// startup. Restoring an older snapshot must NOT rewind that record, or the next
// boot would try to re-run already-applied migrations. Matching the whole
// goose_db_version* family keeps plugin bookkeeping safe too.
func skipTable(name string) bool {
	return strings.HasPrefix(name, "goose_db_version")
}

type Table struct {
	Name    string      `json:"name"`
	Columns []string    `json:"columns"`
	Rows    [][]*string `json:"rows"`
}

type Archive struct {
	Version     int     `json:"version"`
	GeneratedAt string  `json:"generated_at"`
	Tables      []Table `json:"tables"`
}

// Dump writes a gzipped JSON snapshot of all public table data to w.
func Dump(ctx context.Context, db *sqlx.DB, generatedAt string, w io.Writer) error {
	names, err := publicTables(ctx, db)
	if err != nil {
		return err
	}
	arc := Archive{Version: FormatVersion, GeneratedAt: generatedAt}
	for _, t := range names {
		cols, err := tableColumns(ctx, db, t)
		if err != nil {
			return err
		}
		rows, err := dumpRows(ctx, db, t, cols)
		if err != nil {
			return fmt.Errorf("dump %s: %w", t, err)
		}
		arc.Tables = append(arc.Tables, Table{Name: t, Columns: cols, Rows: rows})
	}
	gz := gzip.NewWriter(w)
	defer gz.Close()
	enc := json.NewEncoder(gz)
	return enc.Encode(arc)
}

// Restore reads a gzipped JSON snapshot from r and replaces the current data
// with it, all in one transaction. Foreign keys are disabled for the load via
// session_replication_role, so insertion order (incl. self-referential tables)
// never matters.
func Restore(ctx context.Context, db *sqlx.DB, r io.Reader) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("not a valid backup file (gzip): %w", err)
	}
	defer gz.Close()
	var arc Archive
	if err := json.NewDecoder(gz).Decode(&arc); err != nil {
		return fmt.Errorf("not a valid backup file (json): %w", err)
	}
	if arc.Version != FormatVersion {
		return fmt.Errorf("unsupported backup version %d (expected %d)", arc.Version, FormatVersion)
	}

	// Which dumped tables actually exist in the current schema.
	existing, err := publicTables(ctx, db)
	if err != nil {
		return err
	}
	exists := map[string]bool{}
	for _, t := range existing {
		exists[t] = true
	}

	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Disable FK enforcement for the load (scoped to this tx).
	if _, err := tx.ExecContext(ctx, `SET LOCAL session_replication_role = 'replica'`); err != nil {
		return fmt.Errorf("cannot disable foreign keys for restore (the database role needs that privilege): %w", err)
	}

	// Clear the tables we're about to reload.
	var toClear []string
	for _, t := range arc.Tables {
		if exists[t.Name] && !skipTable(t.Name) {
			toClear = append(toClear, quoteIdent(t.Name))
		}
	}
	if len(toClear) > 0 {
		if _, err := tx.ExecContext(ctx, `TRUNCATE `+strings.Join(toClear, ", ")+` RESTART IDENTITY CASCADE`); err != nil {
			return fmt.Errorf("truncate failed: %w", err)
		}
	}

	for _, t := range arc.Tables {
		if !exists[t.Name] || skipTable(t.Name) {
			continue
		}
		if err := insertRows(ctx, tx, t); err != nil {
			return fmt.Errorf("restore %s: %w", t.Name, err)
		}
		if err := resetSequences(ctx, tx, t.Name, t.Columns); err != nil {
			return fmt.Errorf("reset sequences for %s: %w", t.Name, err)
		}
	}

	// Standalone receipt-number sequences (S-/CR-/DP-) are not owned by a column,
	// so resetSequences (which relies on pg_get_serial_sequence) cannot see them.
	// Advance them explicitly or the first post-restore receipt collides on the
	// unique receipt_no constraint.
	if err := resetReceiptSequences(ctx, tx, exists); err != nil {
		return fmt.Errorf("reset receipt sequences: %w", err)
	}

	return tx.Commit()
}

// --- helpers ---

func publicTables(ctx context.Context, db *sqlx.DB) ([]string, error) {
	var all []string
	err := db.SelectContext(ctx, &all,
		`SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename`)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, t := range all {
		if !skipTable(t) {
			out = append(out, t)
		}
	}
	return out, nil
}

func tableColumns(ctx context.Context, db *sqlx.DB, table string) ([]string, error) {
	var cols []string
	err := db.SelectContext(ctx, &cols, `
		SELECT column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1
		ORDER BY ordinal_position`, table)
	return cols, err
}

func dumpRows(ctx context.Context, db *sqlx.DB, table string, cols []string) ([][]*string, error) {
	sel := make([]string, len(cols))
	for i, c := range cols {
		sel[i] = quoteIdent(c) + "::text"
	}
	q := `SELECT ` + strings.Join(sel, ", ") + ` FROM ` + quoteIdent(table)
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out [][]*string
	for rows.Next() {
		raw := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		rec := make([]*string, len(cols))
		for i := range raw {
			if raw[i].Valid {
				v := raw[i].String
				rec[i] = &v
			}
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func insertRows(ctx context.Context, tx *sqlx.Tx, t Table) error {
	if len(t.Rows) == 0 {
		return nil
	}
	colList := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		colList[i] = quoteIdent(c)
	}
	prefix := `INSERT INTO ` + quoteIdent(t.Name) + ` (` + strings.Join(colList, ", ") + `) VALUES `

	// Chunk rows to keep statements (and the parameter count) reasonable.
	const chunk = 200
	for start := 0; start < len(t.Rows); start += chunk {
		end := min(start+chunk, len(t.Rows))
		batch := t.Rows[start:end]

		var b strings.Builder
		b.WriteString(prefix)
		args := make([]any, 0, len(batch)*len(t.Columns))
		p := 1
		for ri, row := range batch {
			if ri > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('(')
			for ci := range t.Columns {
				if ci > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, "$%d", p)
				p++
				if ci < len(row) && row[ci] != nil {
					args = append(args, *row[ci])
				} else {
					args = append(args, nil)
				}
			}
			b.WriteByte(')')
		}
		if _, err := tx.ExecContext(ctx, b.String(), args...); err != nil {
			return err
		}
	}
	return nil
}

// resetSequences advances each serial column's sequence past the largest value
// just loaded, so future inserts don't collide with restored ids.
func resetSequences(ctx context.Context, tx *sqlx.Tx, table string, cols []string) error {
	for _, c := range cols {
		var seq sql.NullString
		if err := tx.GetContext(ctx, &seq, `SELECT pg_get_serial_sequence($1, $2)`, table, c); err != nil {
			return err
		}
		if !seq.Valid || seq.String == "" {
			continue
		}
		_, err := tx.ExecContext(ctx, fmt.Sprintf(
			`SELECT setval('%s', GREATEST((SELECT COALESCE(MAX(%s), 0) FROM %s), 1), (SELECT COUNT(*) FROM %s) > 0)`,
			seq.String, quoteIdent(c), quoteIdent(table), quoteIdent(table)))
		if err != nil {
			return err
		}
	}
	return nil
}

// receiptSequences lists the standalone (non-column-owned) sequences that mint
// human-friendly receipt numbers, paired with the table/column those numbers
// live in. The numeric value is recovered by stripping the prefix (e.g. "CR-").
var receiptSequences = []struct{ Seq, Table, Col string }{
	{"sales_receipt_seq", "sales", "receipt_no"},            // S-00001
	{"money_receipt_seq", "money_receipts", "receipt_no"},   // CR-000001
	{"debt_receipt_seq", "customer_payments", "receipt_no"}, // DP-000001
}

// resetReceiptSequences advances each receipt-number sequence past the largest
// number already present, so future receipts don't collide with restored ones.
// It is tolerant of missing tables/sequences (older or plugin-trimmed schemas).
func resetReceiptSequences(ctx context.Context, tx *sqlx.Tx, exists map[string]bool) error {
	for _, rs := range receiptSequences {
		if !exists[rs.Table] {
			continue
		}
		var hasSeq bool
		if err := tx.GetContext(ctx, &hasSeq,
			`SELECT EXISTS (SELECT 1 FROM pg_class WHERE relkind = 'S' AND relname = $1)`, rs.Seq); err != nil {
			return err
		}
		if !hasSeq {
			continue
		}
		// max numeric suffix across the column (non-digits stripped); 0 when empty.
		_, err := tx.ExecContext(ctx, fmt.Sprintf(
			`SELECT setval('%s',
				GREATEST((SELECT COALESCE(MAX(NULLIF(regexp_replace(%s, '\D', '', 'g'), '')::bigint), 0) FROM %s), 1),
				(SELECT COUNT(*) FROM %s) > 0)`,
			rs.Seq, quoteIdent(rs.Col), quoteIdent(rs.Table), quoteIdent(rs.Table)))
		if err != nil {
			return err
		}
	}
	return nil
}

// quoteIdent safely double-quotes a SQL identifier coming from the catalog.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
