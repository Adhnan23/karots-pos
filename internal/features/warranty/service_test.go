package warranty

import (
	"testing"
	"time"
)

func TestUntil(t *testing.T) {
	tests := []struct {
		name   string
		soldAt time.Time
		months int
		want   string
	}{
		{"12 months", time.Date(2026, 6, 13, 14, 30, 0, 0, time.UTC), 12, "2027-06-13"},
		{"no warranty", time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC), 0, "2026-06-13"},
		{"6 months crosses year", time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC), 6, "2026-04-01"},
		{"truncates time of day", time.Date(2026, 1, 15, 23, 59, 59, 0, time.UTC), 1, "2026-02-15"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Until(tc.soldAt, tc.months).Format("2006-01-02")
			if got != tc.want {
				t.Errorf("Until(%v, %d) = %q, want %q", tc.soldAt, tc.months, got, tc.want)
			}
		})
	}
}

func TestUnderWarranty(t *testing.T) {
	today := time.Now().UTC()
	tests := []struct {
		name   string
		status string
		until  time.Time
		want   bool
	}{
		{"active and future", "active", today.AddDate(0, 1, 0), true},
		{"active expiring today", "active", today, true},
		{"active expired yesterday", "active", today.AddDate(0, 0, -1), false},
		{"replaced unit never covered", "replaced", today.AddDate(1, 0, 0), false},
		{"void unit never covered", "void", today.AddDate(1, 0, 0), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u := Unit{Status: tc.status, WarrantyUntil: tc.until}
			if got := u.UnderWarranty(); got != tc.want {
				t.Errorf("UnderWarranty() status=%q until=%v = %v, want %v", tc.status, tc.until, got, tc.want)
			}
		})
	}
}
