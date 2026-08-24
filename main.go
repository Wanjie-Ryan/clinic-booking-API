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
	"github.com/sirupsen/logrus"
	"github.com/uptrace/opentelemetry-go-extra/otellogrus"
	"github.com/uptrace/uptrace-go/uptrace"
	"go.opentelemetry.io/otel"

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

	setup := os.Getenv("SETUP_TYPE")
	if setup == "" {
		setup = "all"
	}

	environment := os.Getenv("ENV")
	if environment == "" {
		environment = "dev"
	}

	ctx := context.Background()

	// Degrades to a no-op tracer automatically when UPTRACE_DSN is empty or
	// invalid -- confirmed against the SDK source, not assumed.
	uptrace.ConfigureOpentelemetry(
		uptrace.WithDSN(os.Getenv("UPTRACE_DSN")),
		uptrace.WithServiceName("clinic-booking-api"),
		uptrace.WithServiceVersion("v1.0.0"),
		uptrace.WithDeploymentEnvironment(environment),
		uptrace.WithMetricsEnabled(true),
		uptrace.WithTracingEnabled(true),
	)
	defer uptrace.Shutdown(ctx)

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
	dbWithoutName := database.DbInstanceWithoutDatabaseName()
	defer dbWithoutName.Close()

	dbName := os.Getenv("DATABASE_NAME")
	if _, err := dbWithoutName.Exec(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", dbName)); err != nil {
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error creating database",
		}).Fatal(err.Error())
	}

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
