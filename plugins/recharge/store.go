package recharge

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jmoiron/sqlx"
)

// Carrier is a recharge carrier (Dialog, Mobitel, …). ProductID is the hidden
// is_service core product that carries this carrier's recharge sales.
type Carrier struct {
	ID        int64  `db:"id"         json:"id"`
	Name      string `db:"name"       json:"name"`
	ProductID int64  `db:"product_id" json:"product_id"`
	IsActive  bool   `db:"is_active"  json:"is_active"`
}

// Store is the recharge plugin's data access over the core database. Cross-table
// references to core (products, categories, units) are by id only; the plugin
// never alters core schema.
type Store struct{ db *sqlx.DB }

// NewStore builds a Store over the core DB handle from the Core API.
func NewStore(db *sqlx.DB) *Store { return &Store{db: db} }

// Carriers lists active carriers, alphabetically.
func (s *Store) Carriers(ctx context.Context) ([]Carrier, error) {
	var cs []Carrier
	err := s.db.SelectContext(ctx, &cs,
		`SELECT id, name, product_id, is_active FROM recharge_carriers WHERE is_active = true ORDER BY name`)
	return cs, err
}

// CreateCarrier inserts a carrier bound to its hidden service product.
func (s *Store) CreateCarrier(ctx context.Context, name string, productID int64) (int64, error) {
	var id int64
	err := s.db.GetContext(ctx, &id,
		`INSERT INTO recharge_carriers (name, product_id) VALUES ($1, $2) RETURNING id`, name, productID)
	return id, err
}

// DeactivateCarrier soft-deletes a carrier (its sales history is preserved).
func (s *Store) DeactivateCarrier(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE recharge_carriers SET is_active = false WHERE id = $1`, id)
	return err
}

// CarrierExists reports whether an active carrier already uses this name.
func (s *Store) CarrierExists(ctx context.Context, name string) (bool, error) {
	var ok bool
	err := s.db.GetContext(ctx, &ok,
		`SELECT EXISTS (SELECT 1 FROM recharge_carriers WHERE lower(name) = lower($1) AND is_active = true)`, name)
	return ok, err
}

// Device is a physical SIM/phone/terminal under a carrier that holds a float
// balance. Carrier is the joined carrier name (for admin listings).
type Device struct {
	ID        int64  `db:"id"         json:"id"`
	CarrierID int64  `db:"carrier_id" json:"carrier_id"`
	Label     string `db:"label"      json:"label"`
	Number    string `db:"number"     json:"number"`
	IsActive  bool   `db:"is_active"  json:"is_active"`
	Carrier   string `db:"carrier"    json:"carrier"`
}

// Devices lists active devices joined with their carrier name, grouped by
// carrier then label.
func (s *Store) Devices(ctx context.Context) ([]Device, error) {
	var ds []Device
	err := s.db.SelectContext(ctx, &ds, `
		SELECT d.id, d.carrier_id, d.label, COALESCE(d.number,'') AS number,
		       d.is_active, c.name AS carrier
		FROM recharge_devices d
		JOIN recharge_carriers c ON c.id = d.carrier_id
		WHERE d.is_active = true AND c.is_active = true
		ORDER BY c.name, d.label`)
	return ds, err
}

// CreateDevice adds a device under a carrier.
func (s *Store) CreateDevice(ctx context.Context, carrierID int64, label, number string) (int64, error) {
	var num *string
	if strings.TrimSpace(number) != "" {
		num = &number
	}
	var id int64
	err := s.db.GetContext(ctx, &id,
		`INSERT INTO recharge_devices (carrier_id, label, number) VALUES ($1,$2,$3) RETURNING id`,
		carrierID, label, num)
	return id, err
}

// DeactivateDevice retires a device (its history is preserved).
func (s *Store) DeactivateDevice(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE recharge_devices SET is_active = false WHERE id = $1`, id)
	return err
}

// serviceDefaults resolves a category id (ensuring a "Recharge" category exists)
// and a unit id (any existing unit) for the hidden service product. It touches
// core reference tables additively only — it never alters their schema.
func (s *Store) serviceDefaults(ctx context.Context) (catID, unitID int64, err error) {
	if err = s.db.GetContext(ctx, &unitID, `SELECT id FROM units ORDER BY id LIMIT 1`); err != nil {
		return 0, 0, err
	}
	err = s.db.GetContext(ctx, &catID, `SELECT id FROM categories WHERE name = 'Recharge' LIMIT 1`)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.GetContext(ctx, &catID, `INSERT INTO categories (name) VALUES ('Recharge') RETURNING id`)
	}
	return catID, unitID, err
}
