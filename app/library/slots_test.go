package library

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nairobi(t *testing.T) *time.Location {
	loc, err := time.LoadLocation("Africa/Nairobi")
	require.NoError(t, err)
	return loc
}

func TestGenerateSlots_SingleFullRange(t *testing.T) {
	loc := nairobi(t)
	ranges := []WorkingRange{{Start: "08:00:00", End: "13:00:00"}}
	date := time.Date(2026, 8, 24, 0, 0, 0, 0, loc)

	slots, err := GenerateSlots(ranges, date, loc, 30*time.Minute)
	require.NoError(t, err)

	assert.Len(t, slots, 10)
	assert.Equal(t, "08:00", slots[0].In(loc).Format("15:04"))
	assert.Equal(t, "12:30", slots[len(slots)-1].In(loc).Format("15:04"))
}

func TestGenerateSlots_TwoRangesWithBreak(t *testing.T) {
	loc := nairobi(t)
	ranges := []WorkingRange{
		{Start: "08:00:00", End: "13:00:00"},
		{Start: "14:00:00", End: "17:00:00"},
	}
	date := time.Date(2026, 8, 24, 0, 0, 0, 0, loc)

	slots, err := GenerateSlots(ranges, date, loc, 30*time.Minute)
	require.NoError(t, err)

	assert.Len(t, slots, 16, "10 slots in 08-13, 6 slots in 14-17")

	for _, s := range slots {
		hm := s.In(loc).Format("15:04")
		assert.NotEqual(t, "13:00", hm)
		assert.NotEqual(t, "13:30", hm)
	}
}

func TestGenerateSlots_UnevenRangeStopsEarly(t *testing.T) {
	loc := nairobi(t)
	ranges := []WorkingRange{{Start: "08:00:00", End: "10:15:00"}}
	date := time.Date(2026, 8, 24, 0, 0, 0, 0, loc)

	slots, err := GenerateSlots(ranges, date, loc, 30*time.Minute)
	require.NoError(t, err)

	var got []string
	for _, s := range slots {
		got = append(got, s.In(loc).Format("15:04"))
	}
	assert.Equal(t, []string{"08:00", "08:30", "09:00", "09:30"}, got)
}

func TestGenerateSlots_InvalidRange(t *testing.T) {
	loc := nairobi(t)
	ranges := []WorkingRange{{Start: "13:00:00", End: "08:00:00"}}
	date := time.Date(2026, 8, 24, 0, 0, 0, 0, loc)

	_, err := GenerateSlots(ranges, date, loc, 30*time.Minute)
	assert.Error(t, err)
}

func TestAvailableSlots_RemovesBookedAndTooSoon(t *testing.T) {
	loc := nairobi(t)
	ranges := []WorkingRange{{Start: "08:00:00", End: "10:00:00"}}
	date := time.Date(2026, 8, 24, 0, 0, 0, 0, loc)

	candidates, err := GenerateSlots(ranges, date, loc, 30*time.Minute)
	require.NoError(t, err)
	require.Len(t, candidates, 4) // 08:00, 08:30, 09:00, 09:30

	booked := map[time.Time]bool{
		candidates[1].UTC(): true, // 08:30 taken
	}

	// "now" is 07:00 local with a 60-minute lead -- cutoff is exactly 08:00. A slot
	// exactly at the cutoff is exactly the minimum lead away, so it's still
	// bookable (only strictly-before-cutoff is excluded).
	now := time.Date(2026, 8, 24, 7, 0, 0, 0, loc)

	available := AvailableSlots(candidates, booked, now, 60*time.Minute)

	var got []string
	for _, s := range available {
		got = append(got, s.In(loc).Format("15:04"))
	}
	assert.Equal(t, []string{"08:00", "09:00", "09:30"}, got)
}
