package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
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

// metricPath maps a request path to the value of the "path" label. Anything
// that is not one of the service's routes becomes "other": the label would
// otherwise take every URL a scanner tries and grow without bound.
func metricPath(path string) string {
	switch path {
	case "/compare", "/health", "/metrics", "/api/v1/similar", "/api/v1/tracks/search",
		"/api/v1/events":
		return path
	default:
		return "other"
	}
}

// metricsMiddleware records Prometheus counters/histograms for every request.
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		path := metricPath(r.URL.Path)
		requestsTotal.WithLabelValues(r.Method, path, strconv.Itoa(rec.status)).Inc()
		requestDuration.WithLabelValues(r.Method, path).Observe(time.Since(start).Seconds())
	})
}

// recoverMiddleware turns a panic in a handler into a logged 500 instead of
// letting it kill the connection silently. The log line carries the stack.
func recoverMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			if recovered == http.ErrAbortHandler {
				panic(recovered) // net/http's own signal to abort the response
			}

			logger.Error("handler panicked",
				"method", r.Method, "path", r.URL.Path,
				"panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
			addLogAttr(r.Context(), slog.String("error", "internal error: handler panicked"))
			writeError(w, http.StatusInternalServerError, "internal error")
		}()

		next.ServeHTTP(w, r)
	})
}
