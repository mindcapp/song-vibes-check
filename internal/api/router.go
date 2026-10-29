package api

import (
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func NewRouter(h *Handlers, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /compare", h.Compare)
	mux.HandleFunc("GET /health", h.Health)
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /api/v1/similar", h.Similar)
	mux.HandleFunc("GET /api/v1/tracks/search", h.SearchTracks)
	mux.HandleFunc("POST /api/v1/events", h.PostEvent)
	return metricsMiddleware(loggingMiddleware(logger, recoverMiddleware(logger, mux)))
}
