package productplus

import (
	"context"
	"encoding/json"
	"strings"

	appdb "karots-pos/internal/db"
	"karots-pos/internal/plugin"

	"github.com/lib/pq"
)

// Field is one admin-defined custom product field.
type Field struct {
	ID           int64    `db:"id"`
	Key          string   `db:"key"`
	Label        string   `db:"label"`
	Type         string   `db:"type"` // text | textarea | number | bool | select | date
	DefaultValue string   `db:"default_value"`
	Hint         string   `db:"hint"`
	Required     bool     `db:"required"`
	Searchable   bool     `db:"searchable"`
	ShowAtTill     bool   `db:"show_at_till"`
	PrintOnLabel   bool   `db:"print_on_label"`
	PrintOnReceipt bool   `db:"print_on_receipt"`
	// OptionsRaw is the options JSONB as stored; exported so sqlx can scan it.
	// Use Options (decoded) everywhere else.
	OptionsRaw []byte   `db:"options"`
	Options    []string `db:"-"`
	SortOrder  int      `db:"sort_order"`
	IsActive   bool     `db:"is_active"`
}

// fieldColumns is the shared SELECT list, kept in one place so a new column lands
// in every read at once.
const fieldColumns = `id, key, label, type, default_value, hint, required, searchable,
	show_at_till, print_on_label, print_on_receipt, options, sort_order, is_active`

type Store struct{ db appdb.Queryer }

func NewStore(db appdb.Queryer) *Store { return &Store{db: db} }

func (f *Field) decodeOptions() {
	f.Options = nil
	if len(f.OptionsRaw) > 0 {
		_ = json.Unmarshal(f.OptionsRaw, &f.Options)
	}
}

func (s *Store) Fields(ctx context.Context, includeDisabled bool) ([]Field, error) {
	var rows []Field
	err := s.db.SelectContext(ctx, &rows, `
		SELECT `+fieldColumns+`
		FROM pp_fields
		WHERE ($1 OR is_active)
		ORDER BY sort_order, id`, includeDisabled)
	for i := range rows {
		rows[i].decodeOptions()
	}
	return rows, err
}

func (s *Store) ActiveFields(ctx context.Context) ([]Field, error) { return s.Fields(ctx, false) }

func (s *Store) Field(ctx context.Context, id int64) (*Field, error) {
	var f Field
	if err := s.db.GetContext(ctx, &f, `
		SELECT `+fieldColumns+`
		FROM pp_fields WHERE id=$1`, id); err != nil {
		return nil, err
	}
	f.decodeOptions()
	return &f, nil
}

func (s *Store) CreateField(ctx context.Context, f Field) (int64, error) {
	var opts []byte
	if len(f.Options) > 0 {
		opts, _ = json.Marshal(f.Options)
	}
	var id int64
	err := s.db.GetContext(ctx, &id, `
		INSERT INTO pp_fields (key, label, type, default_value, hint, required, searchable,
			show_at_till, print_on_label, print_on_receipt, options, sort_order, is_active)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,true) RETURNING id`,
		f.Key, f.Label, f.Type, f.DefaultValue, f.Hint, f.Required, f.Searchable,
		f.ShowAtTill, f.PrintOnLabel, f.PrintOnReceipt, nullJSON(opts), f.SortOrder)
	return id, err
}

func (s *Store) UpdateField(ctx context.Context, f Field) error {
	var opts []byte
	if len(f.Options) > 0 {
		opts, _ = json.Marshal(f.Options)
	}
	// sort_order is deliberately NOT updated here — it's managed by MoveField
	// (the ▲▼ buttons), so editing a field never disturbs its position.
	_, err := s.db.ExecContext(ctx, `
		UPDATE pp_fields SET label=$2, type=$3, default_value=$4, hint=$5, required=$6,
		       searchable=$7, show_at_till=$8, print_on_label=$9, print_on_receipt=$10,
		       options=$11 WHERE id=$1`,
		f.ID, f.Label, f.Type, f.DefaultValue, f.Hint, f.Required, f.Searchable,
		f.ShowAtTill, f.PrintOnLabel, f.PrintOnReceipt, nullJSON(opts))
	return err
}

func (s *Store) SetFieldActive(ctx context.Context, id int64, active bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE pp_fields SET is_active=$2 WHERE id=$1`, id, active)
	return err
}

// FieldCount returns how many fields exist (active or not) — used to append a new
// field at the end of the order.
func (s *Store) FieldCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.GetContext(ctx, &n, `SELECT count(*) FROM pp_fields`)
	return n, err
}

// MoveField shifts a field one step up or down in display order. It renumbers
// every field to its position in the swapped sequence, so ties (many fields left
// at the default sort_order 0, ordered by id) resolve deterministically and a
// swap always has a visible effect. No-op at the edges.
//
// ponytail: rewrites every row per move — fine for a short admin field list.
func (s *Store) MoveField(ctx context.Context, id int64, up bool) error {
	fields, err := s.Fields(ctx, true) // full list, in (sort_order, id) order
	if err != nil {
		return err
	}
	idx := -1
	for i := range fields {
		if fields[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	j := idx + 1
	if up {
		j = idx - 1
	}
	if j < 0 || j >= len(fields) {
		return nil // already at the edge
	}
	fields[idx], fields[j] = fields[j], fields[idx]
	for i := range fields {
		if err := s.SetSortOrder(ctx, fields[i].ID, i); err != nil {
			return err
		}
	}
	return nil
}

// SetSortOrder writes one field's position.
func (s *Store) SetSortOrder(ctx context.Context, id int64, n int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE pp_fields SET sort_order=$2 WHERE id=$1`, id, n)
	return err
}

// KeyExists reports whether a field key is already taken (for slug uniqueness).
func (s *Store) KeyExists(ctx context.Context, key string) (bool, error) {
	var n int
	err := s.db.GetContext(ctx, &n, `SELECT count(*) FROM pp_fields WHERE key=$1`, key)
	return n > 0, err
}

// Values returns a product's set custom values keyed by field id (absent fields
// resolve to their default at render time, not here).
func (s *Store) Values(ctx context.Context, productID int64) (map[int64]string, error) {
	type row struct {
		FieldID int64  `db:"field_id"`
		Value   string `db:"value"`
	}
	var rows []row
	if err := s.db.SelectContext(ctx, &rows,
		`SELECT field_id, value FROM pp_values WHERE product_id=$1`, productID); err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(rows))
	for _, r := range rows {
		out[r.FieldID] = r.Value
	}
	return out, nil
}

func (s *Store) SetValue(ctx context.Context, fieldID, productID int64, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO pp_values (field_id, product_id, value) VALUES ($1,$2,$3)
		ON CONFLICT (field_id, product_id) DO UPDATE SET value = EXCLUDED.value`,
		fieldID, productID, value)
	return err
}

func (s *Store) DeleteValue(ctx context.Context, fieldID, productID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pp_values WHERE field_id=$1 AND product_id=$2`, fieldID, productID)
	return err
}

// MatchProductIDs returns product ids whose value for a SEARCHABLE field contains
// the query (case-insensitive substring). Distinct across fields.
//
// Text/number/select match on the stored value directly (a select value IS its
// option label, e.g. "Grade B", so it reads naturally). A bool value is stored as
// "1"/absence, which no human word matches — so a Yes bool is instead found by
// typing the field's LABEL (search "waterproof" → every product where Waterproof
// is Yes). Only the Yes side is findable, which is exactly what you'd want.
//
// ponytail: substring match only — no numeric range / exact-code fast path. Add a
// typed column + index if searchable numbers ever need range queries.
func (s *Store) MatchProductIDs(ctx context.Context, query string) ([]int64, error) {
	var ids []int64
	err := s.db.SelectContext(ctx, &ids, `
		SELECT DISTINCT v.product_id
		FROM pp_values v JOIN pp_fields f ON f.id = v.field_id
		WHERE f.is_active AND f.searchable AND (
			(f.type <> 'bool' AND v.value ILIKE '%' || $1 || '%')
			OR (f.type = 'bool' AND v.value = '1' AND f.label ILIKE '%' || $1 || '%')
		)`, query)
	return ids, err
}

// MetaFor returns a short, read-only display string per product ("Model No: XJ-9 ·
// Grade B · Waterproof"), for the admin product list. One query for the whole page.
// Only STORED values appear (a value equal to the default has no row), so the line
// stays sparse; bool shows the label only when Yes.
func (s *Store) MetaFor(ctx context.Context, productIDs []int64) (map[int64]string, error) {
	if len(productIDs) == 0 {
		return nil, nil
	}
	type row struct {
		ProductID int64  `db:"product_id"`
		Label     string `db:"label"`
		Type      string `db:"type"`
		Value     string `db:"value"`
	}
	var rows []row
	if err := s.db.SelectContext(ctx, &rows, `
		SELECT v.product_id, f.label, f.type, v.value
		FROM pp_values v JOIN pp_fields f ON f.id = v.field_id
		WHERE f.is_active AND v.product_id = ANY($1)
		ORDER BY v.product_id, f.sort_order, f.id`, pq.Array(productIDs)); err != nil {
		return nil, err
	}
	parts := map[int64][]string{}
	for _, r := range rows {
		var label string
		switch r.Type {
		case "bool":
			if r.Value != "1" { // No / unset: nothing to show
				continue
			}
			label = r.Label
		default:
			if strings.TrimSpace(r.Value) == "" {
				continue
			}
			label = r.Label + ": " + truncate(r.Value, 40)
		}
		parts[r.ProductID] = append(parts[r.ProductID], label)
	}
	out := make(map[int64]string, len(parts))
	for id, ps := range parts {
		out[id] = strings.Join(ps, " · ")
	}
	return out, nil
}

func nullJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// truncate shortens a value for a compact summary line (long textarea text would
// otherwise blow up the admin list / info popup). Rune-safe, adds an ellipsis.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// TillRows returns the custom-field rows to show in the till info popup for a
// product: only fields flagged show_at_till, in sort order, skipping blanks and
// No booleans (a bool shows its label only when Yes). Value equal to the default
// still shows — the till reader wants the fact, not just overrides.
func (s *Store) TillRows(ctx context.Context, productID int64) ([]plugin.DetailRow, error) {
	type row struct {
		Label   string `db:"label"`
		Type    string `db:"type"`
		Default string `db:"default_value"`
		Value   *string `db:"value"`
	}
	var rows []row
	if err := s.db.SelectContext(ctx, &rows, `
		SELECT f.label, f.type, f.default_value, v.value
		FROM pp_fields f
		LEFT JOIN pp_values v ON v.field_id = f.id AND v.product_id = $1
		WHERE f.is_active AND f.show_at_till
		ORDER BY f.sort_order, f.id`, productID); err != nil {
		return nil, err
	}
	out := make([]plugin.DetailRow, 0, len(rows))
	for _, r := range rows {
		val := r.Default
		if r.Value != nil {
			val = *r.Value
		}
		if r.Type == "bool" {
			if val != "1" {
				continue
			}
			out = append(out, plugin.DetailRow{Label: r.Label, Value: "Yes"})
			continue
		}
		if strings.TrimSpace(val) == "" {
			continue
		}
		out = append(out, plugin.DetailRow{Label: r.Label, Value: truncate(val, 120)})
	}
	return out, nil
}
