// Command server starts the song-similarity HTTP API on :8080.
package main

import (
	"context"
	"log"
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
	userAgent := os.Getenv("MB_USER_AGENT")
	if userAgent == "" {
		userAgent = defaultUserAgent
	}

	mbClient := provider.NewClient(userAgent)
	handlers := api.NewHandlers(mbClient)
	router := api.NewRouter(handlers)

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("song-similarity listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}
