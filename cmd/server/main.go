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
	"song-similarity/internal/cache"
	"song-similarity/internal/provider"
	"song-similarity/internal/tags"
)

const (
	defaultUserAgent        = "song-similarity/0.1.0 ( https://example.com/song-similarity )"
	defaultCacheTTL         = 24 * time.Hour
	defaultCacheLowDataTTL  = time.Hour
	defaultCacheNegativeTTL = 10 * time.Minute
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	userAgent := os.Getenv("MB_USER_AGENT")
	if userAgent == "" {
		userAgent = defaultUserAgent
	}

	normalizer, err := tags.Load(os.Getenv("TAG_STOPLIST_FILE"))
	if err != nil {
		logger.Error("failed to load tag configuration", "error", err.Error())
		os.Exit(1)
	}

	searcher, closeCache := withCache(provider.NewClient(userAgent), logger)
	defer closeCache()

	handlers := api.NewHandlers(searcher, normalizer)
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

// withCache wraps the searcher with a Redis cache when REDIS_URL is set. Any
// problem with the cache configuration or with Redis itself only disables or
// degrades caching; it never stops the service from starting.
func withCache(searcher api.TrackSearcher, logger *slog.Logger) (api.TrackSearcher, func()) {
	noop := func() {}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		logger.Info("cache disabled: REDIS_URL is not set")
		return searcher, noop
	}

	store, err := cache.NewRedisStore(redisURL, logger)
	if err != nil {
		logger.Warn("cache disabled: invalid REDIS_URL", "error", err.Error())
		return searcher, noop
	}

	cfg := cache.Config{
		TTL:         durationEnv(logger, "CACHE_TTL", defaultCacheTTL),
		LowDataTTL:  durationEnv(logger, "CACHE_LOW_DATA_TTL", defaultCacheLowDataTTL),
		NegativeTTL: durationEnv(logger, "CACHE_NEGATIVE_TTL", defaultCacheNegativeTTL),
	}

	pingCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := store.Ping(pingCtx); err != nil {
		logger.Warn("redis is not reachable at startup, will keep retrying", "error", err.Error())
	} else {
		logger.Info("cache enabled", "ttl", cfg.TTL.String(),
			"low_data_ttl", cfg.LowDataTTL.String(), "negative_ttl", cfg.NegativeTTL.String())
	}

	closeStore := func() {
		if err := store.Close(); err != nil {
			logger.Warn("closing redis client failed", "error", err.Error())
		}
	}
	return cache.NewSearcher(searcher, store, cfg, logger), closeStore
}

// durationEnv reads a Go duration ("24h", "90m") from the environment,
// falling back to def when the variable is unset or not a positive duration.
func durationEnv(logger *slog.Logger, name string, def time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		logger.Warn("ignoring invalid duration, using default", "variable", name, "value", raw, "default", def.String())
		return def
	}
	return d
}
