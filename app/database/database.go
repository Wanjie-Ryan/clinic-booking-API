package database

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/uptrace/opentelemetry-go-extra/otelsql"
	semconv "go.opentelemetry.io/otel/semconv/v1.10.0"
)

// dsn builds MySQL connection string for the given DB name. loc = UTC

func dsn(dbName string) string {
	return fmt.Sprintf(
		"%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&collation=utf8mb4_0900_ai_ci&parseTime=true&loc=UTC&multiStatements=true",
		os.Getenv("DATABASE_USERNAME"),
		os.Getenv("DATABASE_PASSWORD"),
		os.Getenv("DATABASE_HOST"),
		os.Getenv("DATABASE_PORT"),
		dbName,
	)
}

// DbInstance returns the application's MySQL, wrapped with openTelemtry instrumentationso every query gets a span and shows up in the otelsql DB stats metrics

func DbInstance() *sql.DB {
	dbName := os.Getenv("DATABASE_NAME")

	db, err := otelsql.Open("mysql", dsn(dbName),
		otelsql.WithAttributes(semconv.DBSystemMySQL),
		otelsql.WithDBName(dbName))

	if err != nil {
		logrus.WithFields(logrus.Fields{"description": "error opening DB connection"}).Fatal(err.Error())
	}

	idleConns, err := strconv.Atoi(os.Getenv("DATABASE_IDLE_CONNECTION"))

	if err != nil {
		idleConns = 10
	}

	maxConns, err := strconv.Atoi(os.Getenv("DATABASE_MAX_CONNECTION"))

	if err != nil {
		maxConns = 50
	}

	connLifetime, err := strconv.Atoi(os.Getenv("DATABASE_CONNECTION_LIFETIME"))

	if err != nil {
		connLifetime = 300
	}

	db.SetMaxIdleConns(idleConns)
	db.SetMaxOpenConns(maxConns)
	db.SetConnMaxLifetime(time.Duration(connLifetime) * time.Second)
	db.SetConnMaxIdleTime(time.Duration(connLifetime) * time.Second)

	if err := db.Ping(); err != nil {
		logrus.WithFields(logrus.Fields{"description": "error pinging database"}).Fatal(err.Error())
	}

	otelsql.ReportDBStatsMetrics(db)

	return db

}

// DbInstanceWithoutDatabaseName connects to the MySQL server without selecting
// a database. Used once at startup to run CREATE DATABASE IF NOT EXISTS before
// DbInstance connects to a database that might not exist yet.
func DbInstanceWithoutDatabaseName() *sql.DB {
	db, err := sql.Open("mysql", dsn(""))
	if err != nil {
		logrus.WithFields(logrus.Fields{"description": "error opening database connection"}).Fatal(err.Error())
	}
	return db
}
