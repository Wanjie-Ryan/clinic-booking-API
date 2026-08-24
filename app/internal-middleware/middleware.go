package internalmiddleware

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/github.com/labstack/echo/otelecho"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/time/rate"
)

func isHealthCheck(c echo.Context) bool {
	return c.Request().URL.Path == "/healthz"
}

// corsConfig builds the CORS policy from CORS_ALLOWED_ORIGINS.
func corsConfig() middleware.CORSConfig {
	origins := os.Getenv("CORS_ALLOWED_ORIGINS")
	if origins == "" {
		origins = "*"
	}

	return middleware.CORSConfig{
		AllowOrigins: strings.Split(origins, ","),
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodOptions},
	}
}

// rateLimiterConfig builds a per-IP in-memory rate limiter from RATE_LIMIT_*.
func rateLimiterConfig() middleware.RateLimiterConfig {
	rateVal, err := strconv.ParseFloat(os.Getenv("RATE_LIMIT_RATE"), 64)
	if err != nil || rateVal == 0 {
		rateVal = 20
	}

	burst, err := strconv.Atoi(os.Getenv("RATE_LIMIT_BURST"))
	if err != nil || burst == 0 {
		burst = 5
	}

	interval, err := strconv.Atoi(os.Getenv("RATE_LIMIT_INTERVAL"))
	if err != nil || interval == 0 {
		interval = 5
	}

	return middleware.RateLimiterConfig{
		Skipper: isHealthCheck,
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(
			middleware.RateLimiterMemoryStoreConfig{
				Rate:      rate.Limit(rateVal),
				Burst:     burst,
				ExpiresIn: time.Duration(interval) * time.Second,
			},
		),
		IdentifierExtractor: func(c echo.Context) (string, error) {
			return c.RealIP(), nil
		},
		ErrorHandler: func(c echo.Context, err error) error {
			return c.JSON(http.StatusForbidden, nil)
		},
		DenyHandler: func(c echo.Context, identifier string, err error) error {
			return c.JSON(http.StatusTooManyRequests, nil)
		},
	}
}

func timeoutConfig() middleware.TimeoutConfig {
	seconds, err := strconv.Atoi(os.Getenv("API_TIMEOUT_IN_SECONDS"))
	if err != nil || seconds == 0 {
		seconds = 30
	}

	return middleware.TimeoutConfig{
		Skipper: isHealthCheck,
		Timeout: time.Duration(seconds) * time.Second,
	}
}

// requestLogger logs one structured line per request: method, path, status,
// latency and the OpenTelemetry trace ID, so a request can be correlated with
// its trace. It deliberately does not log the request or response body -- a
// booking payload carries a patient's name, email and phone number, and there
// is no reason for that to sit in application logs.
func requestLogger(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if isHealthCheck(c) {
			return next(c)
		}

		start := time.Now()
		err := next(c)

		span := trace.SpanFromContext(c.Request().Context())
		traceID := span.SpanContext().TraceID().String()

		logrus.WithFields(logrus.Fields{
			"method":     c.Request().Method,
			"path":       c.Path(),
			"status":     c.Response().Status,
			"latency_ms": time.Since(start).Milliseconds(),
			"trace_id":   traceID,
		}).Info("request")

		return err
	}
}

// SetupMiddlewares wires the global middleware chain onto the Echo instance.
func SetupMiddlewares(tr trace.Tracer, e *echo.Echo) {
	deploymentName := strings.ReplaceAll(os.Getenv("DEPLOYMENT_NAME"), "-", "_")

	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	e.Use(middleware.BodyLimit("2M"))
	e.Use(middleware.CORSWithConfig(corsConfig()))
	e.Use(otelecho.Middleware(deploymentName))
	e.Use(middleware.RateLimiterWithConfig(rateLimiterConfig()))
	e.Use(middleware.Gzip())
	e.Use(requestLogger)
	e.Use(middleware.TimeoutWithConfig(timeoutConfig()))
}
