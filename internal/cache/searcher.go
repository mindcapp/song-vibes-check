package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"song-similarity/internal/provider"
)

const (
	// keyPrefix carries a version that is bumped whenever the meaning of a
	// cached track changes. v2: tags are merged from several recordings.
	keyPrefix = "track:v2:"
	// LowDataTags is the number of tags below which a track is considered
	// thinly tagged and is cached for Config.LowDataTTL only.
	LowDataTags = 3
	// DefaultLowDataTTL is the default for Config.LowDataTTL.
	DefaultLowDataTTL = time.Hour
	// DefaultCooldown is how long the cache is bypassed after a store error.
	DefaultCooldown = 30 * time.Second
	// DefaultOpTimeout bounds a single cache read or write.
	DefaultOpTimeout = 150 * time.Millisecond
)

var (
	hitsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "cache_hits_total",
		Help: "Track lookups answered from the cache.",
	})
	missesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "cache_misses_total",
		Help: "Track lookups that were not cached and started an upstream lookup.",
	})
	coalescedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "cache_coalesced_total",
		Help: "Track lookups that were not cached and joined an upstream lookup already in progress.",
	})
	errorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "cache_errors_total",
		Help: "Cache store errors (the lookup falls back to MusicBrainz).",
	})
)

// TrackSearcher is the lookup being cached; *provider.Client satisfies it.
type TrackSearcher interface {
	SearchTrack(ctx context.Context, artist, title string) (*provider.Track, error)
}

// Config tunes a Searcher.
type Config struct {
	// TTL is how long a well-tagged track stays cached.
	TTL time.Duration
	// LowDataTTL is how long a track with fewer than LowDataTags tags stays
	// cached. Such results are the ones most likely to improve as MusicBrainz
	// users add tags. Zero means DefaultLowDataTTL.
	LowDataTTL time.Duration
	// NegativeTTL is how long "not found" stays cached. Kept short because
	// MusicBrainz data is crowd-sourced and the song may be added.
	NegativeTTL time.Duration
	// Cooldown is how long the cache is bypassed after a store error, so an
	// unreachable Redis costs one timeout rather than one per request.
	// Zero means DefaultCooldown.
	Cooldown time.Duration
	// OpTimeout is the hard deadline for one cache read or write.
	// Zero means DefaultOpTimeout.
	OpTimeout time.Duration
}

// Searcher wraps a TrackSearcher with a read-through cache. Store failures
// never fail a lookup: they are logged, counted and the lookup falls through
// to the wrapped searcher. Concurrent misses for the same track share a single
// upstream lookup, and every caller gets its own copy of the track. A Searcher
// is safe for concurrent use.
type Searcher struct {
	next    TrackSearcher
	store   Store
	cfg     Config
	logger  *slog.Logger
	now     func() time.Time
	flights flightGroup

	// bypassUntil is a unix-nano timestamp; zero means the cache is healthy.
	// It is the only mutable state besides flights and is only touched
	// through atomic operations.
	bypassUntil atomic.Int64
}

// NewSearcher wraps next with a cache backed by store.
func NewSearcher(next TrackSearcher, store Store, cfg Config, logger *slog.Logger) *Searcher {
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = DefaultCooldown
	}
	if cfg.OpTimeout <= 0 {
		cfg.OpTimeout = DefaultOpTimeout
	}
	if cfg.LowDataTTL <= 0 {
		cfg.LowDataTTL = DefaultLowDataTTL
	}
	s := &Searcher{next: next, store: store, cfg: cfg, logger: logger, now: time.Now}
	s.flights.joined = func(leader bool) {
		if leader {
			missesTotal.Inc()
		} else {
			coalescedTotal.Inc()
		}
	}
	s.flights.onPanic = func(key string, recovered any, stack []byte) {
		logger.Error("track lookup panicked",
			"key", key, "panic", fmt.Sprint(recovered), "stack", string(stack))
	}
	return s
}

type entry struct {
	Found bool        `json:"found"`
	Track *entryTrack `json:"track,omitempty"`
}

type entryTrack struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Artist    string     `json:"artist"`
	Tags      []entryTag `json:"tags"`
	TagSource string     `json:"tag_source,omitempty"`
}

type entryTag struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// SearchTrack returns the cached result for (artist, title) when there is
// one, and otherwise asks the wrapped searcher and stores what it returns.
// Upstream failures are never cached.
func (s *Searcher) SearchTrack(ctx context.Context, artist, title string) (*provider.Track, error) {
	key := Key(artist, title)

	if track, err, ok := s.lookup(ctx, key); ok {
		hitsTotal.Inc()
		return track, err
	}

	return s.flights.do(ctx, key, func(flightCtx context.Context) (*provider.Track, error) {
		// flightCtx has no deadline on purpose. The upstream client queues
		// behind its rate limiter, so a burst of different tracks can wait
		// many seconds for a slot; a deadline here would fail the tail of
		// that queue. The HTTP call itself is bounded by the client's own
		// timeout, and the lookup is cancelled once nobody waits for it.
		track, err := s.next.SearchTrack(flightCtx, artist, title)
		switch {
		case err == nil:
			ttl := s.cfg.TTL
			if len(track.Tags) < LowDataTags {
				ttl = s.cfg.LowDataTTL
			}
			s.save(flightCtx, key, entry{Found: true, Track: toEntry(track)}, ttl)
		case errors.Is(err, provider.ErrNotFound):
			s.save(flightCtx, key, entry{Found: false}, s.cfg.NegativeTTL)
		}
		return track, err
	})
}

// Key builds the cache key for a lookup: artist and title are trimmed,
// lower-cased and have inner whitespace collapsed, so "Queen " and "queen"
// share an entry.
func Key(artist, title string) string {
	return keyPrefix + keyPart(artist) + "|" + keyPart(title)
}

func keyPart(s string) string {
	return url.QueryEscape(strings.Join(strings.Fields(strings.ToLower(s)), " "))
}

func (s *Searcher) lookup(ctx context.Context, key string) (track *provider.Track, err error, ok bool) {
	if s.bypassed() {
		return nil, nil, false
	}

	opCtx, cancel := context.WithTimeout(ctx, s.cfg.OpTimeout)
	raw, found, getErr := s.store.Get(opCtx, key)
	cancel()
	if getErr != nil {
		s.fail(ctx, "get", getErr)
		return nil, nil, false
	}
	s.recovered()
	if !found {
		return nil, nil, false
	}

	var e entry
	if jsonErr := json.Unmarshal(raw, &e); jsonErr != nil {
		s.logger.Warn("cache entry is corrupt, ignoring it", "key", key, "error", jsonErr.Error())
		return nil, nil, false
	}
	if !e.Found || e.Track == nil {
		return nil, provider.ErrNotFound, true
	}
	return fromEntry(e.Track), nil, true
}

func (s *Searcher) save(ctx context.Context, key string, e entry, ttl time.Duration) {
	if ttl <= 0 || s.bypassed() {
		return
	}

	raw, err := json.Marshal(e)
	if err != nil {
		s.logger.Warn("cache entry could not be encoded", "key", key, "error", err.Error())
		return
	}

	opCtx, cancel := context.WithTimeout(ctx, s.cfg.OpTimeout)
	defer cancel()

	if err := s.store.Set(opCtx, key, raw, ttl); err != nil {
		s.fail(ctx, "set", err)
	}
}

func (s *Searcher) bypassed() bool {
	until := s.bypassUntil.Load()
	return until != 0 && s.now().UnixNano() < until
}

// fail records a store error and opens the bypass window. ctx is the context
// the operation was started from (not the per-operation deadline): if that
// one has ended, the error says nothing about Redis and is ignored.
func (s *Searcher) fail(ctx context.Context, op string, err error) {
	if ctx.Err() != nil {
		return
	}
	errorsTotal.Inc()
	if s.bypassUntil.Swap(s.now().Add(s.cfg.Cooldown).UnixNano()) == 0 {
		s.logger.Warn("cache unavailable, serving without it",
			"op", op, "error", err.Error(), "retry_in", s.cfg.Cooldown.String())
	}
}

func (s *Searcher) recovered() {
	if s.bypassUntil.Swap(0) != 0 {
		s.logger.Info("cache is available again")
	}
}

func toEntry(t *provider.Track) *entryTrack {
	tags := make([]entryTag, len(t.WeightedTags))
	for i, tag := range t.WeightedTags {
		tags[i] = entryTag{Name: tag.Name, Count: tag.Count}
	}
	return &entryTrack{ID: t.ID, Title: t.Title, Artist: t.Artist, Tags: tags, TagSource: t.TagSource}
}

func fromEntry(e *entryTrack) *provider.Track {
	track := &provider.Track{ID: e.ID, Title: e.Title, Artist: e.Artist, TagSource: e.TagSource, Cached: true}
	for _, tag := range e.Tags {
		track.Tags = append(track.Tags, tag.Name)
		track.WeightedTags = append(track.WeightedTags, provider.Tag{Name: tag.Name, Count: tag.Count})
	}
	return track
}
