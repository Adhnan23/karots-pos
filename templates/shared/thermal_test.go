package shared

import "testing"

// The size shown in the view must ride along to the print endpoint, so a printed
// slip follows the switcher rather than only the saved setting.
func TestThermalFromCarriesSizeToPrintURL(t *testing.T) {
	base, printURL := "/cashier/money-receipts/7", "/cashier/money-receipts/7/print"

	// Saved 80mm, no override → view + print both 80.
	d := ThermalFrom("80", "", "R", base, printURL)
	if d.Narrow {
		t.Fatalf("want wide for 80mm setting")
	}
	if d.PrintURL != printURL+"?size=80" {
		t.Fatalf("print URL missing size=80: %q", d.PrintURL)
	}

	// Switched to 58 in the view → print must follow to 58.
	d = ThermalFrom("80", "58", "R", base, printURL)
	if !d.Narrow || d.PrintURL != printURL+"?size=58" {
		t.Fatalf("size switch not carried: narrow=%v url=%q", d.Narrow, d.PrintURL)
	}

	// A printURL that already has a query keeps it and appends with &.
	d = ThermalFrom("58", "", "R", base, printURL+"?kick=1")
	if d.PrintURL != printURL+"?kick=1&size=58" {
		t.Fatalf("query-append wrong: %q", d.PrintURL)
	}
}
