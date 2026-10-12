package cache

import (
	"context"
	"encoding/json"
	"errors"
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
	keyPrefix = "track:v1:"
	// DefaultCooldown is how long the cache is bypassed after a store error.
	DefaultCooldown = 30 * time.Second
)

var (
	hitsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "cache_hits_total",
		Help: "Track lookups answered from the cache.",
	})
	missesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "cache_misses_total",
		Help: "Track lookups that had to go to MusicBrainz.",
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
	// TTL is how long a track with tags stays cached.
	TTL time.Duration
	// NegativeTTL is how long "not found" and "found but untagged" stay
	// cached. Kept short because MusicBrainz data is crowd-sourced and such
	// results can change.
	NegativeTTL time.Duration
	// Cooldown is how long the cache is bypassed after a store error, so an
	// unreachable Redis costs one timeout rather than one per request.
	// Zero means DefaultCooldown.
	Cooldown time.Duration
}

// Searcher wraps a TrackSearcher with a read-through cache. Store failures
// never fail a lookup: they are logged, counted and the lookup falls through
// to the wrapped searcher.
type Searcher struct {
	next   TrackSearcher
	store  Store
	cfg    Config
	logger *slog.Logger
	now    func() time.Time

	// bypassUntil is a unix-nano timestamp; zero means the cache is healthy.
	bypassUntil atomic.Int64
}

// NewSearcher wraps next with a cache backed by store.
func NewSearcher(next TrackSearcher, store Store, cfg Config, logger *slog.Logger) *Searcher {
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = DefaultCooldown
	}
	return &Searcher{next: next, store: store, cfg: cfg, logger: logger, now: time.Now}
}

type entry struct {
	Found bool        `json:"found"`
	Track *entryTrack `json:"track,omitempty"`
}

type entryTrack struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Artist string     `json:"artist"`
	Tags   []entryTag `json:"tags"`
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
	missesTotal.Inc()

	track, err := s.next.SearchTrack(ctx, artist, title)
	switch {
	case err == nil:
		ttl := s.cfg.TTL
		if len(track.Tags) == 0 {
			ttl = s.cfg.NegativeTTL
		}
		s.save(ctx, key, entry{Found: true, Track: toEntry(track)}, ttl)
	case errors.Is(err, provider.ErrNotFound):
		s.save(ctx, key, entry{Found: false}, s.cfg.NegativeTTL)
	}
	return track, err
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

	raw, found, getErr := s.store.Get(ctx, key)
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

	// The caller's context may already be cancelled (client went away) while
	// the result is still worth keeping, so the write gets its own deadline.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()

	if err := s.store.Set(writeCtx, key, raw, ttl); err != nil {
		s.fail(writeCtx, "set", err)
	}
}

func (s *Searcher) bypassed() bool {
	until := s.bypassUntil.Load()
	return until != 0 && s.now().UnixNano() < until
}

// fail records a store error and opens the bypass window. Errors caused by the
// caller's own context ending say nothing about Redis and are ignored.
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
	return &entryTrack{ID: t.ID, Title: t.Title, Artist: t.Artist, Tags: tags}
}

func fromEntry(e *entryTrack) *provider.Track {
	track := &provider.Track{ID: e.ID, Title: e.Title, Artist: e.Artist, Cached: true}
	for _, tag := range e.Tags {
		track.Tags = append(track.Tags, tag.Name)
		track.WeightedTags = append(track.WeightedTags, provider.Tag{Name: tag.Name, Count: tag.Count})
	}
	return track
}
