package main

import (
	"fmt"
	"os"

	"karots-pos/internal/support"

	"github.com/jmoiron/sqlx"
	"golang.org/x/crypto/bcrypt"
)

// A shipped binary carries ONLY its own shop's credential — never the master
// secret it was derived from.
//
// The master key used to be baked in with -ldflags, which put it in the binary
// twice: as a plain string, and again in Go's build metadata, where
// `go version -m karots-pos` prints the whole ldflags line in labelled, readable
// form. Anyone holding one shop's binary could read the key and derive every
// other shop's PIN, which defeated the entire point of per-shop PINs.
//
// So the bootstrapper does the derivation at build time and bakes in the install
// id plus a bcrypt HASH of that shop's PIN. Cracking open a shipped binary now
// yields a hash for the one shop whose machine you are already standing at, and
// nothing whatsoever about any other shop. The master key never leaves the
// developer's machine.
var (
	installIDBaked = ""
	supportHash    = ""
)

// supportCredential resolves the support account's PIN hash for this boot, and
// describes where it came from for the log.
//
// Order matters: an explicit override is the documented way back in if a master
// secret is ever lost, so it has to beat everything else.
func supportCredential(db *sqlx.DB) (hash, source string, err error) {
	// 1. Deliberate per-deploy override.
	if pin := os.Getenv("POS_SYSTEM_PIN"); pin != "" {
		h, herr := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
		return string(h), "POS_SYSTEM_PIN override", herr
	}
	// 2. A shipped binary: the hash was computed at build time.
	if supportHash != "" {
		return supportHash, "baked in at build for install " + installIDBaked, nil
	}
	// 3. Running from source with the master secret to hand (the developer's own
	//    machine, where .env carries it).
	if secret := os.Getenv("POS_SUPPORT_SECRET"); secret != "" {
		id, ierr := installID(db)
		if ierr == nil && id != "" {
			h, herr := bcrypt.GenerateFromPassword([]byte(support.DerivePIN(secret, id)), bcrypt.DefaultCost)
			return string(h), "derived from POS_SUPPORT_SECRET for install " + id, herr
		}
	}
	// 4. Nothing to go on. Same fixed PIN as every other bare build, so say so.
	h, herr := bcrypt.GenerateFromPassword([]byte("2273"), bcrypt.DefaultCost)
	return string(h), "", herr
}

// installID reads this shop's identifier (migration 0055 generates one).
func installID(db *sqlx.DB) (string, error) {
	var id string
	err := db.Get(&id, `SELECT COALESCE(install_id,'') FROM settings ORDER BY id LIMIT 1`)
	return id, err
}

// adoptBakedInstallID makes the database agree with the id the binary was built
// for, so the id the shop reads out is the one the developer's -support-pin
// expects. Without this a rebuilt binary and its database could disagree, and
// the derived PIN would simply not work with no clue as to why.
func adoptBakedInstallID(db *sqlx.DB) error {
	if installIDBaked == "" {
		return nil
	}
	_, err := db.Exec(
		`UPDATE settings SET install_id = $1 WHERE COALESCE(install_id,'') <> $1`,
		support.Normalise(installIDBaked))
	return err
}

// printSupportPIN answers "the shop is on the phone reading me their install id,
// what is their PIN?".
//
// The master secret comes from the environment at the moment it is needed — the
// developer's .env — and is never compiled into anything. Run this on a shop's
// own binary and it has nothing to work with, which is the point.
func printSupportPIN(id string) {
	secret := os.Getenv("POS_SUPPORT_SECRET")
	if secret == "" {
		fmt.Println("POS_SUPPORT_SECRET is not set — run this on your own machine, where .env has it")
		fmt.Println("  make support-pin ID=" + support.Normalise(id))
		return
	}
	fmt.Printf("install %s → support PIN %s\n", support.Normalise(id), support.DerivePIN(secret, id))
}
