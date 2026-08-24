package library

import (
	"fmt"
	"time"
)

// WorkingRange is one contiguous working-hours range on a single weekday, as stored in
// doctor_working_hours: wall-clock HH:MM:SS strings in the clinic's local timezone.
type WorkingRange struct {
	Start string
	End   string
}

// wallClockOn resolves a "HH:MM:SS" string to a concrete time.Time on the given date,
// in the given location.
func wallClockOn(hhmmss string, year int, month time.Month, day int, loc *time.Location) (time.Time, error) {
	parsed, err := time.Parse("15:04:05", hhmmss)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid time %q: %w", hhmmss, err)
	}
	return time.Date(year, month, day, parsed.Hour(), parsed.Minute(), parsed.Second(), 0, loc), nil
}

// GenerateSlots returns every candidate slot start time (in UTC) within the given
// ranges on the given date, in the given location, at slotDuration intervals. Ranges
// are assumed to already be the correct weekday's working hours -- GenerateSlots has
// no database dependency and doesn't resolve weekdays itself.
//
// A range that doesn't divide evenly by slotDuration stops early: an 08:00-10:15
// range with a 30-minute slot yields 08:00, 08:30, 09:00, 09:30, and stops --
// 09:45-10:15 is not offered, since a 30-minute appointment starting there would run
// past the end of the doctor's hours.
func GenerateSlots(ranges []WorkingRange, date time.Time, loc *time.Location, slotDuration time.Duration) ([]time.Time, error) {
	year, month, day := date.Date()

	var slots []time.Time
	for _, r := range ranges {
		start, err := wallClockOn(r.Start, year, month, day, loc)
		if err != nil {
			return nil, err
		}
		end, err := wallClockOn(r.End, year, month, day, loc)
		if err != nil {
			return nil, err
		}
		if !end.After(start) {
			return nil, fmt.Errorf("working range end %q is not after start %q", r.End, r.Start)
		}

		for t := start; !t.Add(slotDuration).After(end); t = t.Add(slotDuration) {
			slots = append(slots, t.UTC())
		}
	}

	return slots, nil
}

// AvailableSlots removes booked slots and anything inside the minimum lead window from
// now, returning what's left. Order follows candidates.
func AvailableSlots(candidates []time.Time, booked map[time.Time]bool, now time.Time, minimumLead time.Duration) []time.Time {
	cutoff := now.Add(minimumLead)

	available := make([]time.Time, 0, len(candidates))
	for _, s := range candidates {
		if booked[s.UTC()] {
			continue
		}
		if s.Before(cutoff) {
			continue
		}
		available = append(available, s)
	}

	return available
}
