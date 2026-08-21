package constants

const (
	DESCRIPTION = "description"
	DATA        = "data"
)

const (
	StatusBooked    = "booked"
	StatusCancelled = "cancelled"
)

// expected MYSQL DuplicateEntryErrorCode so that i can map it to 409 rather than 500
const MySQLDuplicateEntryErrorCode = 1062
