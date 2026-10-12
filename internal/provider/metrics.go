package provider

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Values of the "endpoint" label.
const (
	endpointSearch = "recording_search"
	endpointArtist = "artist_lookup"
)

var (
	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "musicbrainz_request_duration_seconds",
		Help: "Duration of one HTTP request to MusicBrainz, by endpoint. " +
			"Excludes time spent waiting for the rate limiter.",
		Buckets: []float64{0.1, 0.25, 0.5, 0.75, 1, 1.5, 2, 3, 5, 10},
	}, []string{"endpoint"})

	errorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "musicbrainz_errors_total",
		Help: "Failed HTTP requests to MusicBrainz, by endpoint and reason. " +
			"Counts every attempt, including ones that were retried successfully.",
	}, []string{"endpoint", "reason"})

	retriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "musicbrainz_retries_total",
		Help: "Requests to MusicBrainz that were sent a second time after a 503 or a network error.",
	}, []string{"endpoint"})
)

// errorReason names a failed attempt for the "reason" label. The set of
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
	case errors.As(err, &upstream) && upstream.Status == http.StatusServiceUnavailable:
		return "status_503"
	case errors.As(err, &upstream) && upstream.Status >= 500:
		return "status_5xx"
	case errors.As(err, &upstream):
		return "status_" + strconv.Itoa(upstream.Status/100) + "xx"
	default:
		return "bad_response"
	}
}
