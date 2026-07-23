package main

import "testing"

// The support PIN exists so a developer can get into any shop without asking the
// owner for their credentials. What it must NOT be is the same everywhere: one
// PIN compiled into every binary means a credential lifted from any till opens
// every shop that was ever shipped.

func TestSupportPINDiffersPerInstall(t *testing.T) {
	supportSecret = "test-master-key"
	defer func() { supportSecret = "" }()

	a := supportPIN("A1B2C3D4")
	b := supportPIN("99887766")
	if a == b {
		t.Fatalf("two installs share the PIN %q — one leak would open every shop", a)
	}
}

func TestSupportPINIsStableForAnInstall(t *testing.T) {
	supportSecret = "test-master-key"
	defer func() { supportSecret = "" }()

	// The whole workflow is "read me your Install ID" over the phone, so the same
	// id must derive the same PIN every time, on any machine.
	first := supportPIN("A1B2C3D4")
	for range 5 {
		if got := supportPIN("A1B2C3D4"); got != first {
			t.Fatalf("derivation is not stable: %q then %q", first, got)
		}
	}
	// Case and stray spaces must not change it — an owner reading it aloud and a
	// developer typing it back should not have to match punctuation.
	if got := supportPIN("  a1b2c3d4 "); got != first {
		t.Errorf("case/whitespace changed the PIN: %q vs %q", got, first)
	}
}

func TestSupportPINIsSixDigits(t *testing.T) {
	supportSecret = "test-master-key"
	defer func() { supportSecret = "" }()

	// The login form accepts 4–6 numeric digits; anything else cannot be typed in.
	for _, id := range []string{"A1B2C3D4", "00000000", "ZZZZZZZZ", "12345678"} {
		pin := supportPIN(id)
		if len(pin) != 6 {
			t.Errorf("install %s gave PIN %q, want 6 digits", id, pin)
		}
		for _, r := range pin {
			if r < '0' || r > '9' {
				t.Errorf("install %s gave non-numeric PIN %q", id, pin)
				break
			}
		}
	}
}

// A different master secret must produce a different PIN for the same shop, so
// rotating the secret in a new build actually rotates the credential.
func TestSupportPINFollowsTheMasterSecret(t *testing.T) {
	defer func() { supportSecret = "" }()

	supportSecret = "first-key"
	before := supportPIN("A1B2C3D4")
	supportSecret = "second-key"
	after := supportPIN("A1B2C3D4")
	if before == after {
		t.Error("rotating the master secret did not change the PIN")
	}
}
