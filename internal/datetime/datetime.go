// Package datetime formats timestamps in the shop's local timezone. The DB
// stores instants in UTC; displaying them raw shows UTC (e.g. 5h30m behind in
// Sri Lanka), so receipts/reports must convert to the local zone first.
package datetime

import (
	"os"
	"time"
)

// Location is the display timezone. It honors the TZ env var when set, otherwise
// defaults to Asia/Colombo (the shop locale). tzdata is embedded in the binary
// (see cmd/server) so LoadLocation works even on a host without system zoneinfo.
var Location = load()

func load() *time.Location {
	if tz := os.Getenv("TZ"); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			return l
		}
	}
	if l, err := time.LoadLocation("Asia/Colombo"); err == nil {
		return l
	}
	return time.Local
}

// DateTime renders a timestamp as "2006-01-02 15:04" in the shop timezone.
func DateTime(t time.Time) string { return t.In(Location).Format("2006-01-02 15:04") }

// Clock renders just the time as "15:04" in the shop timezone.
func Clock(t time.Time) string { return t.In(Location).Format("15:04") }
