package products

import (
	"context"
	"errors"
	"testing"

	"karots-pos/internal/apperr"
)

// The reassign guards must reject a bad pairing BEFORE touching the DB, so a
// zero-value Service (nil repo) is enough to prove they short-circuit. If a
// guard ever moved after the repo call this would nil-panic instead of failing.
func TestReassignPreferredSupplierGuards(t *testing.T) {
	s := &Service{} // nil db/repo: must not be reached
	cases := []struct{ name string; from, to int64 }{
		{"same supplier", 5, 5},
		{"zero from", 0, 3},
		{"zero to", 3, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ReassignPreferredSupplier(context.Background(), tc.from, tc.to)
			var ae *apperr.AppError
			if !errors.As(err, &ae) || ae.Status != apperr.Validation("").Status {
				t.Fatalf("want validation error, got %v", err)
			}
		})
	}
}
