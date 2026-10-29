// Command server starts the song-vibes-check HTTP API on :8080.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mindcapp/song-vibes-check/internal/api"
	"github.com/mindcapp/song-vibes-check/internal/cache"
	"github.com/mindcapp/song-vibes-check/internal/events"
	"github.com/mindcapp/song-vibes-check/internal/provider"
	"github.com/mindcapp/song-vibes-check/internal/tags"
	"github.com/mindcapp/song-vibes-check/internal/vectors"
)

const (
	defaultUserAgent        = "song-vibes-check/0.2.0 ( https://github.com/mindcapp/song-vibes-check )"
	defaultCacheTTL         = 24 * time.Hour
	defaultCacheLowDataTTL  = time.Hour
	defaultCacheArtistTTL   = 72 * time.Hour
	defaultRequestTimeout   = 30 * time.Second
	defaultCacheNegativeTTL = 10 * time.Minute
	defaultQdrantCollection = "tracks"
	// eventsFlushTimeout bounds how long shutdown waits for queued events.
	eventsFlushTimeout = 10 * time.Second
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
	// Deferred before the server starts, so it runs after srv.Shutdown has
	// let in-flight requests finish: their events are flushed, not dropped.
	publisher, closeEvents := withEvents(logger)
	defer closeEvents()

	handlers := api.NewHandlers(resolver, normalizer, requestTimeout).
		WithVectors(withVectors(logger)).
		WithEvents(publisher)

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

// withVectors connects the similar-track endpoints to Qdrant when QDRANT_URL
// is set. Without it, or with an invalid URL, those endpoints answer 503 and
// the rest of the service works as before. Qdrant is not contacted here: a
// database that is down only fails the requests that need it.
func withVectors(logger *slog.Logger) vectors.VectorStore {
	qdrantURL := os.Getenv("QDRANT_URL")
	if qdrantURL == "" {
		logger.Info("vector search disabled: QDRANT_URL is not set")
		return nil
	}
	collection := os.Getenv("QDRANT_COLLECTION")
	if collection == "" {
		collection = defaultQdrantCollection
	}
	var opts []vectors.Option
	if key := os.Getenv("QDRANT_API_KEY"); key != "" {
		opts = append(opts, vectors.WithAPIKey(key))
	}

	store, err := vectors.NewQdrant(qdrantURL, collection, opts...)
	if err != nil {
		logger.Warn("vector search disabled: invalid QDRANT_URL", "error", err.Error())
		return nil
	}
	logger.Info("vector search enabled", "collection", collection)
	return store
}

// withEvents connects POST /api/v1/events to Redpanda when REDPANDA_BROKERS
// (comma-separated host:port list) is set. Without it the endpoint answers
// 503 and the rest of the service works as before. The brokers are not
// contacted here. The returned function flushes queued events and must run
// after the HTTP server has stopped.
func withEvents(logger *slog.Logger) (events.Publisher, func()) {
	noop := func() {}

	var brokers []string
	for _, b := range strings.Split(os.Getenv("REDPANDA_BROKERS"), ",") {
		if b = strings.TrimSpace(b); b != "" {
			brokers = append(brokers, b)
		}
	}
	if len(brokers) == 0 {
		logger.Info("events disabled: REDPANDA_BROKERS is not set")
		return nil, noop
	}
	topic := os.Getenv("REDPANDA_TOPIC")
	if topic == "" {
		topic = events.DefaultTopic
	}

	pub, err := events.NewRedpanda(brokers, topic, events.DefaultBufferSize, logger)
	if err != nil {
		logger.Warn("events disabled: invalid configuration", "error", err.Error())
		return nil, noop
	}
	logger.Info("events enabled", "brokers", strings.Join(brokers, ","), "topic", topic)

	return pub, func() {
		ctx, cancel := context.WithTimeout(context.Background(), eventsFlushTimeout)
		defer cancel()
		if err := pub.Close(ctx); err != nil {
			logger.Warn("queued events were not all delivered before exit", "error", err.Error())
			return
		}
		logger.Info("events flushed")
	}
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
