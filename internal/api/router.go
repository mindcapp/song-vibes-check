package api

import "net/http"

// NewRouter builds the HTTP router for the service.
func NewRouter(h *Handlers) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /compare", h.Compare)
	return mux
}
