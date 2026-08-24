package database

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

func RedisClient() *redis.Client {

	dbNumber, err := strconv.Atoi(os.Getenv("REDIS_DATABASE_NUMBER"))
	if err != nil {
		dbNumber = 0
	}

	opts := &redis.Options{
		Addr:         fmt.Sprintf("%s:%s", os.Getenv("REDIS_HOST"), os.Getenv("REDIS_PORT")),
		DB:           dbNumber,
		MinIdleConns: 5,
		PoolSize:     50,
		ReadTimeout:  3 * time.Second,
	}

	if password := os.Getenv("REDIS_PASSWORD"); password != "" {
		opts.Password = password
	}

	client := redis.NewClient(opts)

	if err := redisotel.InstrumentMetrics(client); err != nil {
		logrus.WithFields(logrus.Fields{"description": "error instrumenting redis metrics"}).Error(err.Error())
	}

	if err := redisotel.InstrumentTracing(client); err != nil {
		logrus.WithFields(logrus.Fields{"description": "error instrumenting redis tracing"}).Error(err.Error())
	}

	return client
}
