package main

import (
	"testing"
	"time"

	"karots-pos/internal/support"
)

func TestSystemPINValidatorResolutionOrder(t *testing.T) {
	seed := support.DeriveSeed("master-secret-at-least-32-characters!", "A1B2C3D4")
	now := time.Date(2026, 7, 27, 9, 30, 0, 0, time.UTC)

	// Seed present, no override: the current rotating code wins, a stale one loses.
	t.Setenv("POS_SYSTEM_PIN", "")
	v := systemPINValidator(seed)
	if !v(support.Code(seed, now), now) {
		t.Fatal("current rotating code should validate")
	}
	if v(support.Code(seed, now.Add(-2*time.Hour)), now) {
		t.Fatal("two-hours-stale code should be rejected")
	}

	// Override set: only the override validates; the rotating code stops working.
	t.Setenv("POS_SYSTEM_PIN", "778899")
	v = systemPINValidator(seed)
	if !v("778899", now) {
		t.Fatal("POS_SYSTEM_PIN override should validate")
	}
	if v(support.Code(seed, now), now) {
		t.Fatal("rotating code must be rejected while override is set")
	}

	// No seed, no override: the 2273 fallback validates.
	t.Setenv("POS_SYSTEM_PIN", "")
	v = systemPINValidator(nil)
	if !v("2273", now) {
		t.Fatal("2273 fallback should validate when no seed and no override")
	}
	if v("000000", now) {
		t.Fatal("a wrong PIN must be rejected in fallback mode")
	}
}
