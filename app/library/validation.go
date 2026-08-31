package library

import (
	"errors"
	"time"
)

var (
	ErrSlotInPast              = errors.New("slot is in the past")
	ErrSlotTooSoon             = errors.New("slot is within the minimum lead time")
	ErrSlotNotAligned          = errors.New("slot does not align to the doctor's slot grid")
	ErrSlotOutsideWorkingHours = errors.New("slot falls outside the doctor's working hours")
)

// ValidateSlot checks that start is a legitimate slot to book: not in the past, not
// inside the minimum lead window, and inside one of the doctor's working-hour ranges
// for that weekday, aligned to the slot grid. It does not check whether the slot is
// already taken by another appointment -- that is enforced by the database's unique
// constraint on insert (README section 1.3) and is deliberately not duplicated here.
func ValidateSlot(start time.Time, ranges []WorkingRange, loc *time.Location, slotDuration, minimumLead time.Duration, now time.Time) error {
	// start_time for patient < current_time
	if start.Before(now) {
		return ErrSlotInPast
	}
	// start_time for patient < current_time + 60 minutes
	// computes the earliest bookable instant - right now, plus 60 minutes. if the request falls before that line, reject. This is to allow room for preparation
	if start.Before(now.Add(minimumLead)) {
		return ErrSlotTooSoon
	}

	// converts the start_time into Nairobi wall clock representation
	local := start.In(loc)
	year, month, day := local.Date()

	withinAnyRange := false

	for _, r := range ranges {
		rangeStart, err := wallClockOn(r.Start, year, month, day, loc)
		if err != nil {
			return err
		}
		rangeEnd, err := wallClockOn(r.End, year, month, day, loc)
		if err != nil {
			return err
		}

		// say we have patient booked in at 10Am (this is the local now)
		// range is between 08:00 - 13:00

		if local.Before(rangeStart) || local.Add(slotDuration).After(rangeEnd) {
			// question being asked here is, is 10Am before 08:00, No, and if we add 30 minutes to 10Am does it come after 13:00, still NO
			// continue does not run here, execution falls through past the if block, withinrange = true gets set, then alignment check runs next.
			continue
		}

		// usecase for 13:15, it will pass the before check, but fail the after cause 13:15 + 30 = 13:45 past 13:00, continue does run here.
		// once continue is triggered, execution jumps back up to the top of the for loop, skipping the withinrange and the alignment check.
		// it doesn't exit the function though, and does no return an error yet - it just abandons checking 13:15 against range 1, and moves onto check it against range 2 {14:00 - 17:00}

		// NOTE: ranges for this doctor has 2 entries; The loop's whole job is: "check this candidate time against every range this doctor has, and see if any single one of them fits."
		// CONTINUE is the mechanism that says "this range was a no, don't bother finishing the checks for it, go try the next range instead."

		withinAnyRange = true

		if local.Sub(rangeStart)%slotDuration == 0 {
			return nil
		}
	}

	if withinAnyRange {
		return ErrSlotNotAligned
	}
	return ErrSlotOutsideWorkingHours
}
