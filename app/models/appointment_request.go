package models

// BookAppointmentRequest is the payload for POST /appointments.
type BookAppointmentRequest struct {
	DoctorID  int64  `json:"doctor_id"`
	PatientID int64  `json:"patient_id"`
	StartTime string `json:"start_time"`
}

// AppointmentResponse is returned for a successfully booked, cancelled, or
// rescheduled appointment.
type AppointmentResponse struct {
	ID                 int64   `json:"id"`
	DoctorID           int64   `json:"doctor_id"`
	PatientID          int64   `json:"patient_id"`
	StartTime          string  `json:"start_time"`
	EndTime            string  `json:"end_time"`
	Status             string  `json:"status"`
	CancellationReason *string `json:"cancellation_reason,omitempty"`
}
