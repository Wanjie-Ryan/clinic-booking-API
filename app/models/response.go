package models

// a common generic error response across the service
type ErrorResponse struct {
	ErrorCode    int    `json:"error_code"`
	ErrorMessage string `json:"error_message"`
}

// also a common generic successResponse returned across the service
type SuccessResponse struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
}
