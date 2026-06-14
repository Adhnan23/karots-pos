package response

import "testing"

// ToastAnd must emit events in the order given (not alphabetically sorted, as a
// Go map would), so callers can keep "close-modal" last — htmx dispatches the
// HX-Trigger events in JSON order on the triggering element, and closing the
// modal detaches that element, which would drop any later events.
func TestToastAndPreservesEventOrder(t *testing.T) {
	got := ToastAnd("Product created", "success", "reload-products", "close-modal")
	want := `{"show-toast":{"level":"success","message":"Product created"},"reload-products":true,"close-modal":true}`
	if got != want {
		t.Fatalf("ToastAnd order wrong:\n got: %s\nwant: %s", got, want)
	}
}
