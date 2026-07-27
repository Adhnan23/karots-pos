package support

import (
	"testing"
	"time"
)

const secret = "test-master-secret-at-least-32-chars!!"

// A frozen instant so vectors are reproducible. 2026-07-27T09:30:00Z.
var frozen = time.Date(2026, 7, 27, 9, 30, 0, 0, time.UTC)

// The support PIN exists so a developer can get into any shop without asking the
// owner for their credentials. It must NOT be the same everywhere (one leak would
// open every shop), and — the point of this scheme — it must ROTATE, so a PIN
// someone observed cannot be reused later.

func TestDeriveSeedIsStableAndPerInstall(t *testing.T) {
	a1 := DeriveSeed(secret, "A1B2C3D4")
	a2 := DeriveSeed(secret, "  a1b2c3d4 ") // case/space-insensitive via Normalise
	if string(a1) != string(a2) {
		t.Fatal("seed must be stable regardless of case/whitespace")
	}
	if len(a1) != 32 {
		t.Fatalf("seed length = %d, want 32", len(a1))
	}
	if string(DeriveSeed(secret, "99887766")) == string(a1) {
		t.Fatal("different installs must produce different seeds")
	}
	if string(DeriveSeed("other-master-secret-at-least-32ch!!", "A1B2C3D4")) == string(a1) {
		t.Fatal("different master secrets must produce different seeds")
	}
}

func TestCodeIsSixDigitsAndRotatesHourly(t *testing.T) {
	seed := DeriveSeed(secret, "A1B2C3D4")
	now := Code(seed, frozen)
	if len(now) != 6 {
		t.Fatalf("code %q is not 6 digits", now)
	}
	for _, r := range now {
		if r < '0' || r > '9' {
			t.Fatalf("code %q is not all digits", now)
		}
	}
	// Same hour → same code.
	if Code(seed, frozen.Add(20*time.Minute)) != now {
		t.Fatal("code must not change within the same hour")
	}
	// Next hour → (almost certainly) different code.
	if Code(seed, frozen.Add(time.Hour)) == now {
		t.Fatal("code must change across the hour boundary")
	}
}

func TestValidAcceptsCurrentAndAdjacentWindows(t *testing.T) {
	seed := DeriveSeed(secret, "A1B2C3D4")
	// A code generated an hour ago must still validate now (skew 1).
	prev := Code(seed, frozen.Add(-time.Hour))
	if !Valid(seed, prev, frozen, 1) {
		t.Fatal("previous-hour code must be accepted with skew 1")
	}
	next := Code(seed, frozen.Add(time.Hour))
	if !Valid(seed, next, frozen, 1) {
		t.Fatal("next-hour code must be accepted with skew 1")
	}
	if !Valid(seed, Code(seed, frozen), frozen, 1) {
		t.Fatal("current code must be accepted")
	}
	// Two hours away is outside the window.
	if Valid(seed, Code(seed, frozen.Add(-2*time.Hour)), frozen, 1) {
		t.Fatal("two-hours-old code must be rejected with skew 1")
	}
	if Valid(seed, "000000", frozen, 1) && Code(seed, frozen) != "000000" {
		t.Fatal("a wrong code must be rejected")
	}
}

// CodeForSecret is the developer-side convenience: derive the seed then the code.
func TestCodeForSecretMatchesDeriveThenCode(t *testing.T) {
	if CodeForSecret(secret, "A1B2C3D4", frozen) != Code(DeriveSeed(secret, "A1B2C3D4"), frozen) {
		t.Fatal("CodeForSecret must equal Code(DeriveSeed(...))")
	}
}

// Install ids must not collide — two shops sharing one would share a seed.
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
