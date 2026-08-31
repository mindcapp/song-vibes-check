package api

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests processed, by method, path and status.",
	}, []string{"method", "path", "status"})

	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request duration in seconds, by method and path.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

type logAttrsKey struct{}

// addLogAttr attaches an extra field (e.g. artist, title) to the structured
// access-log line that loggingMiddleware writes once the request finishes.
func addLogAttr(ctx context.Context, attr slog.Attr) {
	if attrs, ok := ctx.Value(logAttrsKey{}).(*[]slog.Attr); ok {
		*attrs = append(*attrs, attr)
	}
}

// loggingMiddleware writes one structured (JSON) log line per request with
// timestamp, method, path, status, duration and remote address, plus any
// extra fields handlers attached via addLogAttr (e.g. artist/title on /compare).
func loggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		attrs := &[]slog.Attr{}
		ctx := context.WithValue(r.Context(), logAttrsKey{}, attrs)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(ctx))

		args := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_addr", r.RemoteAddr,
		}
		for _, a := range *attrs {
			args = append(args, a.Key, a.Value.Any())
		}
		logger.Info("request", args...)
	})
}

// metricsMiddleware records Prometheus counters/histograms for every request.
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		requestsTotal.WithLabelValues(r.Method, r.URL.Path, strconv.Itoa(rec.status)).Inc()
		requestDuration.WithLabelValues(r.Method, r.URL.Path).Observe(time.Since(start).Seconds())
	})
}
