package recharge

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
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

// AllCarriers lists every carrier including disabled ones, for the admin screen
// where they can be renamed or switched back on. Active first, then by name.
func (s *Store) AllCarriers(ctx context.Context) ([]Carrier, error) {
	var cs []Carrier
	err := s.db.SelectContext(ctx, &cs,
		`SELECT id, name, product_id, is_active FROM recharge_carriers
		 ORDER BY is_active DESC, name`)
	return cs, err
}

// SetCarrierActive enables or disables a carrier. Carriers are never deleted:
// disabling hides them from the till and every picker while keeping their sales,
// float sessions and ledger intact, and it is reversible. Deleting used to be
// the only option and was a one-way door — the row survived as a tombstone that
// held its name forever, so the same carrier could never be added back.
func (s *Store) SetCarrierActive(ctx context.Context, id int64, active bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE recharge_carriers SET is_active = $2 WHERE id = $1`, id, active)
	return err
}

// RenameCarrier changes a carrier's display name. A carrier that rebrands is
// still the same carrier, so renaming keeps every past sale attached to it.
func (s *Store) RenameCarrier(ctx context.Context, id int64, name string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE recharge_carriers SET name = $2 WHERE id = $1`, id, name)
	return err
}

// Carrier returns one carrier by id, enabled or not.
func (s *Store) Carrier(ctx context.Context, id int64) (*Carrier, error) {
	var c Carrier
	err := s.db.GetContext(ctx, &c,
		`SELECT id, name, product_id, is_active FROM recharge_carriers WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// CarrierNameTaken reports whether another ENABLED carrier already uses this
// name. Pass excludeID when renaming so a carrier does not collide with itself;
// pass 0 when creating.
//
// Scoped to enabled carriers to match the partial unique index added in
// migration 00012. Checking only active names while the DB enforced uniqueness
// across ALL names is precisely what made the old failure so confusing: the app
// said the name was free and the database then refused the insert — after the
// hidden service product had already been created and left orphaned.
func (s *Store) CarrierNameTaken(ctx context.Context, name string, excludeID int64) (bool, error) {
	var ok bool
	err := s.db.GetContext(ctx, &ok, `
		SELECT EXISTS (
			SELECT 1 FROM recharge_carriers
			WHERE lower(name) = lower($1) AND is_active = true AND id <> $2
		)`, name, excludeID)
	return ok, err
}

// Device is a physical SIM/phone/terminal under a carrier that holds a float
// balance. Carrier is the joined carrier name (for admin listings).
type Device struct {
	ID          int64  `db:"id"           json:"id"`
	CarrierID   int64  `db:"carrier_id"   json:"carrier_id"`
	Label       string `db:"label"        json:"label"`
	Number      string `db:"number"       json:"number"`
	IsActive    bool   `db:"is_active"    json:"is_active"`
	Carrier     string `db:"carrier"      json:"carrier"`
	ForRecharge bool   `db:"for_recharge" json:"for_recharge"`
	ForMoney    bool   `db:"for_money"    json:"for_money"`
	TracksFloat bool   `db:"tracks_float" json:"tracks_float"` // false = bank card (no float balance)
}

// Devices lists active devices joined with their carrier name, grouped by
// carrier then label.
func (s *Store) Devices(ctx context.Context) ([]Device, error) {
	var ds []Device
	err := s.db.SelectContext(ctx, &ds, `
		SELECT d.id, d.carrier_id, d.label, COALESCE(d.number,'') AS number,
		       d.is_active, c.name AS carrier, d.for_recharge, d.for_money, d.tracks_float
		FROM recharge_devices d
		JOIN recharge_carriers c ON c.id = d.carrier_id
		WHERE d.is_active = true AND c.is_active = true
		ORDER BY c.name, d.label`)
	return ds, err
}

// DeviceTracksFloat reports whether a device holds a tracked float (false for a
// bank card). Unknown/inactive devices default to true (the safe path that keeps
// the overdraw guard on). Used by the tx handler to skip the float guard and zero
// the float delta for bank-card money movements.
func (s *Store) DeviceTracksFloat(ctx context.Context, deviceID int64) (bool, error) {
	var tracks bool
	err := s.db.GetContext(ctx, &tracks,
		`SELECT tracks_float FROM recharge_devices WHERE id = $1 AND is_active = true`, deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return tracks, err
}

// CreateDevice adds a device under a carrier. forRecharge/forMoney tag which
// pickers it appears in (a device can hold a recharge float, a money float, or
// both); tracksFloat=false marks a bank card with no float balance to track.
func (s *Store) CreateDevice(ctx context.Context, carrierID int64, label, number string, forRecharge, forMoney, tracksFloat bool) (int64, error) {
	var num *string
	if strings.TrimSpace(number) != "" {
		num = &number
	}
	var id int64
	err := s.db.GetContext(ctx, &id,
		`INSERT INTO recharge_devices (carrier_id, label, number, for_recharge, for_money, tracks_float)
		 VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		carrierID, label, num, forRecharge, forMoney, tracksFloat)
	return id, err
}

// DeactivateDevice retires a device (its history is preserved).
func (s *Store) DeactivateDevice(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE recharge_devices SET is_active = false WHERE id = $1`, id)
	return err
}

// AllDevices lists every device including retired ones, for the admin screen.
func (s *Store) AllDevices(ctx context.Context) ([]Device, error) {
	var ds []Device
	err := s.db.SelectContext(ctx, &ds, `
		SELECT d.id, d.carrier_id, d.label, COALESCE(d.number,'') AS number,
		       d.is_active, c.name AS carrier, d.for_recharge, d.for_money, d.tracks_float
		FROM recharge_devices d
		JOIN recharge_carriers c ON c.id = d.carrier_id
		ORDER BY d.is_active DESC, c.name, d.label`)
	return ds, err
}

// Device returns one device by id, active or not.
func (s *Store) Device(ctx context.Context, id int64) (*Device, error) {
	var d Device
	err := s.db.GetContext(ctx, &d, `
		SELECT d.id, d.carrier_id, d.label, COALESCE(d.number,'') AS number,
		       d.is_active, c.name AS carrier, d.for_recharge, d.for_money, d.tracks_float
		FROM recharge_devices d
		JOIN recharge_carriers c ON c.id = d.carrier_id
		WHERE d.id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// UpdateDevice edits a device in place. A SIM whose number changes is still the
// same float account, so editing keeps its balance, sessions and ledger — the
// only alternative before this was retire-and-recreate, which silently split one
// device's history across two rows.
//
// The carrier is deliberately not editable: moving a device to another carrier
// would reassign every past transaction's float to the wrong account.
func (s *Store) UpdateDevice(ctx context.Context, id int64, label, number string, forRecharge, forMoney bool) error {
	var num *string
	if strings.TrimSpace(number) != "" {
		trimmed := strings.TrimSpace(number)
		num = &trimmed
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE recharge_devices
		SET label = $2, number = $3, for_recharge = $4, for_money = $5
		WHERE id = $1`, id, label, num, forRecharge, forMoney)
	return err
}

// RecordOpeningFloat declares money already sitting on a device at onboarding.
//
// This is the float equivalent of opening stock: float_delta = +amount with NO
// cash movement and NO expense, because nothing was bought — the balance simply
// pre-dates the POS. The alternatives were a reload or a supplier refill, both
// of which book an expense and would have invented a purchase that never
// happened, quietly distorting the P&L on day one.
//
// session_id = 0 puts it on the same carry-over path DeviceBalance already uses
// for float moved outside a session, so it is picked up without touching the
// balance query.
func (s *Store) RecordOpeningFloat(ctx context.Context, carrierID, deviceID int64, amount decimal.Decimal, userID int64) error {
	note := "opening float balance at onboarding"
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO recharge_transactions
			(session_id, carrier_id, device_id, type, amount, cash_delta, float_delta, note, created_by)
		VALUES (0, $1, $2, 'opening', $3, 0, $3, $4, $5)`,
		carrierID, deviceID, amount, note, userID)
	return err
}

// SetDeviceActive retires or restores a device.
func (s *Store) SetDeviceActive(ctx context.Context, id int64, active bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE recharge_devices SET is_active = $2 WHERE id = $1`, id, active)
	return err
}

// CarrierOfDevice returns the carrier id owning an active device (0 if none).
// Handlers derive the carrier from the chosen device so the two can never
// disagree — the device is the unit of float.
func (s *Store) CarrierOfDevice(ctx context.Context, deviceID int64) (int64, error) {
	var cid int64
	err := s.db.GetContext(ctx, &cid,
		`SELECT carrier_id FROM recharge_devices WHERE id = $1 AND is_active = true`, deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return cid, err
}

// CarrierName returns a carrier's display name ("" when unknown).
func (s *Store) CarrierName(ctx context.Context, id int64) string {
	var n string
	_ = s.db.GetContext(ctx, &n, `SELECT name FROM recharge_carriers WHERE id = $1`, id)
	return n
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
