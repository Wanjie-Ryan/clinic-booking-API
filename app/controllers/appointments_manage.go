package controllers

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/codes"

	"github.com/Wanjie-Ryan/clinic-booking-API/app/constants"
	"github.com/Wanjie-Ryan/clinic-booking-API/app/library"
	"github.com/Wanjie-Ryan/clinic-booking-API/app/models"
)

// CancelAppointment cancels an appointment with a reason. The slot becomes
// bookable again immediately, since active_slot_key is NULL for any non-'booked'
// row (README section 1.3).
// PATCH /appointments/:id/cancel
func (controller *Controller) CancelAppointment(c echo.Context) error {
	ctx, span := controller.Tracer.Start(c.Request().Context(), "CancelAppointment")
	defer span.End()

	startTime := time.Now()
	defer func() {
		library.Histogram(ctx, "cancel_appointment.duration", "how long it takes to cancel an appointment", startTime)
	}()

	appointmentID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || appointmentID <= 0 {
		return library.RespondRaw(c, http.StatusBadRequest, models.ErrorResponse{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "invalid appointment id",
		})
	}

	req := new(models.CancelAppointmentRequest)
	if err := c.Bind(req); err != nil {
		return library.RespondRaw(c, http.StatusBadRequest, models.ErrorResponse{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "invalid request body",
		})
	}
	if strings.TrimSpace(req.Reason) == "" {
		return library.RespondRaw(c, http.StatusBadRequest, models.ErrorResponse{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "reason is required",
		})
	}

	var doctorID, patientID int64
	var status string
	var appointmentStart, appointmentEnd time.Time
	err = controller.DB.QueryRowContext(ctx,
		"SELECT doctor_id, patient_id, status, start_time, end_time FROM appointments WHERE id = ?", appointmentID).
		Scan(&doctorID, &patientID, &status, &appointmentStart, &appointmentEnd)
	if errors.Is(err, sql.ErrNoRows) {
		return library.RespondRaw(c, http.StatusNotFound, models.ErrorResponse{
			ErrorCode:    http.StatusNotFound,
			ErrorMessage: "appointment not found",
		})
	}
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error fetching appointment",
			constants.DATA:        appointmentID,
		}).Error(err.Error())
		return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "internal server error",
		})
	}

	// The WHERE status = 'booked' below is the actual concurrency guard here:
	// if two cancel requests race, only the first UPDATE affects a row: the
	// second one matches zero rows because status is no longer 'booked' by the
	// time it runs, and RowsAffected tells us that without a second read.
	result, err := controller.DB.ExecContext(ctx,
		`UPDATE appointments SET status = ?, cancellation_reason = ? WHERE id = ? AND status = ?`,
		constants.StatusCancelled, req.Reason, appointmentID, constants.StatusBooked)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error cancelling appointment",
			constants.DATA:        appointmentID,
		}).Error(err.Error())
		return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "internal server error",
		})
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error reading rows affected",
		}).Error(err.Error())
		return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "internal server error",
		})
	}
	if rowsAffected == 0 {
		return library.RespondRaw(c, http.StatusConflict, models.ErrorResponse{
			ErrorCode:    http.StatusConflict,
			ErrorMessage: "appointment is already cancelled",
		})
	}

	// Cache invalidation happens after the write has committed, never before
	// (README section 1.5).
	controller.invalidateAvailabilityCache(ctx, doctorID, appointmentStart.UTC())

	reason := req.Reason
	return library.RespondRaw(c, http.StatusOK, models.AppointmentResponse{
		ID:                 appointmentID,
		DoctorID:           doctorID,
		PatientID:          patientID,
		StartTime:          appointmentStart.UTC().Format(time.RFC3339),
		EndTime:            appointmentEnd.UTC().Format(time.RFC3339),
		Status:             constants.StatusCancelled,
		CancellationReason: &reason,
	})
}

// RescheduleAppointment moves an appointment to a new slot. The original
// appointment row is cancelled (freeing its slot) and a new appointment row is
// inserted for the new slot, validated exactly as a fresh booking would be. Both
// writes happen in one transaction: if the new slot is unavailable, the original
// appointment is left untouched, not stranded half-cancelled (README section 1.3,
// D-05).
// PATCH /appointments/:id/reschedule
func (controller *Controller) RescheduleAppointment(c echo.Context) error {
	ctx, span := controller.Tracer.Start(c.Request().Context(), "RescheduleAppointment")
	defer span.End()

	startTime := time.Now()
	defer func() {
		library.Histogram(ctx, "reschedule_appointment.duration", "how long it takes to reschedule an appointment", startTime)
	}()

	appointmentID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || appointmentID <= 0 {
		return library.RespondRaw(c, http.StatusBadRequest, models.ErrorResponse{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "invalid appointment id",
		})
	}

	req := new(models.RescheduleAppointmentRequest)
	if err := c.Bind(req); err != nil {
		return library.RespondRaw(c, http.StatusBadRequest, models.ErrorResponse{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "invalid request body",
		})
	}

	newStart, err := time.Parse(time.RFC3339, req.StartTime)
	if err != nil {
		return library.RespondRaw(c, http.StatusBadRequest, models.ErrorResponse{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "start_time must be RFC3339, e.g. 2026-08-25T09:00:00+03:00",
		})
	}

	tx, err := controller.DB.BeginTx(ctx, nil)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error starting reschedule transaction",
		}).Error(err.Error())
		return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "internal server error",
		})
	}

	// SELECT ... FOR UPDATE locks this specific row for the rest of the
	// transaction, so a second reschedule/cancel request against the same
	// appointment ID blocks until this one commits or rolls back -- it can't
	// see a stale 'booked' status and race the UPDATE below.
	var doctorID, patientID int64
	var status string
	var oldStart time.Time
	err = tx.QueryRowContext(ctx,
		"SELECT doctor_id, patient_id, status, start_time FROM appointments WHERE id = ? FOR UPDATE", appointmentID).
		Scan(&doctorID, &patientID, &status, &oldStart)
	if errors.Is(err, sql.ErrNoRows) {
		tx.Rollback()
		return library.RespondRaw(c, http.StatusNotFound, models.ErrorResponse{
			ErrorCode:    http.StatusNotFound,
			ErrorMessage: "appointment not found",
		})
	}
	if err != nil {
		tx.Rollback()
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error fetching appointment for reschedule",
			constants.DATA:        appointmentID,
		}).Error(err.Error())
		return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "internal server error",
		})
	}

	if status == constants.StatusCancelled {
		tx.Rollback()
		return library.RespondRaw(c, http.StatusConflict, models.ErrorResponse{
			ErrorCode:    http.StatusConflict,
			ErrorMessage: "cannot reschedule a cancelled appointment",
		})
	}

	weekday := int(newStart.In(controller.ClinicLocation).Weekday())

	rows, err := tx.QueryContext(ctx,
		`SELECT start_time, end_time
		 FROM doctor_working_hours
		 WHERE doctor_id = ? AND day_of_week = ?
		 ORDER BY start_time`,
		doctorID, weekday)
	if err != nil {
		tx.Rollback()
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error fetching working hours for reschedule",
			constants.DATA:        doctorID,
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
			rows.Close()
			tx.Rollback()
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			logrus.WithContext(ctx).WithFields(logrus.Fields{
				constants.DESCRIPTION: "error scanning working hours row",
			}).Error(err.Error())
			return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: "internal server error",
			})
		}
		ranges = append(ranges, library.WorkingRange{Start: rangeStart, End: rangeEnd})
	}
	rows.Close()

	if err := library.ValidateSlot(newStart, ranges, controller.ClinicLocation, controller.SlotDuration, controller.MinimumLeadTime, time.Now()); err != nil {
		tx.Rollback()
		return library.RespondRaw(c, http.StatusBadRequest, models.ErrorResponse{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: validationMessage(err, controller.MinimumLeadTime),
		})
	}

	newEnd := newStart.Add(controller.SlotDuration)

	// Write 1: free the old slot.
	_, err = tx.ExecContext(ctx,
		`UPDATE appointments SET status = ?, cancellation_reason = ? WHERE id = ? AND status = ?`,
		constants.StatusCancelled, "rescheduled to a new slot", appointmentID, constants.StatusBooked)
	if err != nil {
		tx.Rollback()
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error cancelling old slot during reschedule",
			constants.DATA:        appointmentID,
		}).Error(err.Error())
		return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "internal server error",
		})
	}

	// Write 2: book the new slot. Same unique-constraint protection as a fresh
	// booking (README section 1.3) -- if another request took this exact slot
	// between our SELECT and here, this INSERT fails with 1062 and the whole
	// transaction rolls back, leaving the original appointment untouched.
	result, err := tx.ExecContext(ctx,
		`INSERT INTO appointments (doctor_id, patient_id, start_time, end_time, status)
		 VALUES (?, ?, ?, ?, ?)`,
		doctorID, patientID, newStart.UTC(), newEnd.UTC(), constants.StatusBooked)
	if err != nil {
		tx.Rollback()

		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == constants.MySQLDuplicateEntryErrorCode {
			return library.RespondRaw(c, http.StatusConflict, models.ErrorResponse{
				ErrorCode:    http.StatusConflict,
				ErrorMessage: "the new slot was just taken",
			})
		}

		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error booking new slot during reschedule",
			constants.DATA:        appointmentID,
		}).Error(err.Error())
		return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "internal server error",
		})
	}

	newID, err := result.LastInsertId()
	if err != nil {
		tx.Rollback()
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error reading new appointment id",
		}).Error(err.Error())
		return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "internal server error",
		})
	}

	if err := tx.Commit(); err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error committing reschedule transaction",
		}).Error(err.Error())
		return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "internal server error",
		})
	}

	// Both the freed old slot and the newly booked slot need their cache
	// entries invalidated -- they can fall on different local dates (README
	// section 1.5). Both happen only after the transaction has committed.
	controller.invalidateAvailabilityCache(ctx, doctorID, oldStart.UTC())
	controller.invalidateAvailabilityCache(ctx, doctorID, newStart.UTC())

	return library.RespondRaw(c, http.StatusOK, models.AppointmentResponse{
		ID:        newID,
		DoctorID:  doctorID,
		PatientID: patientID,
		StartTime: newStart.UTC().Format(time.RFC3339),
		EndTime:   newEnd.UTC().Format(time.RFC3339),
		Status:    constants.StatusBooked,
	})
}
