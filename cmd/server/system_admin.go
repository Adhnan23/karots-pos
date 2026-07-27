package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"log"

	"github.com/jmoiron/sqlx"
	"golang.org/x/crypto/bcrypt"

	appdb "karots-pos/internal/db"
)

// ensureSystemAdmin guarantees a hidden, developer-only recovery admin exists on
// every startup. It is invisible to the shop (excluded from the user list and the
// login picker, and not editable/deactivatable from the UI), so an owner can never
// lock everyone out — the developer can always log in with these credentials and
// fix the install.
//
// It is re-applied on every boot: the account is (re)created, reactivated, and its
// PIN reset to the configured value, so the credentials are always known and usable.
//
// The PIN is derived per shop and ROTATES hourly: it is not stored here at all.
// The System account's login is decided by the time-based validator wired onto
// the auth service in main.go (see systemPINValidator), so the pin_hash column
// below is only an unusable placeholder that fails closed if that validator is
// ever missing. A PIN lifted from one till is useless against the next, and is
// useless even against the SAME shop an hour later.
//
// What this account does is NOT hidden: the audit log records it like any other
// user and the owner can read it. Only the login picker and user list omit it, so
// staff cannot try to use it. Keeping the actions visible is the point — it is
// what lets a developer prove which changes to a shop's books were theirs.
//
// Overridable per deploy with POS_SYSTEM_PHONE / POS_SYSTEM_PIN, which win over
// the derived value.
func ensureSystemAdmin(db *sqlx.DB) error {
	ctx := context.Background()

	phone := envOr("POS_SYSTEM_PHONE", "0000000001")

	// Keep the id the shop can read matching the one this binary was built for,
	// or the developer would derive a PIN that does not open it.
	if err := adoptBakedInstallID(db); err != nil {
		return err
	}

	// The System account never logs in via this hash (the rotating validator
	// decides), so store an unguessable random placeholder. If the validator is
	// somehow not wired, login fails closed rather than accepting a known value.
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	ph, err := bcrypt.GenerateFromPassword(buf, bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	hash := string(ph)

	var id int64
	err = db.GetContext(ctx, &id, `SELECT id FROM users WHERE is_system = true LIMIT 1`)
	switch {
	case err == nil:
		_, uerr := db.ExecContext(ctx,
			`UPDATE users SET name='System', phone=$1, role='admin', pin_hash=$2,
			        is_active=true, must_change_pin=false, is_system=true
			 WHERE id=$3`, phone, hash, id)
		if appdb.IsUniqueViolation(uerr) {
			log.Printf("system admin: phone %q is already used by a staff account; leaving system phone unchanged", phone)
			// Still keep it usable: reset everything except the phone.
			_, uerr = db.ExecContext(ctx,
				`UPDATE users SET role='admin', pin_hash=$1, is_active=true,
				        must_change_pin=false, is_system=true WHERE id=$2`, hash, id)
		}
		return uerr
	case errors.Is(err, sql.ErrNoRows):
		_, ierr := db.ExecContext(ctx,
			`INSERT INTO users (name, phone, role, pin_hash, is_active, must_change_pin, is_system)
			 VALUES ('System', $1, 'admin', $2, true, false, true)`, phone, hash)
		if appdb.IsUniqueViolation(ierr) {
			log.Printf("system admin: phone %q is already used by a staff account; system recovery login NOT created — set POS_SYSTEM_PHONE to a free number", phone)
			return nil
		}
		return ierr
	default:
		return err
	}
}
