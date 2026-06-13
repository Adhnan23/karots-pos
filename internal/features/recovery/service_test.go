package recovery

import (
	"context"
	"testing"

	"karots-pos/internal/apperr"
)

// These cases all fail validation BEFORE the transaction opens, so a nil-db
// Service exercises them safely. They guard the rules a recovery must satisfy.
func TestRecordValidation(t *testing.T) {
	s := &Service{} // db is never reached on these paths
	ctx := context.Background()
	sup := int64(1)

	tests := []struct {
		name string
		in   CreateInput
	}{
		{"bad source", CreateInput{SourceType: "bogus", Outcome: OutcomeWrittenOff}},
		{"bad outcome", CreateInput{SourceType: SourceDamage, Outcome: "bogus"}},
		{"paid without amount", CreateInput{SourceType: SourceDamage, Outcome: OutcomePaid, SupplierID: &sup}},
		{"paid zero amount", CreateInput{SourceType: SourceDamage, Outcome: OutcomePaid, SupplierID: &sup, RecoveredAmount: "0"}},
		{"paid without supplier", CreateInput{SourceType: SourceDamage, Outcome: OutcomePaid, RecoveredAmount: "100"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := s.Record(ctx, tc.in, 1)
			if err == nil {
				t.Fatalf("expected a validation error, got nil")
			}
			if _, ok := apperr.As(err); !ok {
				t.Fatalf("expected an AppError, got %T: %v", err, err)
			}
		})
	}
}

func TestNilIfEmpty(t *testing.T) {
	if nilIfEmpty("") != nil {
		t.Error("empty string should map to nil")
	}
	if got := nilIfEmpty("x"); got == nil || *got != "x" {
		t.Errorf("non-empty should map to its pointer, got %v", got)
	}
}
