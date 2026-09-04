package adminpages

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func d(n int64) decimal.Decimal { return decimal.NewFromInt(n) }

func TestDeltaInfoDirection(t *testing.T) {
	cases := []struct {
		cur, prev int64
		wantDir   int
	}{
		{100, 50, 1},   // up
		{50, 100, -1},  // down
		{0, 0, 0},      // flat
		{10, 0, 1},     // new (prev zero)
		{50, 50, 0},    // unchanged
		{-20, -10, -1}, // profit worsened (more loss)
	}
	for _, c := range cases {
		txt, dir := deltaInfo(d(c.cur), d(c.prev))
		if dir != c.wantDir {
			t.Errorf("deltaInfo(%d,%d) dir=%d want %d", c.cur, c.prev, dir, c.wantDir)
		}
		if txt == "" {
			t.Errorf("deltaInfo(%d,%d) returned empty text", c.cur, c.prev)
		}
	}
}

func TestWeekdayName(t *testing.T) {
	if WeekdayName(0) != "Sun" || WeekdayName(6) != "Sat" {
		t.Fatal("DOW 0 must be Sun and 6 Sat (Postgres convention)")
	}
	if WeekdayName(9) != "?" {
		t.Error("out-of-range DOW should be '?'")
	}
}

func TestPeakBgStyleClamp(t *testing.T) {
	// No data (max 0) → floor alpha, never a divide-by-zero.
	if !strings.Contains(peakBgStyle(0, 0), "0.15") {
		t.Error("max=0 should give floor alpha 0.15")
	}
	// Hottest cell → full alpha.
	if !strings.Contains(peakBgStyle(10, 10), "1.00") {
		t.Errorf("count==max should give alpha 1.00, got %q", peakBgStyle(10, 10))
	}
}
