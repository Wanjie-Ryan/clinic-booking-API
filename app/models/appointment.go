package models

import "time"

// status is one of constants.StatusBooked or constants.StatusCancelled

type Appointment struct {
	ID                 int64     `json:"id"`
	DoctorID           int64     `json:"doctor_id"`
	PatientID          int64     `json:"patient_id"`
	StartTime          time.Time `json:"start_time"`
	EndTime            time.Time `json:"end_time"`
	Status             string    `json:"status"`
	CancellationReason *string   `json:"cancellation_reason,omitempty"`
	Created            time.Time `json:"created"`
}
