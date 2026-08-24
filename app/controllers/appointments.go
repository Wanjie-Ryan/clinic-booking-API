package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/codes"

	"github.com/Wanjie-Ryan/clinic-booking-API/app/constants"
	"github.com/Wanjie-Ryan/clinic-booking-API/app/library"
	"github.com/Wanjie-Ryan/clinic-booking-API/app/models"
)

// BookAppointment books a slot for a patient with a doctor.
// POST /appointments
func (controller *Controller) BookAppointment(c echo.Context) error {
	ctx, span := controller.Tracer.Start(c.Request().Context(), "BookAppointment")
	defer span.End()

	startTime := time.Now()
	defer func() {
		library.Histogram(ctx, "book_appointment.duration", "how long it takes to book an appointment", startTime)
	}()

	req := new(models.BookAppointmentRequest)
	if err := c.Bind(req); err != nil {
		return library.RespondRaw(c, http.StatusBadRequest, models.ErrorResponse{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "invalid request body",
		})
	}

	if req.DoctorID <= 0 || req.PatientID <= 0 {
		return library.RespondRaw(c, http.StatusBadRequest, models.ErrorResponse{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "doctor_id and patient_id are required",
		})
	}

	start, err := time.Parse(time.RFC3339, req.StartTime)
	if err != nil {
		return library.RespondRaw(c, http.StatusBadRequest, models.ErrorResponse{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "start_time must be RFC3339, e.g. 2026-08-25T09:00:00+03:00",
		})
	}

	var doctorExists bool
	err = controller.DB.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM doctors WHERE id = ?)", req.DoctorID).Scan(&doctorExists)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error checking doctor exists",
			constants.DATA:        req.DoctorID,
		}).Error(err.Error())
		return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "internal server error",
		})
	}
	if !doctorExists {
		return library.RespondRaw(c, http.StatusNotFound, models.ErrorResponse{
			ErrorCode:    http.StatusNotFound,
			ErrorMessage: "doctor not found",
		})
	}

	var patientExists bool
	err = controller.DB.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM patients WHERE id = ?)", req.PatientID).Scan(&patientExists)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error checking patient exists",
			constants.DATA:        req.PatientID,
		}).Error(err.Error())
		return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "internal server error",
		})
	}
	if !patientExists {
		return library.RespondRaw(c, http.StatusNotFound, models.ErrorResponse{
			ErrorCode:    http.StatusNotFound,
			ErrorMessage: "patient not found",
		})
	}

	weekday := int(start.In(controller.ClinicLocation).Weekday())

	rows, err := controller.DB.QueryContext(ctx,
		`SELECT start_time, end_time
		 FROM doctor_working_hours
		 WHERE doctor_id = ? AND day_of_week = ?
		 ORDER BY start_time`,
		req.DoctorID, weekday)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error fetching working hours",
			constants.DATA:        req.DoctorID,
		}).Error(err.Error())
		return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "internal server error",
		})
	}

	var ranges []library.WorkingRange
	for rows.Next() {
		var rangeStart, rangeEnd string
		if err := rows.Scan(&rangeStart, &rangeEnd); err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			logrus.WithContext(ctx).WithFields(logrus.Fields{
				constants.DESCRIPTION: "error scanning working hours row",
			}).Error(err.Error())
			continue
		}
		ranges = append(ranges, library.WorkingRange{Start: rangeStart, End: rangeEnd})
	}
	rows.Close()

	if err := library.ValidateSlot(start, ranges, controller.ClinicLocation, controller.SlotDuration, controller.MinimumLeadTime, time.Now()); err != nil {
		return library.RespondRaw(c, http.StatusBadRequest, models.ErrorResponse{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: validationMessage(err, controller.MinimumLeadTime),
		})
	}

	end := start.Add(controller.SlotDuration)

	// Courtesy check: cheap, common-case 409 without needing to provoke a MySQL
	// error. The INSERT below is what's actually authoritative under
	// concurrency (README section 1.3) -- two requests can both pass this
	// SELECT before either commits.
	var alreadyBooked bool
	err = controller.DB.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM appointments WHERE doctor_id = ? AND start_time = ? AND status = ?)`,
		req.DoctorID, start.UTC(), constants.StatusBooked).Scan(&alreadyBooked)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error checking existing booking",
			constants.DATA:        req.DoctorID,
		}).Error(err.Error())
		return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "internal server error",
		})
	}
	if alreadyBooked {
		return library.RespondRaw(c, http.StatusConflict, models.ErrorResponse{
			ErrorCode:    http.StatusConflict,
			ErrorMessage: "slot is already booked",
		})
	}

	result, err := controller.DB.ExecContext(ctx,
		`INSERT INTO appointments (doctor_id, patient_id, start_time, end_time, status)
		 VALUES (?, ?, ?, ?, ?)`,
		req.DoctorID, req.PatientID, start.UTC(), end.UTC(), constants.StatusBooked)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == constants.MySQLDuplicateEntryErrorCode {
			// The unique constraint on active_slot_key is what's actually
			// enforcing this -- see README section 1.3. This is the case the
			// whole double-booking guard exists for: two requests raced past
			// the courtesy SELECT above, and the database rejected the loser.
			logrus.WithContext(ctx).WithFields(logrus.Fields{
				constants.DESCRIPTION: "slot already taken (unique constraint)",
				constants.DATA:        map[string]interface{}{"doctor_id": req.DoctorID, "start_time": start.UTC()},
			}).Warn("duplicate booking rejected by database")

			return library.RespondRaw(c, http.StatusConflict, models.ErrorResponse{
				ErrorCode:    http.StatusConflict,
				ErrorMessage: "slot was just taken",
			})
		}

		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error inserting appointment",
			constants.DATA:        req,
		}).Error(err.Error())
		return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "internal server error",
		})
	}

	id, err := result.LastInsertId()
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error reading inserted appointment id",
		}).Error(err.Error())
		return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "internal server error",
		})
	}

	return library.RespondRaw(c, http.StatusCreated, models.AppointmentResponse{
		ID:        id,
		DoctorID:  req.DoctorID,
		PatientID: req.PatientID,
		StartTime: start.UTC().Format(time.RFC3339),
		EndTime:   end.UTC().Format(time.RFC3339),
		Status:    constants.StatusBooked,
	})
}

// validationMessage translates a library.ErrSlot* sentinel into the message returned
// to the client.
func validationMessage(err error, minimumLead time.Duration) string {
	switch {
	case errors.Is(err, library.ErrSlotInPast):
		return "start_time is in the past"
	case errors.Is(err, library.ErrSlotTooSoon):
		return fmt.Sprintf("start_time must be at least %s from now", minimumLead)
	case errors.Is(err, library.ErrSlotNotAligned):
		return "start_time does not align to the doctor's slot grid"
	case errors.Is(err, library.ErrSlotOutsideWorkingHours):
		return "start_time falls outside the doctor's working hours"
	default:
		return "start_time is not bookable"
	}
}
