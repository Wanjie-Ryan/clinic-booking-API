package router

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/trace"

	"github.com/Wanjie-Ryan/clinic-booking-API/app/controllers"
	"github.com/Wanjie-Ryan/clinic-booking-API/app/database"
	internal_middleware "github.com/Wanjie-Ryan/clinic-booking-API/app/internal-middleware"
)

// App wires together the HTTP server and its dependencies.
type App struct {
	DB         *sql.DB
	E          *echo.Echo
	RedisConn  *redis.Client
	Tracer     trace.Tracer
	Controller *controllers.Controller
}

// Initialize sets up the database pool, Redis client, controller, middleware
// chain and routes.
func (a *App) Initialize(tr trace.Tracer, ctx context.Context) {
	a.Tracer = tr
	a.DB = database.DbInstance()
	a.RedisConn = database.RedisClient()

	// necessary cause Rails servers are not in kenya, so if I were to use the servers local time, instead of converting the code would be returning wrong answers silently
	loc, err := time.LoadLocation(os.Getenv("CLINIC_TIMEZONE"))
	if err != nil {
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			"description": "invalid CLINIC_TIMEZONE, falling back to UTC",
		}).Warn(err.Error())
		loc = time.UTC
	}

	slotMinutes, err := strconv.Atoi(os.Getenv("SLOT_DURATION_MINUTES"))
	if err != nil || slotMinutes <= 0 {
		slotMinutes = 30
	}

	// patient can't book sth starting less than 60 minutes from now.
	// enforced on both write path (validateslot - rejects it) and readPath (availableslots - filters it out of whats even offered), nothing inconsistent slips through.
	leadMinutes, err := strconv.Atoi(os.Getenv("BOOKING_MINIMUM_LEAD_MINUTES"))
	if err != nil || leadMinutes < 0 {
		leadMinutes = 60
	}

	// computed slot list sits in Redis for 60s before being stale
	availabilityCacheSeconds, err := strconv.Atoi(os.Getenv("AVAILABILITY_CACHE_TTL_SECONDS"))
	if err != nil || availabilityCacheSeconds <= 0 {
		availabilityCacheSeconds = 60
	}

	// how long retry keys stays valid for replay protection
	idempotencyKeySeconds, err := strconv.Atoi(os.Getenv("IDEMPOTENCY_KEY_TTL_SECONDS"))
	if err != nil || idempotencyKeySeconds <= 0 {
		idempotencyKeySeconds = 86400
	}

	// every other file downstream gets to read from this, they can't modify or access the env themselves.
	a.Controller = &controllers.Controller{
		DB:                   a.DB,
		RedisConn:            a.RedisConn,
		Tracer:               tr,
		ClinicLocation:       loc,
		SlotDuration:         time.Duration(slotMinutes) * time.Minute,
		MinimumLeadTime:      time.Duration(leadMinutes) * time.Minute,
		AvailabilityCacheTTL: time.Duration(availabilityCacheSeconds) * time.Second,
		IdempotencyKeyTTL:    time.Duration(idempotencyKeySeconds) * time.Second,
	}

	a.E = echo.New()
	a.E.HideBanner = true
	internal_middleware.SetupMiddlewares(tr, a.E)

	a.setupHealthCheck()
	a.setRouters()
}

func (a *App) setupHealthCheck() {
	a.E.GET("/healthz", a.GetHealth)
}

// GetHealth reports MySQL and Redis connectivity.
func (a *App) GetHealth(c echo.Context) error {
	status, result := database.CheckConnectionStatus(c.Request().Context(), a.DB, a.RedisConn)
	return c.JSON(status, result)
}

// setRouters registers the business endpoints. Populated in Phase 4.
func (a *App) setRouters() {
	a.E.GET("/doctors/:id/availability", a.Controller.GetDoctorAvailability)
	a.E.POST("/appointments", a.Controller.BookAppointment)
	a.E.PATCH("/appointments/:id/cancel", a.Controller.CancelAppointment)
	a.E.PATCH("/appointments/:id/reschedule", a.Controller.RescheduleAppointment)
	a.E.GET("/patients/:id/appointments", a.Controller.GetPatientAppointments)
}

// Run starts the HTTP server.
func (a *App) Run() {
	host := os.Getenv("SYSTEM_HOST")

	port := os.Getenv("PORT")
	if port == "" {
		port = os.Getenv("SYSTEM_PORT")
		if port == "" {
			port = "8080"
		}
	}

	addr := fmt.Sprintf("%s:%s", host, port)
	log.Printf("listening on %s", addr)
	a.E.Logger.Fatal(a.E.Start(addr))
}
