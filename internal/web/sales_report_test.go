package web

import (
	"testing"
	"time"
)

func TestPrevPeriodIsEqualLengthImmediatelyBefore(t *testing.T) {
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC) // 7-day window
	pFrom, pTo := prevPeriod(from, to)
	if !pTo.Equal(from) {
		t.Errorf("prev period should end where current begins: got pTo=%v want %v", pTo, from)
	}
	if got := to.Sub(from); pTo.Sub(pFrom) != got {
		t.Errorf("prev period length %v != current %v", pTo.Sub(pFrom), got)
	}
	if want := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC); !pFrom.Equal(want) {
		t.Errorf("pFrom = %v, want %v", pFrom, want)
	}
}

func TestFmtHour(t *testing.T) {
	for in, want := range map[int]string{0: "00:00", 7: "07:00", 18: "18:00", 23: "23:00"} {
		if got := fmtHour(in); got != want {
			t.Errorf("fmtHour(%d) = %q, want %q", in, got, want)
		}
	}
}
