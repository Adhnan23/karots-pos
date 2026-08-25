package productplus

import (
	"context"
	"encoding/json"
	"strings"

	appdb "karots-pos/internal/db"

	"github.com/lib/pq"
)

// Field is one admin-defined custom product field.
type Field struct {
	ID           int64    `db:"id"`
	Key          string   `db:"key"`
	Label        string   `db:"label"`
	Type         string   `db:"type"` // text | number | bool | select
	DefaultValue string   `db:"default_value"`
	Required     bool     `db:"required"`
	Searchable   bool     `db:"searchable"`
	// OptionsRaw is the options JSONB as stored; exported so sqlx can scan it.
	// Use Options (decoded) everywhere else.
	OptionsRaw []byte   `db:"options"`
	Options    []string `db:"-"`
	SortOrder  int      `db:"sort_order"`
	IsActive   bool     `db:"is_active"`
}

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
		SELECT id, key, label, type, default_value, required, searchable,
		       options, sort_order, is_active
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
		SELECT id, key, label, type, default_value, required, searchable,
		       options, sort_order, is_active
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
		INSERT INTO pp_fields (key, label, type, default_value, required, searchable, options, sort_order, is_active)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,true) RETURNING id`,
		f.Key, f.Label, f.Type, f.DefaultValue, f.Required, f.Searchable, nullJSON(opts), f.SortOrder)
	return id, err
}

func (s *Store) UpdateField(ctx context.Context, f Field) error {
	var opts []byte
	if len(f.Options) > 0 {
		opts, _ = json.Marshal(f.Options)
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE pp_fields SET label=$2, type=$3, default_value=$4, required=$5,
		       searchable=$6, options=$7, sort_order=$8 WHERE id=$1`,
		f.ID, f.Label, f.Type, f.DefaultValue, f.Required, f.Searchable, nullJSON(opts), f.SortOrder)
	return err
}

func (s *Store) SetFieldActive(ctx context.Context, id int64, active bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE pp_fields SET is_active=$2 WHERE id=$1`, id, active)
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
			label = r.Label + ": " + r.Value
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
