package library

import (
	"context"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var meter = otel.Meter(strings.ReplaceAll(os.Getenv("DEPLOYMENT_NAME"), "-", "_"))

// histogram that records how long an ops took in ms
func Histogram(ctx context.Context, name, description string, start time.Time) {
	recorder, _ := meter.Int64Histogram(name, metric.WithUnit("milliseconds"), metric.WithDescription(description))
	recorder.Record(ctx, int64(time.Since(start).Milliseconds()))
}

// countergraph increments a name counter, used for error/event counts
func CounterGraph(ctx context.Context, name, description string, count int64) {
	counter, _ := meter.Int64Counter(name, metric.WithUnit("1"), metric.WithDescription(description))
	counter.Add(ctx, count)
}
