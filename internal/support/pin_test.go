package support

import "testing"

const secret = "test-master-key"

// The support PIN exists so a developer can get into any shop without asking the
// owner for their credentials. What it must NOT be is the same everywhere: one
// PIN compiled into every binary means a credential lifted from any till opens
// every shop that was ever shipped.

func TestDerivePINDiffersPerInstall(t *testing.T) {
	a := DerivePIN(secret, "A1B2C3D4")
	b := DerivePIN(secret, "99887766")
	if a == b {
		t.Fatalf("two installs share the PIN %q — one leak would open every shop", a)
	}
}

func TestDerivePINIsStableForAnInstall(t *testing.T) {
	// The whole workflow is "read me your Install ID" over the phone, so the same
	// id must derive the same PIN every time, on any machine.
	first := DerivePIN(secret, "A1B2C3D4")
	for range 5 {
		if got := DerivePIN(secret, "A1B2C3D4"); got != first {
			t.Fatalf("derivation is not stable: %q then %q", first, got)
		}
	}
	// Case and stray spaces must not change it — an owner reading it aloud and a
	// developer typing it back should not have to match punctuation.
	if got := DerivePIN(secret, "  a1b2c3d4 "); got != first {
		t.Errorf("case/whitespace changed the PIN: %q vs %q", got, first)
	}
}

func TestDerivePINIsSixDigits(t *testing.T) {
	// The login form accepts 4–6 numeric digits; anything else cannot be typed in.
	for _, id := range []string{"A1B2C3D4", "00000000", "ZZZZZZZZ", "12345678"} {
		pin := DerivePIN(secret, id)
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
func TestDerivePINFollowsTheMasterSecret(t *testing.T) {
	before := DerivePIN("first-key", "A1B2C3D4")
	after := DerivePIN("second-key", "A1B2C3D4")
	if before == after {
		t.Error("rotating the master secret did not change the PIN")
	}
}

// Install ids must not collide — two shops sharing one would share a PIN.
func TestNewInstallIDIsUniqueAndReadable(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		id, err := NewInstallID()
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("generated a duplicate install id: %s", id)
		}
		seen[id] = true
		// Short enough to read down a phone, and typeable without ambiguity.
		if len(id) != 8 {
			t.Errorf("install id %q is %d chars, want 8", id, len(id))
		}
		if id != Normalise(id) {
			t.Errorf("install id %q is not in canonical form", id)
		}
	}
}
