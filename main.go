package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	_ "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	migratemysql "github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/joho/godotenv"
	"github.com/sirupsen/logrus"
	"github.com/uptrace/opentelemetry-go-extra/otellogrus"
	"github.com/uptrace/uptrace-go/uptrace"
	"go.opentelemetry.io/otel"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/Wanjie-Ryan/clinic-booking-API/app/constants"
	"github.com/Wanjie-Ryan/clinic-booking-API/app/database"
	"github.com/Wanjie-Ryan/clinic-booking-API/app/router"
)

func logsInit() {
	logrus.SetFormatter(&logrus.JSONFormatter{})
	logrus.AddHook(otellogrus.NewHook(otellogrus.WithLevels(
		logrus.PanicLevel,
		logrus.FatalLevel,
		logrus.ErrorLevel,
		logrus.WarnLevel,
		logrus.InfoLevel,
	)))
}

func main() {
	logsInit()

	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, relying on real environment variables")
	}
	setup := os.Getenv("SETUP_TYPE")
	if setup == "" {
		setup = "all"
	}

	environment := os.Getenv("ENV")
	if environment == "" {
		environment = "dev"
	}

	ctx := context.Background()

	// Second metric reader: a pull-based Prometheus exporter that registers into
	// client_golang's default registry (served at GET /metrics). It runs alongside
	// Uptrace's OTLP push reader, so both backends get the same instruments.
	promExporter, err := otelprom.New()
	if err != nil {
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error creating prometheus exporter",
		}).Fatal(err.Error())
	}

	uptrace.ConfigureOpentelemetry(
		uptrace.WithDSN(os.Getenv("UPTRACE_DSN")),
		uptrace.WithServiceName("clinic-booking-api"),
		uptrace.WithServiceVersion("v1.0.0"),
		uptrace.WithDeploymentEnvironment(environment),
		uptrace.WithMetricsEnabled(true),
		uptrace.WithTracingEnabled(true),
		uptrace.WithMetricOption(sdkmetric.WithReader(promExporter)),
	)
	defer uptrace.Shutdown(ctx)

	// uptrace-go returns early ("Uptrace is disabled") on an empty/invalid DSN and
	// never builds a MeterProvider, so the reader passed via WithMetricOption above
	// would never be attached and /metrics would stay empty. In that case build our
	// own provider with just the Prometheus reader.
	if _, ok := otel.GetMeterProvider().(*sdkmetric.MeterProvider); !ok {
		otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(promExporter)))
	}

	tracer := otel.Tracer("clinic-booking-api")
	ctx, mainSpan := tracer.Start(ctx, "main")
	defer mainSpan.End()

	if setup == "migrate" {
		runMigrations(ctx)
		return
	}

	runMigrations(ctx)

	var a router.App
	a.Initialize(tracer, ctx)
	a.Run()
}

// runMigrations creates the database if it doesn't exist yet and applies every
// pending migration in migrations/. Used as the Railway pre-deploy command
// (SETUP_TYPE=migrate) so schema changes land before the new app version
// starts serving traffic.
func runMigrations(ctx context.Context) {
	// opens a MySQL connection without selecting a specific DB; ie. connects to the server itself, not to clinic.
	dbWithoutName := database.DbInstanceWithoutDatabaseName()
	defer dbWithoutName.Close()

	dbName := os.Getenv("DATABASE_NAME")
	if _, err := dbWithoutName.Exec(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", dbName)); err != nil {
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error creating database",
		}).Fatal(err.Error())
	}

	// opens a second and separate connection, this time to the now existing clinic DB. This is the one migrations actually run against.
	db := database.DbInstance()
	defer db.Close()

	driver, err := migratemysql.WithInstance(db, &migratemysql.Config{})
	if err != nil {
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error creating migration driver",
		}).Fatal(err.Error())
	}

	m, err := migrate.NewWithDatabaseInstance(fmt.Sprintf("file://%s/migrations", rootPath()), "mysql", driver)
	if err != nil {
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error initializing migrator",
		}).Fatal(err.Error())
	}

	// ErrNoChange means every migration was already applied -- that is a
	// successful no-op on redeploy, not a failure.
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "migration failed",
		}).Fatal(err.Error())
	}

	log.Println("migrations applied")
}

// rootPath returns the directory this file lives in, so migrations/ resolves
// correctly regardless of the working directory the binary is started from.
func rootPath() string {
	_, b, _, _ := runtime.Caller(0)
	return filepath.Dir(b)
}
