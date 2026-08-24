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
	if start.Before(now) {
		return ErrSlotInPast
	}
	if start.Before(now.Add(minimumLead)) {
		return ErrSlotTooSoon
	}

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

		if local.Before(rangeStart) || local.Add(slotDuration).After(rangeEnd) {
			continue
		}

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
