package repairs

import (
	"testing"
	"time"
)

// TestWarrantyUntil covers the date logic stamped at collection. The full
// collect path (which rings a core sale) is exercised live over HTTP in the
// Task 7 verification, because sales.Service.Create owns its own transaction and
// cannot participate in a rollback test — and adding a Tx variant would be a core
// change this plugin is not allowed to make.
func TestWarrantyUntil(t *testing.T) {
	from := time.Date(2026, 9, 8, 15, 30, 0, 0, time.UTC)

	if got := warrantyUntil(0, from); got != nil {
		t.Fatalf("0 days should give no warranty, got %v", got)
	}
	got := warrantyUntil(30, from)
	if got == nil {
		t.Fatal("30 days should give a warranty date")
	}
	want := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("warrantyUntil(30) = %v, want %v (date-only + 30d)", got, want)
	}
}
