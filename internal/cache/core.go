package cache

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	// DefaultCooldown is how long the cache is bypassed after a store error.
	DefaultCooldown = 30 * time.Second
	// DefaultOpTimeout bounds a single cache read or write.
	DefaultOpTimeout = 150 * time.Millisecond
	// DefaultLowDataTTL is the default for Config.LowDataTTL.
	DefaultLowDataTTL = time.Hour
	// DefaultArtistTTL is the default for Config.ArtistTTL.
	DefaultArtistTTL = 72 * time.Hour

	// LowDataTags is the number of tags below which a result is considered
	// thinly tagged and is cached for Config.LowDataTTL only.
	LowDataTags = 3
)

// Names of the two caches, used as the "cache" label on the metrics.
const (
	kindTrack  = "track"
	kindArtist = "artist"
)

var (
	hitsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "cache_hits_total",
		Help: "Lookups answered from the cache.",
	}, []string{"cache"})
	missesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "cache_misses_total",
		Help: "Lookups that were not cached and started an upstream request.",
	}, []string{"cache"})
	coalescedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "cache_coalesced_total",
		Help: "Lookups that were not cached and joined an upstream request already in progress.",
	}, []string{"cache"})
	errorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "cache_errors_total",
		Help: "Cache store errors (the lookup falls back to MusicBrainz).",
	}, []string{"cache"})
)

// Config tunes the caches.
type Config struct {
	// TTL is how long a well-tagged track stays cached.
	TTL time.Duration
	// LowDataTTL is how long a result with fewer than LowDataTags tags stays
	// cached. Such results are the ones most likely to improve as MusicBrainz
	// users add tags. Zero means DefaultLowDataTTL.
	LowDataTTL time.Duration
	// NegativeTTL is how long "not found" stays cached. Kept short because
	// MusicBrainz data is crowd-sourced and the song may be added.
	NegativeTTL time.Duration
	// ArtistTTL is how long an artist's tags stay cached. Artist tags change
	// slowly and are shared by all of the artist's songs.
	// Zero means DefaultArtistTTL.
	ArtistTTL time.Duration
	// Cooldown is how long the cache is bypassed after a store error, so an
	// unreachable Redis costs one timeout rather than one per request.
	// Zero means DefaultCooldown.
	Cooldown time.Duration
	// OpTimeout is the hard deadline for one cache read or write.
	// Zero means DefaultOpTimeout.
	OpTimeout time.Duration
}

func (cfg Config) withDefaults() Config {
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = DefaultCooldown
	}
	if cfg.OpTimeout <= 0 {
		cfg.OpTimeout = DefaultOpTimeout
	}
	if cfg.LowDataTTL <= 0 {
		cfg.LowDataTTL = DefaultLowDataTTL
	}
	if cfg.ArtistTTL <= 0 {
		cfg.ArtistTTL = DefaultArtistTTL
	}
	return cfg
}

// core is the store access shared by the caches: per-operation deadlines and
// the bypass window that keeps a broken store from slowing lookups down.
// Store failures never surface to the caller; they are logged, counted and
// reported as a miss. core is safe for concurrent use and must not be copied.
type core struct {
	kind   string
	store  Store
	cfg    Config
	logger *slog.Logger
	now    func() time.Time

	// bypassUntil is a unix-nano timestamp; zero means the store is healthy.
	// It is only touched through atomic operations.
	bypassUntil atomic.Int64
}

func (c *core) init(kind string, store Store, cfg Config, logger *slog.Logger) {
	c.kind, c.store, c.cfg, c.logger, c.now = kind, store, cfg.withDefaults(), logger, time.Now

	// Create every series at zero. A counter that has never been incremented
	// is otherwise absent, and an expression such as
	// hits / (hits + misses + coalesced) is empty while any term is absent.
	for _, counter := range []*prometheus.CounterVec{hitsTotal, missesTotal, coalescedTotal, errorsTotal} {
		counter.WithLabelValues(kind)
	}
}

// get returns the cached value for key; found is false on a miss, on a store
// error and while the store is bypassed.
func (c *core) get(ctx context.Context, key string) (raw []byte, found bool) {
	if c.bypassed() {
		return nil, false
	}

	opCtx, cancel := context.WithTimeout(ctx, c.cfg.OpTimeout)
	raw, found, err := c.store.Get(opCtx, key)
	cancel()
	if err != nil {
		c.fail(ctx, "get", err)
		return nil, false
	}
	c.recovered()
	return raw, found
}

func (c *core) set(ctx context.Context, key string, raw []byte, ttl time.Duration) {
	if ttl <= 0 || c.bypassed() {
		return
	}

	opCtx, cancel := context.WithTimeout(ctx, c.cfg.OpTimeout)
	defer cancel()

	if err := c.store.Set(opCtx, key, raw, ttl); err != nil {
		c.fail(ctx, "set", err)
	}
}

func (c *core) bypassed() bool {
	until := c.bypassUntil.Load()
	return until != 0 && c.now().UnixNano() < until
}

// fail records a store error and opens the bypass window. ctx is the context
// the operation was started from (not the per-operation deadline): if that
// one has ended, the error says nothing about Redis and is ignored.
func (c *core) fail(ctx context.Context, op string, err error) {
	if ctx.Err() != nil {
		return
	}
	errorsTotal.WithLabelValues(c.kind).Inc()
	if c.bypassUntil.Swap(c.now().Add(c.cfg.Cooldown).UnixNano()) == 0 {
		c.logger.Warn("cache unavailable, serving without it",
			"cache", c.kind, "op", op, "error", err.Error(), "retry_in", c.cfg.Cooldown.String())
	}
}

func (c *core) recovered() {
	if c.bypassUntil.Swap(0) != 0 {
		c.logger.Info("cache is available again", "cache", c.kind)
	}
}

// countJoins returns the flight callback that feeds the miss and coalesced
// counters: only the caller that starts an upstream request is a miss.
func (c *core) countJoins() func(leader bool) {
	return func(leader bool) {
		if leader {
			missesTotal.WithLabelValues(c.kind).Inc()
		} else {
			coalescedTotal.WithLabelValues(c.kind).Inc()
		}
	}
}

func (c *core) logPanic(key string, recovered any, stack []byte) {
	c.logger.Error("cached lookup panicked",
		"cache", c.kind, "key", key, "panic", recovered, "stack", string(stack))
}
