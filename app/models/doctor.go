package models

import "time"

// will have the doctors and doctors working hours as well

type Doctor struct {
	ID      int64     `json:"id"`
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
}

type DoctorWorkingHours struct {
	ID        int64  `json:"id"`
	DoctorID  int64  `json:"doctor_id"`
	DayOfWeek int    `json:"day_of_week"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}
