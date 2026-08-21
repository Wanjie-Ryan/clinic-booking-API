package models

import "time"

type Patient struct {
	ID      int64     `json:"id"`
	Name    string    `json:"name"`
	Email   string    `json:"email"`
	Phone   string    `json:"phone"`
	Created time.Time `json:"created"`
}
