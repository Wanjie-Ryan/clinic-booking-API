package library

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// UNIT TESTING

func TestValidateSlot(t *testing.T) {
	loc := nairobi(t)
	ranges := []WorkingRange{
		{Start: "08:00:00", End: "13:00:00"},
		{Start: "14:00:00", End: "17:00:00"},
	}
	now := time.Date(2026, 8, 24, 6, 0, 0, 0, loc) // Monday, 06:00 local
	minimumLead := 60 * time.Minute
	slotDuration := 30 * time.Minute

	// table driven testing - instead of writing 8 separate nearly identical test functions (one per scenario)
	tests := []struct {
		name    string
		start   time.Time // input to test
		wantErr error     // what you expect back
	}{
		{"valid aligned slot in first range", time.Date(2026, 8, 24, 9, 0, 0, 0, loc), nil}, // we expect no error, should be a valid booking
		{"valid aligned slot in second range", time.Date(2026, 8, 24, 15, 30, 0, 0, loc), nil},
		{"in the past", time.Date(2026, 8, 24, 5, 0, 0, 0, loc), ErrSlotInPast},
		{"future but inside minimum lead window", time.Date(2026, 8, 24, 6, 30, 0, 0, loc), ErrSlotTooSoon},
		{"during lunch break, between ranges", time.Date(2026, 8, 24, 13, 30, 0, 0, loc), ErrSlotOutsideWorkingHours},
		{"before working hours start", time.Date(2026, 8, 24, 7, 0, 0, 0, loc), ErrSlotOutsideWorkingHours},
		{"not aligned to the 30-minute grid", time.Date(2026, 8, 24, 9, 15, 0, 0, loc), ErrSlotNotAligned},
		{"starts before close but the appointment would run past it", time.Date(2026, 8, 24, 12, 45, 0, 0, loc), ErrSlotOutsideWorkingHours},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSlot(tt.start, ranges, loc, slotDuration, minimumLead, now)
			if tt.wantErr == nil {
				assert.NoError(t, err)
				// assert records a failure, but lets that specific subtest's remaining code finish running rather than aborting immediately.
			} else {
				assert.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
}
