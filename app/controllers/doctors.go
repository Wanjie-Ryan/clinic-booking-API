package controllers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/codes"

	"github.com/Wanjie-Ryan/clinic-booking-API/app/constants"
	"github.com/Wanjie-Ryan/clinic-booking-API/app/library"
	"github.com/Wanjie-Ryan/clinic-booking-API/app/models"
)

// GetDoctorAvailability returns every free slot for a doctor on a given date.
// GET /doctors/:id/availability?date=YYYY-MM-DD
// The DB just stores the time value as it is, no context attached. Conversion was never a DB/server concern. It was always 100%, a Go code comcern.
// The physical location of the machine running the code is irrelevant; what matters is that the code has one fixed, explicit, correct answer for that nairobi time means, never guesses based on wherever it happens to be executing.
func (controller *Controller) GetDoctorAvailability(c echo.Context) error {
	ctx, span := controller.Tracer.Start(c.Request().Context(), "GetDoctorAvailability")
	defer span.End()

	startTime := time.Now()
	defer func() {
		library.Histogram(ctx, "doctor_availability.duration", "how long it takes to compute doctor availability", startTime)
	}()

	doctorID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || doctorID <= 0 {
		return library.RespondRaw(c, http.StatusBadRequest, models.ErrorResponse{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "invalid doctor id",
		})
	}

	dateParam := c.QueryParam("date")
	if dateParam == "" {
		return library.RespondRaw(c, http.StatusBadRequest, models.ErrorResponse{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "date query parameter is required, format YYYY-MM-DD",
		})
	}

	// parseInlocation interprets time as in the given location, in the absence of time zone info, Parse inteprets time as UTC
	// Anchor the date here to Nairobi cause of the Cliniclocation in the controller eg 2026-08-27 becomes Aug 27, 2026, 00:00:00
	date, err := time.ParseInLocation("2006-01-02", dateParam, controller.ClinicLocation)
	if err != nil {
		return library.RespondRaw(c, http.StatusBadRequest, models.ErrorResponse{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "invalid date, expected format YYYY-MM-DD",
		})
	}

	cacheKey := availabilityCacheKey(doctorID, dateParam)
	cached, err := library.GetRedisKey(ctx, controller.RedisConn, cacheKey)
	if err == nil {
		var cachedResponse models.AvailabilityResponse
		if jsonErr := json.Unmarshal([]byte(cached), &cachedResponse); jsonErr == nil {
			return library.RespondRaw(c, http.StatusOK, cachedResponse)
		}
	} else if !errors.Is(err, redis.Nil) {
		// Redis being unreachable should degrade to computing the answer
		// normally, not fail the request -- the cache is an optimisation, not
		// a dependency.
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error reading availability cache",
			constants.DATA:        cacheKey,
		}).Warn(err.Error())
	}

	// check if doc exist
	var doctorName string
	err = controller.DB.QueryRowContext(ctx, "SELECT name FROM doctors WHERE id = ?", doctorID).Scan(&doctorName)
	if errors.Is(err, sql.ErrNoRows) {
		return library.RespondRaw(c, http.StatusNotFound, models.ErrorResponse{
			ErrorCode:    http.StatusNotFound,
			ErrorMessage: "doctor not found",
		})
	}
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error fetching doctor",
			constants.DATA:        doctorID,
		}).Error(err.Error())
		return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "internal server error",
		})
	}

	// time.Weekday is 0=Sunday..6=Saturday, matching day_of_week in
	// doctor_working_hours (README section 1.2).
	// tells us which day of the week it is, give 27th August, then it becomes Thursday which is 4
	weekday := int(date.Weekday())

	//find the doctors working hours using the day of week and doctors id
	rows, err := controller.DB.QueryContext(ctx,
		`SELECT start_time, end_time
		 FROM doctor_working_hours
		 WHERE doctor_id = ? AND day_of_week = ?
		 ORDER BY start_time`,
		doctorID, weekday)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error fetching working hours",
			constants.DATA:        doctorID,
		}).Error(err.Error())
		return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "internal server error",
		})
	}
	defer rows.Close()

	// cfeates an empty slice that can hold start and end times.
	var ranges []library.WorkingRange
	// row is a cursor MySQL handed back - it doesn't contain data yet, its more like a pointer sitting before the 1st row.
	// rows.next moves the cursor forward one row, and returns true if there was a row to move to, false if there's nothing left
	// the query returns 2 rows
	for rows.Next() {
		var start, end string
		if err := rows.Scan(&start, &end); err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			logrus.WithContext(ctx).WithFields(logrus.Fields{
				constants.DESCRIPTION: "error scanning working hours row",
			}).Error(err.Error())
			continue
		}
		ranges = append(ranges, library.WorkingRange{Start: start, End: end})
		// the for loop iterates two time and appends the second value to the ranges array.
		// now ranges = [{08:00:00, 13:00:00}, {14:00:00, 17:00:00}]
		// does the 3rd iteration returns false and loop exits.
	}

	// No working-hour rows for this weekday means the doctor simply doesn't work
	// that day -- an empty slot list, not an error.
	// if nothing was fetched from the DB, return an empty slot
	if len(ranges) == 0 {
		response := models.AvailabilityResponse{
			DoctorID: doctorID,
			Date:     dateParam,
			Slots:    []string{},
		}
		controller.cacheAvailability(ctx, cacheKey, response)
		return library.RespondRaw(c, http.StatusOK, response)
	}

	candidates, err := library.GenerateSlots(ranges, date, controller.ClinicLocation, controller.SlotDuration)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error generating slots",
			constants.DATA:        doctorID,
		}).Error(err.Error())
		return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "internal server error",
		})
	}

	// supposed this the date from the param date = 2026-08-31
	// daystart creates 2026-08-31 00:00:00 Africa/Nairobi
	// then convert to utc it becomes +3
	// since nairobi is UTC + 3 already
	// 2026-08-31 00:00:00 Nairobi
	// ↓ UTC
	// 2026-08-30 21:00:00 UTC
	dayStart := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, controller.ClinicLocation).UTC()
	dayEnd := dayStart.Add(24 * time.Hour)

	// the query becomes
	// 	SELECT start_time
	// FROM appointments
	// WHERE doctor_id = 2
	//   AND status = 'booked'
	//   AND start_time >= '2026-08-30 21:00:00'
	//   AND start_time < '2026-08-31 21:00:00';
	bookedRows, err := controller.DB.QueryContext(ctx,
		`SELECT start_time
		 FROM appointments
		 WHERE doctor_id = ? AND status = ? AND start_time >= ? AND start_time < ?`,
		doctorID, constants.StatusBooked, dayStart, dayEnd)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error fetching booked appointments",
			constants.DATA:        doctorID,
		}).Error(err.Error())
		return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "internal server error",
		})
	}
	defer bookedRows.Close()

	// make is Go's built in for initializing a map, slice, or channel before use.
	// make gives you a real, empty, ready to write map.
	booked := make(map[time.Time]bool)
	for bookedRows.Next() {
		var start time.Time
		if err := bookedRows.Scan(&start); err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			logrus.WithContext(ctx).WithFields(logrus.Fields{
				constants.DESCRIPTION: "error scanning booked appointment row",
			}).Error(err.Error())
			continue
		}
		booked[start.UTC()] = true
	}

	available := library.AvailableSlots(candidates, booked, time.Now(), controller.MinimumLeadTime)

	slots := make([]string, 0, len(available))
	for _, s := range available {
		slots = append(slots, s.Format(time.RFC3339))
	}

	response := models.AvailabilityResponse{
		DoctorID: doctorID,
		Date:     dateParam,
		Slots:    slots,
	}
	controller.cacheAvailability(ctx, cacheKey, response)
	return library.RespondRaw(c, http.StatusOK, response)
}

// doctor_working_hours table time is in nairobi hence the wallclockon conversion while appointments is already in UTC
