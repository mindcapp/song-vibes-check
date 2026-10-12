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
	defaultUserAgent        = "song-vibes-check/0.2.0 ( https://github.com/mindcapp/song-vibes-check )"
	defaultCacheTTL         = 24 * time.Hour
	defaultCacheLowDataTTL  = time.Hour
	defaultCacheArtistTTL   = 72 * time.Hour
	defaultRequestTimeout   = 30 * time.Second
	defaultCacheNegativeTTL = 10 * time.Minute
)

func main() {
	os.Exit(run())
}

// run starts the server and blocks until it stops. It returns the process
// exit code; keeping os.Exit out of it lets deferred cleanup run.
func run() int {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	userAgent := os.Getenv("MB_USER_AGENT")
	if userAgent == "" {
		userAgent = defaultUserAgent
	}

	normalizer, err := tags.Load(os.Getenv("TAG_STOPLIST_FILE"))
	if err != nil {
		logger.Error("failed to load tag configuration", "error", err.Error())
		return 1
	}

	tracks, artists, closeCache := withCache(provider.NewClient(userAgent), logger)
	defer closeCache()

	resolver := provider.NewResolver(tracks, artists, func(names []string) int {
		return len(normalizer.NormalizeAll(names))
	})
	requestTimeout := durationEnv(logger, "REQUEST_TIMEOUT", defaultRequestTimeout)
	handlers := api.NewHandlers(resolver, normalizer, requestTimeout)

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           api.NewRouter(handlers, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		// Must outlast the handler's own deadline, or the connection would be
		// cut before the 502 it produces could be written.
		WriteTimeout:   requestTimeout + 10*time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 16 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("starting server", "addr", srv.Addr, "request_timeout", requestTimeout.String())
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		logger.Error("server error", "error", err.Error())
		return 1
	case <-ctx.Done():
	}
	stop() // a second signal now kills the process the default way

	// In-flight requests get their full deadline to finish.
	logger.Info("shutting down", "grace_period", (requestTimeout + 5*time.Second).String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), requestTimeout+5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err.Error())
		return 1
	}
	logger.Info("server stopped")
	return 0
}

// withCache wraps the MusicBrainz client with Redis caches when REDIS_URL is
// set. Any problem with the cache configuration or with Redis itself only
// disables or degrades caching; it never stops the service from starting.
func withCache(client *provider.Client, logger *slog.Logger) (provider.TrackSearcher, provider.ArtistTagger, func()) {
	noop := func() {}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		logger.Info("cache disabled: REDIS_URL is not set")
		return client, client, noop
	}

	store, err := cache.NewRedisStore(redisURL, logger)
	if err != nil {
		logger.Warn("cache disabled: invalid REDIS_URL", "error", err.Error())
		return client, client, noop
	}

	cfg := cache.Config{
		TTL:         durationEnv(logger, "CACHE_TTL", defaultCacheTTL),
		LowDataTTL:  durationEnv(logger, "CACHE_LOW_DATA_TTL", defaultCacheLowDataTTL),
		NegativeTTL: durationEnv(logger, "CACHE_NEGATIVE_TTL", defaultCacheNegativeTTL),
		ArtistTTL:   durationEnv(logger, "CACHE_ARTIST_TTL", defaultCacheArtistTTL),
	}

	pingCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := store.Ping(pingCtx); err != nil {
		logger.Warn("redis is not reachable at startup, will keep retrying", "error", err.Error())
	} else {
		logger.Info("cache enabled", "ttl", cfg.TTL.String(), "low_data_ttl", cfg.LowDataTTL.String(),
			"negative_ttl", cfg.NegativeTTL.String(), "artist_ttl", cfg.ArtistTTL.String())
	}

	closeStore := func() {
		if err := store.Close(); err != nil {
			logger.Warn("closing redis client failed", "error", err.Error())
		}
	}
	return cache.NewSearcher(client, store, cfg, logger), cache.NewArtistTags(client, store, cfg, logger), closeStore
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
