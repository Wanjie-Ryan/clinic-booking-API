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
	// extracts hours = 8, minute = 0, second = 0
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid time %q: %w", hhmmss, err)
	}
	// return time.Date(2026, August, 31, 8, 0, 0, 0, Nairobiloc)
	// it builds an instant 2026-08-31 08:00:00 +03:00
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

	// ranges comes in as [{08:00:00, 13:00:00}, {14:00:00, 17:00:00}]
	year, month, day := date.Date()

	var slots []time.Time
	// range is a Go keyword for "loop over every item in this slice". It gives you (index, value) the _ throws away the index, r is the current item.
	for _, r := range ranges {
		// then this gives you outer iteration 1:: r ={t=start:"08:00:00", end:"13:00:00"}
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

		// trace the above with real values, slotDuration = 30 minutes
		// t is assigned start time which is 08:00 then add 30 minutes to it, the condition is is 08:30 After the end which is 13:00, the answer is no. Append 08:00 then advance.
		// It continues like this upto 13:00 where it tries to add 30 minutes to it, and it becomes 13:30, ask again is 13:30 after end time which is 13:00? Answer this time is YES, the loop stops, 13:00 is NOT appended
		// this produces exactly 10 slots from 08:00 through 12:30

		// the outer iteration 2 {14:00:00 - 17:00:00}. This one produces 14:00:00 through 16:30:00 - 6 more slots converted to UTC and get appended onto the same slots

		// a total of 16 slots

	}

	return slots, nil
}

// AvailableSlots removes booked slots and anything inside the minimum lead window from
// now, returning what's left. Order follows candidates.
func AvailableSlots(candidates []time.Time, booked map[time.Time]bool, now time.Time, minimumLead time.Duration) []time.Time {
	cutoff := now.Add(minimumLead)

	// pre-allocates a slice with 0 elements but room for 16
	available := make([]time.Time, 0, len(candidates))

	// the candidates here are 05:00z, 05:30z upto 13:30z

	// start with  05:00z given the booked which is (07:00z) do they match? false is 05:00 before time.now, which time.now returns a full date + time in UTC, so given that the candidates are all after today, this check will be just pass
	for _, s := range candidates {
		if booked[s.UTC()] {
			continue
			// continue skips to the next loop iteration - it never reaches the append line for that one entry. Everything else reaches append
		}
		if s.Before(cutoff) {
			continue
		}
		available = append(available, s)
	}

	return available
}
