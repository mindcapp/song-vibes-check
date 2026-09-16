package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/joho/godotenv"

	"user-service/db"
	"user-service/handlers"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := godotenv.Load(); err != nil {
		logger.Info("no .env file found, relying on environment variables")
	}

	if os.Getenv("SECRET_KEY") == "" {
		logger.Error("SECRET_KEY environment variable is required")
		os.Exit(1)
	}

	database, err := db.Init()
	if err != nil {
		logger.Error("failed to initialize database", "error", err)
		os.Exit(1)
	}

	store := handlers.NewGormStore(database)
	h := handlers.New(store, logger)

	mux := http.NewServeMux()
	mux.Handle("POST /register", handlers.RateLimit(5, 10, http.HandlerFunc(h.Register)))
	mux.Handle("POST /login", handlers.RateLimit(5, 10, http.HandlerFunc(h.Login)))
	mux.Handle("GET /me", handlers.AuthMiddleware(http.HandlerFunc(h.GetMe)))
	mux.Handle("POST /history", handlers.AuthMiddleware(http.HandlerFunc(h.AddHistory)))
	mux.Handle("GET /history", handlers.AuthMiddleware(http.HandlerFunc(h.GetHistory)))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	addr := ":8002"
	logger.Info("user-service listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
