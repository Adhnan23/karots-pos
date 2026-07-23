package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
)

// supportSecret is the developer's master key for deriving per-shop support
// PINs. It is deliberately NOT read from the shop's .env — the owner must not be
// able to read it — and is baked in at build time:
//
//	go build -ldflags "-X main.supportSecret=<your-secret>" ./cmd/server
//
// Left empty it falls back to a fixed PIN and the server says so loudly on every
// boot, because that fallback is the old behaviour: one credential shared by
// every install, where a single leak opens every shop that was ever shipped.
var supportSecret = ""

// supportPIN derives this install's support PIN from the master secret and the
// shop's install id.
//
// The point is that the credential differs per shop while the developer still
// has only ONE thing to protect: ask the owner to read their Install ID off the
// login screen, derive the PIN, sign in. Nothing has to be stored per shop, and
// a PIN recovered from one shop's till reveals nothing about the next.
func supportPIN(installID string) string {
	mac := hmac.New(sha256.New, []byte(supportSecret))
	mac.Write([]byte("karots-pos/support/v1|" + strings.ToUpper(strings.TrimSpace(installID))))
	sum := mac.Sum(nil)
	// Six digits: the login form accepts 4–6, and six keeps the guess space at a
	// million rather than ten thousand.
	n := binary.BigEndian.Uint32(sum[:4]) % 1000000
	return fmt.Sprintf("%06d", n)
}

// installID reads this shop's identifier, which migration 0055 generated once.
func installID(db *sqlx.DB) (string, error) {
	var id string
	err := db.Get(&id, `SELECT COALESCE(install_id,'') FROM settings ORDER BY id LIMIT 1`)
	return id, err
}

// printSupportPIN is the `-support-pin <install-id>` helper: it answers "the shop
// is on the phone reading me their Install ID, what is their PIN?" without
// needing a database or a per-shop password list.
func printSupportPIN(installID string) {
	if supportSecret == "" {
		fmt.Println("no support secret compiled into this binary — the support PIN is the fixed fallback")
		return
	}
	fmt.Printf("install %s → support PIN %s\n", strings.ToUpper(installID), supportPIN(installID))
}
