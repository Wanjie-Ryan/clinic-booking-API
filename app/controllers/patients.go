package controllers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/codes"

	"github.com/Wanjie-Ryan/clinic-booking-API/app/constants"
	"github.com/Wanjie-Ryan/clinic-booking-API/app/library"
	"github.com/Wanjie-Ryan/clinic-booking-API/app/models"
)

// GetPatientAppointments returns a patient's upcoming (booked, not-yet-started)
// appointments, soonest first. This is a small, cheap, per-patient query with a
// GET /patients/:id/appointments
func (controller *Controller) GetPatientAppointments(c echo.Context) error {
	ctx, span := controller.Tracer.Start(c.Request().Context(), "GetPatientAppointments")
	defer span.End()

	startTime := time.Now()
	defer func() {
		library.Histogram(ctx, "patient_appointments.duration", "how long it takes to fetch a patient's upcoming appointments", startTime)
	}()

	patientID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || patientID <= 0 {
		return library.RespondRaw(c, http.StatusBadRequest, models.ErrorResponse{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "invalid patient id",
		})
	}

	// below is an example of a subquery - nested queries
	// select exist is outer query while select 1 is inner query
	// select 1 means just hand back number 1 in case the row with the id exists, it only cares about the row existing, not row content.
	// exists collapses whatever the result is to a simple boolean, yes or no
	var patientExists bool
	err = controller.DB.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM patients WHERE id = ?)", patientID).Scan(&patientExists)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error checking patient exists",
			constants.DATA:        patientID,
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

	rows, err := controller.DB.QueryContext(ctx,
		`SELECT id, doctor_id, start_time, end_time
		 FROM appointments
		 WHERE patient_id = ? AND status = ? AND start_time >= ?
		 ORDER BY start_time ASC`,
		patientID, constants.StatusBooked, time.Now().UTC())
	// time.Now().UTC() gives a full instant - year, month, day, hour, minute, second, all fused into one value.
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error fetching patient appointments",
			constants.DATA:        patientID,
		}).Error(err.Error())
		return library.RespondRaw(c, http.StatusInternalServerError, models.ErrorResponse{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "internal server error",
		})
	}
	defer rows.Close()

	appointments := make([]models.AppointmentResponse, 0)
	for rows.Next() {
		var id, doctorID int64
		var start, end time.Time
		if err := rows.Scan(&id, &doctorID, &start, &end); err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			logrus.WithContext(ctx).WithFields(logrus.Fields{
				constants.DESCRIPTION: "error scanning appointment row",
			}).Error(err.Error())
			continue
		}
		appointments = append(appointments, models.AppointmentResponse{
			ID:        id,
			DoctorID:  doctorID,
			PatientID: patientID,
			// start.UTC is just a defensive code mechanism, .Format just converts time into string, cause Json has no native way of dealing with date type
			StartTime: start.UTC().Format(time.RFC3339),
			EndTime:   end.UTC().Format(time.RFC3339),
			Status:    constants.StatusBooked,
		})
	}

	return library.RespondRaw(c, http.StatusOK, appointments)
}
