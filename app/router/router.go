package router

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
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

	a.Controller = &controllers.Controller{
		DB:        a.DB,
		RedisConn: a.RedisConn,
		Tracer:    tr,
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
