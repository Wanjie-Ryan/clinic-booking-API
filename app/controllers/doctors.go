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
	weekday := int(date.Weekday())

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

	var ranges []library.WorkingRange
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
	}

	// No working-hour rows for this weekday means the doctor simply doesn't work
	// that day -- an empty slot list, not an error.
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

	dayStart := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, controller.ClinicLocation).UTC()
	dayEnd := dayStart.Add(24 * time.Hour)

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
