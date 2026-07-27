package main

import (
	"crypto/hmac"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"karots-pos/internal/support"

	"github.com/jmoiron/sqlx"
)

// A shipped binary carries ONLY its own shop's SEED — HMAC(masterSecret, installID)
// — never the master secret. The seed is one-way, so cracking one shop's binary
// yields nothing about the master or any other shop. The support PIN is derived
// from the seed and the current hour, so it rotates and an observed PIN expires.
//
// (The master key used to be baked in with -ldflags, which put it in the binary
// twice — as a plain string and in Go's build metadata, where `go version -m`
// prints the whole ldflags line in readable form. Baking only the per-shop seed
// keeps that leak from ever mattering: one binary reveals only its own shop.)
var (
	installIDBaked = ""
	supportSeedHex = ""
)

// resolveSupportSeed finds this boot's per-shop seed and describes its source.
// A shipped binary has it baked; the developer's own machine derives it from the
// master secret in .env; a bare build has neither (validator falls back to 2273).
func resolveSupportSeed(db *sqlx.DB) (seed []byte, source string, err error) {
	if supportSeedHex != "" {
		b, derr := hex.DecodeString(supportSeedHex)
		return b, "baked in at build for install " + installIDBaked, derr
	}
	if secret := os.Getenv("POS_SUPPORT_SECRET"); secret != "" {
		id, ierr := installID(db)
		if ierr == nil && id != "" {
			return support.DeriveSeed(secret, id), "derived from POS_SUPPORT_SECRET for install " + id, nil
		}
	}
	return nil, "", nil
}

// systemPINValidator returns the login check for the System account.
// Resolution order: POS_SYSTEM_PIN override → hourly rotating code (±1 hour) →
// fixed 2273 fallback when the build has no seed at all.
func systemPINValidator(seed []byte) func(pin string, now time.Time) bool {
	override := os.Getenv("POS_SYSTEM_PIN")
	return func(pin string, now time.Time) bool {
		if override != "" {
			return hmac.Equal([]byte(pin), []byte(override))
		}
		if seed != nil {
			return support.Valid(seed, pin, now, 1)
		}
		return hmac.Equal([]byte(pin), []byte("2273"))
	}
}

// installID reads this shop's identifier (migration 0055 generates one).
func installID(db *sqlx.DB) (string, error) {
	var id string
	err := db.Get(&id, `SELECT COALESCE(install_id,'') FROM settings ORDER BY id LIMIT 1`)
	return id, err
}

// adoptBakedInstallID makes the database agree with the id the binary was built
// for, so the id the shop reads out is the one -support-pin expects. Without this
// a rebuilt binary and its database could disagree, and the derived PIN would
// simply not work with no clue as to why.
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
// what is their PIN right now?". The PIN rotates hourly, so it also prints how
// long this one is valid. The master secret comes from the environment at the
// moment it is needed — the developer's .env — and is never compiled into
// anything. Run this on a shop's own binary and it has nothing to work with,
// which is the point.
func printSupportPIN(id string) {
	secret := os.Getenv("POS_SUPPORT_SECRET")
	if secret == "" {
		fmt.Println("POS_SUPPORT_SECRET is not set — run this on your own machine, where .env has it")
		fmt.Println("  make support-pin ID=" + support.Normalise(id))
		return
	}
	now := time.Now()
	mins := 60 - (now.UTC().Unix()%3600)/60
	fmt.Printf("install %s → support PIN %s  (rotates hourly; valid ~%d more min, previous/next hour also accepted)\n",
		support.Normalise(id), support.CodeForSecret(secret, id, now), mins)
}
