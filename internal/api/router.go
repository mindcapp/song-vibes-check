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
	return metricsMiddleware(loggingMiddleware(logger, mux))
}
