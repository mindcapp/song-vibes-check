// Command server starts the song-similarity HTTP API on :8080.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"song-similarity/internal/api"
	"song-similarity/internal/provider"
)

const defaultUserAgent = "song-similarity/0.1.0 ( https://example.com/song-similarity )"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	userAgent := os.Getenv("MB_USER_AGENT")
	if userAgent == "" {
		userAgent = defaultUserAgent
	}

	mbClient := provider.NewClient(userAgent)
	handlers := api.NewHandlers(mbClient)
	router := api.NewRouter(handlers, logger)

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("starting server", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err.Error())
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "error", err.Error())
	}
}
