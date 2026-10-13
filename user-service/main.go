package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/joho/godotenv"

	"user-service/db"
	"user-service/handlers"
	"user-service/utils"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := godotenv.Load(); err != nil {
		logger.Info("no .env file found, relying on environment variables")
	}

	if err := utils.ValidateSecretKey(os.Getenv("SECRET_KEY")); err != nil {
		logger.Error("refusing to start with an unsafe SECRET_KEY",
			"error", err.Error(), "hint", "generate one with: openssl rand -hex 32")
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
