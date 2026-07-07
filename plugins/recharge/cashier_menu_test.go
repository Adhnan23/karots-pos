package recharge

import (
	"testing"

	"github.com/shopspring/decimal"
)

// TestReloadDeviceNodeShape verifies the amount-leaf built for one device row:
// kind/action, the fixed add_url, and the carrier/device meta the client will
// echo straight back to MenuReloadAdd.
func TestReloadDeviceNodeShape(t *testing.T) {
	d := DeviceBalanceRow{
		ID: 9, CarrierID: 3, Carrier: "Dialog", Label: "SIM 077",
		Number: "0771234567", Balance: decimal.RequireFromString("500"),
	}
	n := reloadDeviceNode(3, d)
	if n.Kind != "leaf" || n.Action != "amount" {
		t.Fatalf("kind/action: %+v", n)
	}
	if n.AddURL != "/cashier/recharge/menu/reload" {
		t.Fatalf("add_url: %s", n.AddURL)
	}
	if n.Meta["carrier_id"] != int64(3) || n.Meta["device_id"] != int64(9) {
		t.Fatalf("meta: %v", n.Meta)
	}
	if n.Name != "Reload — SIM 077 · 0771234567" {
		t.Fatalf("name: %s", n.Name)
	}
}

// TestReloadDeviceNodeShapeNoNumber verifies the label omits the "· number"
// suffix when the device has none (e.g. a fixed terminal).
func TestReloadDeviceNodeShapeNoNumber(t *testing.T) {
	d := DeviceBalanceRow{ID: 5, Label: "Terminal A"}
	n := reloadDeviceNode(1, d)
	if n.Name != "Reload — Terminal A" {
		t.Fatalf("name: %s", n.Name)
	}
}

// TestParseAmountRejectsNonPositive verifies the shared amount-step validation
// used by MenuReloadAdd rejects zero/negative and accepts a positive value.
func TestParseAmountRejectsNonPositive(t *testing.T) {
	if _, err := parseAmount("0"); err == nil {
		t.Fatal("expected error for 0")
	}
	if _, err := parseAmount("-5"); err == nil {
		t.Fatal("expected error for -5")
	}
	v, err := parseAmount("100")
	if err != nil || !v.Equal(decimal.RequireFromString("100")) {
		t.Fatalf("100: %v %v", v, err)
	}
}
