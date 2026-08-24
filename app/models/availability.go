package models

// AvailabilityResponse is the response body for GET /doctors/{id}/availability.
type AvailabilityResponse struct {
	DoctorID int64    `json:"doctor_id"`
	Date     string   `json:"date"`
	Slots    []string `json:"available_slots"`
}
