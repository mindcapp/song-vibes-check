package vectors

import (
	"context"
	"errors"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Values of the "operation" label.
const (
	opRetrieve = "retrieve"
	opQuery    = "query"
	opScroll   = "scroll"
)

var (
	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "qdrant_request_duration_seconds",
		Help:    "Duration of one HTTP request to Qdrant, by operation.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
	}, []string{"operation"})

	errorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "qdrant_errors_total",
		Help: "Failed HTTP requests to Qdrant, by operation and reason.",
	}, []string{"operation", "reason"})
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
	case errors.As(err, &upstream) && upstream.Status == 0:
		return "network"
	case errors.As(err, &upstream):
		return "status_" + strconv.Itoa(upstream.Status/100) + "xx"
	default:
		return "bad_response"
	}
}
