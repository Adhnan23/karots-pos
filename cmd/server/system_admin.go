package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"os"

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
// The PIN is derived per shop from the developer's master secret and the install
// id (see supportPIN), so every shop has a different one. A PIN lifted from one
// till is useless against the next — which was not true when a single credential
// was compiled into every binary that ever shipped.
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
	pin := os.Getenv("POS_SYSTEM_PIN")
	if pin == "" {
		id, err := installID(db)
		switch {
		case err != nil || id == "":
			pin = "2273"
			log.Println("system admin: no install id yet — using the fallback PIN")
		case supportSecret == "":
			pin = "2273"
			log.Println("SECURITY: this binary has no support secret compiled in, so the " +
				"support PIN is the same fixed value as every other build. Build with " +
				"-ldflags \"-X main.supportSecret=...\" to give each shop its own.")
		default:
			pin = supportPIN(id)
			log.Printf("system admin: support PIN derived for install %s", id)
		}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	var id int64
	err = db.GetContext(ctx, &id, `SELECT id FROM users WHERE is_system = true LIMIT 1`)
	switch {
	case err == nil:
		_, uerr := db.ExecContext(ctx,
			`UPDATE users SET name='System', phone=$1, role='admin', pin_hash=$2,
			        is_active=true, must_change_pin=false, is_system=true
			 WHERE id=$3`, phone, string(hash), id)
		if appdb.IsUniqueViolation(uerr) {
			log.Printf("system admin: phone %q is already used by a staff account; leaving system phone unchanged", phone)
			// Still keep it usable: reset everything except the phone.
			_, uerr = db.ExecContext(ctx,
				`UPDATE users SET role='admin', pin_hash=$1, is_active=true,
				        must_change_pin=false, is_system=true WHERE id=$2`, string(hash), id)
		}
		return uerr
	case errors.Is(err, sql.ErrNoRows):
		_, ierr := db.ExecContext(ctx,
			`INSERT INTO users (name, phone, role, pin_hash, is_active, must_change_pin, is_system)
			 VALUES ('System', $1, 'admin', $2, true, false, true)`, phone, string(hash))
		if appdb.IsUniqueViolation(ierr) {
			log.Printf("system admin: phone %q is already used by a staff account; system recovery login NOT created — set POS_SYSTEM_PHONE to a free number", phone)
			return nil
		}
		return ierr
	default:
		return err
	}
}
