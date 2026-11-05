package analytics

import (
	"context"
	"errors"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	requestDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "clickhouse_request_duration_seconds",
		Help:    "Duration of one HTTP request to ClickHouse for transition statistics.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
	})

	errorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "clickhouse_errors_total",
		Help: "Failed HTTP requests to ClickHouse, by reason.",
	}, []string{"reason"})
)

// errorReason names a failed request for the "reason" label. The set of
// values is small and fixed so the metric cannot grow without bound.
func errorReason(err error) string {
	var upstream *UpstreamError
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, errBadResponse):
		return "bad_response"
	case errors.As(err, &upstream) && upstream.Status == 0 && upstream.Err != nil:
		return "network"
	case errors.As(err, &upstream) && upstream.Status != 0:
		return "status_" + strconv.Itoa(upstream.Status/100) + "xx"
	default:
		return "bad_response"
	}
}
