package controllers

import (
	"database/sql"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/trace"
)

// Controller holds every dependency a handler needs. One instance is created
// at startup and shared across all requests.
type Controller struct {
	DB             *sql.DB
	RedisConn      *redis.Client
	Tracer         trace.Tracer
	ClinicLocation *time.Location
	//slotduration is the length of one bookable appointment slot
	SlotDuration time.Duration
	// MinimumLeadTime is how far in advance a booking must start from now
	MinimumLeadTime time.Duration
}
