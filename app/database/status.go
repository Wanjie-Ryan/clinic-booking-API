package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"

	"github.com/redis/go-redis/v9"
)

// CheckConnectionStatus pings MySQL and Redis and returns an HTTP status code
// plus a per-dependency message, used by the /healthz endpoint.
func CheckConnectionStatus(ctx context.Context, db *sql.DB, redisClient *redis.Client) (int, map[string]string) {
	result := make(map[string]string)
	status := http.StatusOK

	if err := db.PingContext(ctx); err != nil {
		result["database"] = fmt.Sprintf("error: %s", err.Error())
		status = http.StatusInternalServerError
	} else {
		result["database"] = "ok"
	}

	if _, err := redisClient.Ping(ctx).Result(); err != nil {
		result["redis"] = fmt.Sprintf("error: %s", err.Error())
		status = http.StatusInternalServerError
	} else {
		result["redis"] = "ok"
	}

	return status, result
}
