package controllers

import (
	"database/sql"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/trace"
)

// Controller holds every dependency a handler needs. One instance is created
// at startup and shared across all requests.
type Controller struct {
	DB        *sql.DB
	RedisConn *redis.Client
	Tracer    trace.Tracer
}
