// Package support derives the per-shop support credential.
//
// It is shared by the server and the bootstrapper so the two can never drift:
// the bootstrapper bakes a shop's SEED at build time and both the running server
// and the developer re-derive the same rotating PIN from it. The seed is stable
// (it depends only on the master secret and the install id); the PIN it produces
// changes every hour, so a PIN someone observed cannot be reused later. If the
// two sides ever disagreed, the developer would be locked out with no clue why.
package support

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// seedDerivation is versioned so a future change to the scheme cannot silently
// shift the PIN for an install already in the field.
const seedDerivation = "karots-pos/support/seed/v1|"

// windowSeconds is the rotation period: the PIN changes every hour.
const windowSeconds = 3600

// DeriveSeed computes a shop's stable per-shop seed from the developer's master
// secret and the shop's install id. One-way (HMAC), so a leaked seed reveals
// neither the master secret nor any other shop.
func DeriveSeed(secret, installID string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(seedDerivation + Normalise(installID)))
	return mac.Sum(nil)
}

// Code is the six-digit support PIN for the hour containing t. The clock supplies
// the changing ingredient, so the same seed yields a different PIN each hour.
//
// Six digits because the login form accepts 4–6 and six is the widest of those;
// the leading zero in something like 036581 is significant, so the value is a
// string everywhere and never an int.
func Code(seed []byte, t time.Time) string {
	return codeForWindow(seed, t.UTC().Unix()/windowSeconds)
}

// CodeForSecret derives the seed then the current code — the developer-side helper.
func CodeForSecret(secret, installID string, t time.Time) string {
	return Code(DeriveSeed(secret, installID), t)
}

// Valid reports whether input matches the code for the window containing now, or
// any window within ±skew (clock-skew and read-latency tolerance). Constant-time.
func Valid(seed []byte, input string, now time.Time, skew int) bool {
	w := now.UTC().Unix() / windowSeconds
	for d := -skew; d <= skew; d++ {
		if hmac.Equal([]byte(input), []byte(codeForWindow(seed, w+int64(d)))) {
			return true
		}
	}
	return false
}

func codeForWindow(seed []byte, window int64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(window))
	mac := hmac.New(sha256.New, seed)
	mac.Write(buf[:])
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
