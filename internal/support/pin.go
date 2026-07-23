// Package support derives the per-shop support credential.
//
// It is shared by the server and the bootstrapper so the two can never drift:
// the bootstrapper computes a shop's PIN at build time and the developer
// re-computes the same PIN months later from the install id the shop reads down
// the phone. If those two calculations ever disagreed, the developer would be
// locked out of a shop with no way to tell why.
package support

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
)

// derivation is versioned so a future change to the scheme cannot silently
// produce a different PIN for an install already in the field.
const derivation = "karots-pos/support/v1|"

// DerivePIN computes a shop's six-digit support PIN from the developer's master
// secret and that shop's install id.
//
// Six digits because the login form accepts 4–6 and six is the widest of those;
// the leading zero in something like 036581 is significant, so the value is a
// string everywhere and never an int.
func DerivePIN(secret, installID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(derivation + Normalise(installID)))
	sum := mac.Sum(nil)
	n := binary.BigEndian.Uint32(sum[:4]) % 1000000
	return fmt.Sprintf("%06d", n)
}

// Normalise puts an install id in canonical form. An owner reading it aloud and
// a developer typing it back should not have to match case or stray spaces.
func Normalise(installID string) string {
	return strings.ToUpper(strings.TrimSpace(installID))
}

// NewInstallID makes an identifier for a fresh install. Not a secret — it is
// useless without the master key — so it only has to be unique, and short enough
// to read over a phone.
func NewInstallID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(b)), nil
}
